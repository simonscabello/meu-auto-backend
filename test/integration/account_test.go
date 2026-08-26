package integration

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// TestRefreshRotatesAndDetectsReuse covers the reason the refresh token is opaque and
// rotating rather than a long-lived JWT (SPEC.md D-10).
//
// Rotation alone does not protect anyone: if a stolen token simply stopped working, the
// thief would just use it first and the owner would be logged out with no explanation.
// What makes it a defence is the second half — a token presented twice means somebody has
// a copy, and the whole family is revoked.
func TestRefreshRotatesAndDetectsReuse(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	u := e.newUser()
	original := u.RefreshToken

	var rotated struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	e.anonymous().post("/v1/auth/refresh", map[string]any{"refresh_token": original}).
		expect(http.StatusOK).decode(&rotated)

	if rotated.RefreshToken == original {
		t.Fatal("the refresh token was not rotated")
	}

	// The new one works.
	e.anonymous().withToken(rotated.AccessToken).get("/v1/me").expect(http.StatusOK)

	// Presenting the old one again is the signal that it leaked.
	e.anonymous().post("/v1/auth/refresh", map[string]any{"refresh_token": original}).
		expectError(http.StatusUnauthorized, "unauthorized")

	// And it takes the whole family with it: the token issued a moment ago is now dead too,
	// so whoever holds the copy cannot keep the session alive.
	e.anonymous().post("/v1/auth/refresh", map[string]any{"refresh_token": rotated.RefreshToken}).
		expectError(http.StatusUnauthorized, "unauthorized")
}

// TestLogoutRevokesThePresentedSession. Signing out on one phone leaves the other one
// signed in — the same account works across devices by design.
func TestLogoutRevokesThePresentedSession(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	u := e.newUser()
	second := e.loginAgain(u)

	e.anonymous().post("/v1/auth/logout", map[string]any{"refresh_token": u.RefreshToken}).
		expect(http.StatusNoContent)

	e.anonymous().post("/v1/auth/refresh", map[string]any{"refresh_token": u.RefreshToken}).
		expectError(http.StatusUnauthorized, "unauthorized")

	// The other device is untouched.
	e.anonymous().post("/v1/auth/refresh", map[string]any{"refresh_token": second}).
		expect(http.StatusOK)
}

// TestReplayingALoggedOutTokenLeavesOtherSessionsAlone is the half of reuse detection that
// has to stay switched off.
//
// A logout the app retried on a bad connection presents a revoked token twice, and on a
// Brazilian mobile network that is an ordinary event rather than an attack. Before
// migration 000008 the two were indistinguishable — logout and rotation both wrote
// revoked_at and nothing else — so the retry ended every session the account had, and the
// owner was signed out of their tablet for signing out of their phone.
//
// The alarm is for a *rotated* token replayed, which is the case where the legitimate
// client holds the successor. TestRefreshRotatesAndDetectsReuse covers that one; this
// covers the noise it must not fire on.
func TestReplayingALoggedOutTokenLeavesOtherSessionsAlone(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	u := e.newUser()
	second := e.loginAgain(u)

	e.anonymous().post("/v1/auth/logout", map[string]any{"refresh_token": u.RefreshToken}).
		expect(http.StatusNoContent)

	// The retry the app sends after a timed-out logout, and then a third for good measure:
	// a replayed dead token stays dead and stays quiet, however often it arrives.
	for range 3 {
		e.anonymous().post("/v1/auth/refresh", map[string]any{"refresh_token": u.RefreshToken}).
			expectError(http.StatusUnauthorized, "unauthorized")
	}

	e.anonymous().post("/v1/auth/refresh", map[string]any{"refresh_token": second}).
		expect(http.StatusOK)
}

// loginAgain signs the same account in a second time and returns the new refresh token, the
// way a second device would.
func (e *env) loginAgain(u *user) string {
	e.t.Helper()

	var session struct {
		RefreshToken string `json:"refresh_token"`
	}
	e.anonymous().post("/v1/auth/login", map[string]any{
		"email": u.Email, "password": u.Password,
	}).expect(http.StatusOK).decode(&session)

	return session.RefreshToken
}

// TestPasswordResetRunsEndToEnd follows the whole flow, including the part that never
// appears in a response: the token only exists inside the e-mail.
func TestPasswordResetRunsEndToEnd(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	u := e.newUser()
	const newPassword = "outra-senha-de-teste"

	e.anonymous().post("/v1/auth/password-reset/request", map[string]any{"email": u.Email}).
		expect(http.StatusAccepted)

	token := e.mailer.lastResetToken(t)

	e.anonymous().post("/v1/auth/password-reset/confirm", map[string]any{
		"token": token, "password": newPassword,
	}).expect(http.StatusNoContent)

	// The new password works and the old one does not.
	e.anonymous().post("/v1/auth/login", map[string]any{
		"email": u.Email, "password": newPassword,
	}).expect(http.StatusOK)

	e.anonymous().post("/v1/auth/login", map[string]any{
		"email": u.Email, "password": u.Password,
	}).expectError(http.StatusUnauthorized, "unauthorized")

	// A reset token is single use: a forwarded or intercepted e-mail must not be a spare key.
	e.anonymous().post("/v1/auth/password-reset/confirm", map[string]any{
		"token": token, "password": "mais-uma-senha-123",
	}).expectError(http.StatusUnauthorized, "unauthorized")

	// Resetting a password ends every session — that is the point of resetting it. The
	// sessions it ends are revoked with their own reason, so an old client still retrying
	// with a dead token cannot knock out the session the owner has just signed back in
	// with (migration 000008).
	e.anonymous().post("/v1/auth/refresh", map[string]any{"refresh_token": u.RefreshToken}).
		expectError(http.StatusUnauthorized, "unauthorized")

	var fresh struct {
		RefreshToken string `json:"refresh_token"`
	}
	e.anonymous().post("/v1/auth/login", map[string]any{
		"email": u.Email, "password": newPassword,
	}).expect(http.StatusOK).decode(&fresh)

	e.anonymous().post("/v1/auth/refresh", map[string]any{"refresh_token": u.RefreshToken}).
		expectError(http.StatusUnauthorized, "unauthorized")

	e.anonymous().post("/v1/auth/refresh", map[string]any{"refresh_token": fresh.RefreshToken}).
		expect(http.StatusOK)
}

// TestPasswordResetDoesNotRevealWhoHasAnAccount. The endpoint answers the same way for an
// address nobody registered — otherwise it is an account enumeration oracle.
func TestPasswordResetDoesNotRevealWhoHasAnAccount(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	u := e.newUser()

	known := e.anonymous().post("/v1/auth/password-reset/request",
		map[string]any{"email": u.Email}).expect(http.StatusAccepted)
	unknown := e.anonymous().post("/v1/auth/password-reset/request",
		map[string]any{"email": "ninguem@example.test"}).expect(http.StatusAccepted)

	if string(known.Body) != string(unknown.Body) {
		t.Errorf("the response differs for a registered address:\n  known:   %s\n  unknown: %s",
			known.Body, unknown.Body)
	}
	if len(e.mailer.sent) != 1 {
		t.Errorf("%d e-mails were sent for two requests, one of them to nobody",
			len(e.mailer.sent))
	}
}

// TestDeletingAnAccountErasesTheVehicles is the LGPD path in SPEC.md D-10, and the reason
// identity.UserDataEraser exists at all: vehicles carry no user_id, so the database cascade
// from users cannot reach them. If the eraser is ever unregistered in the wiring, this is
// the test that notices — the account disappears and the cars stay behind forever.
func TestDeletingAnAccountErasesTheVehicles(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	u := e.newUser()
	vehicleID := u.createVehicle()
	u.createRecord(vehicleID, 55_000, "")
	u.createObligation(vehicleID)
	u.createSeguro(vehicleID)

	// A stolen access token must not be enough: deletion is irreversible and cascades.
	u.delete("/v1/me", map[string]any{"password": "senha-errada"}).
		expectError(http.StatusUnauthorized, "unauthorized")

	if n := e.countRows(t, `SELECT count(*) FROM vehicles WHERE id = $1`,
		uuid.MustParse(vehicleID)); n != 1 {
		t.Fatal("a refused deletion removed the vehicle anyway")
	}

	u.delete("/v1/me", map[string]any{"password": u.Password}).expect(http.StatusNoContent)

	// Not a soft delete this time. Vehicles are soft deleted when their owner removes one
	// (the history is the product's asset); an account erasure has to leave nothing.
	for _, table := range []string{
		"vehicles", "odometer_readings", "maintenance_records", "vehicle_obligations", "seguros",
	} {
		if n := e.countRows(t, `SELECT count(*) FROM `+table); n != 0 {
			t.Errorf("%s still holds %d rows after the account was erased", table, n)
		}
	}
	if n := e.countRows(t, `SELECT count(*) FROM users`); n != 0 {
		t.Errorf("users still holds %d rows", n)
	}

	// The catalogue is global and seeded by a migration that will not run again. Erasing an
	// account must not take it out — that would break the next account created, and the
	// failure would surface somewhere else entirely (CLAUDE.md).
	if n := e.countRows(t, `SELECT count(*) FROM maintenance_items WHERE owner_user_id IS NULL`); n == 0 {
		t.Error("erasing an account wiped the global maintenance catalogue")
	}
}

// TestDeletedVehicleKeepsItsHistoryOnDisk. A vehicle removed by its owner is soft deleted:
// the API stops showing it, and the years of service history behind it stay where they are.
// The whole product rests on that record being defensible, and one wrong tap cannot be what
// destroys it (SPEC.md D-10).
func TestDeletedVehicleKeepsItsHistoryOnDisk(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	u := e.newUser()
	vehicleID := u.createVehicle()
	recordID := u.createRecord(vehicleID, 55_000, "")

	u.delete("/v1/vehicles/"+vehicleID, nil).expect(http.StatusNoContent)

	// Gone from the API, and gone the same way a vehicle that never existed is gone.
	u.get("/v1/vehicles/"+vehicleID).expectError(http.StatusNotFound, "not_found")

	var page struct {
		Data []any `json:"data"`
	}
	u.get("/v1/vehicles").expect(http.StatusOK).decode(&page)
	if len(page.Data) != 0 {
		t.Fatalf("the deleted vehicle is still listed (%d vehicles)", len(page.Data))
	}

	// Still on disk, with the deletion recorded rather than the row removed.
	if n := e.countRows(t, `SELECT count(*) FROM vehicles WHERE id = $1 AND deleted_at IS NOT NULL`,
		uuid.MustParse(vehicleID)); n != 1 {
		t.Error("the vehicle was hard deleted")
	}
	if n := e.countRows(t, `SELECT count(*) FROM maintenance_records WHERE id = $1`,
		uuid.MustParse(recordID)); n != 1 {
		t.Error("deleting the vehicle destroyed its service history")
	}
}
