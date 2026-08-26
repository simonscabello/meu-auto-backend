package integration

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// The odometer is where MVP-2 lands. Abastecimento writes into odometer_readings and into
// the cost totals, which is exactly where a silent regression would sit unnoticed until
// somebody's mileage history was wrong. These tests pin the behaviour it will have to keep.

// TestOdometerCacheIsMaintainedByTheTrigger covers SPEC.md D-11: vehicles.current_mileage_km
// is maintained by a database trigger, and no Go code recalculates it.
//
// The interesting direction is downwards. A trigger that incremented a counter would pass
// the first two assertions and fail the third — deleting the newest reading has to make the
// cache fall back to the one before it, which only works if it is recomputed from the table.
func TestOdometerCacheIsMaintainedByTheTrigger(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	u := e.newUser()
	vehicleID := u.createVehicle(map[string]any{"current_mileage_km": 50_000})

	if km := u.currentMileage(vehicleID); km != 50_000 {
		t.Fatalf("after creation current_mileage_km = %d, want 50000", km)
	}

	// A plain reading moves it forward.
	latest := u.createReading(vehicleID, 62_000, "")
	if km := u.currentMileage(vehicleID); km != 62_000 {
		t.Fatalf("after a reading current_mileage_km = %d, want 62000", km)
	}

	// So does one written by another module: a maintenance record carries a mileage and
	// inserts its own reading, tagged with its own source, inside its own transaction.
	u.createRecord(vehicleID, 70_000, "")
	if km := u.currentMileage(vehicleID); km != 70_000 {
		t.Fatalf("after a maintenance record current_mileage_km = %d, want 70000", km)
	}

	// And deleting a reading has to walk it back down. A mistyped reading is noise, not
	// history, which is why it is a hard delete (SPEC.md D-10) — and why the cache cannot
	// be a running maximum.
	u.createReading(vehicleID, 88_000, "")
	newest := u.latestReadingID(vehicleID)
	u.delete("/v1/odometer/"+newest, nil).expect(http.StatusNoContent)

	if km := u.currentMileage(vehicleID); km != 70_000 {
		t.Fatalf("after deleting the newest reading current_mileage_km = %d, want 70000", km)
	}

	// The reading deleted above is gone for good; the earlier one is untouched.
	u.delete("/v1/odometer/"+newest, nil).expectError(http.StatusNotFound, "not_found")
	u.delete("/v1/odometer/"+latest, nil).expect(http.StatusNoContent)
}

// TestOdometerRollbackIsRefusedAndCorrectable is RN-01.
//
// A refusal is a 422 the client can override, not a hard block: odometers really do get
// replaced, and a product that calls its user a liar about their own car loses. Both halves
// are contract — the code the app switches on, and the details it puts in front of the user.
func TestOdometerRollbackIsRefusedAndCorrectable(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	u := e.newUser()
	vehicleID := u.createVehicle(map[string]any{"current_mileage_km": 80_000})
	path := fmt.Sprintf("/v1/vehicles/%s/odometer", vehicleID)

	res := u.post(path, map[string]any{"mileage_km": 60_000, "source": "manual"}).
		expectError(http.StatusUnprocessableEntity, "odometer_rollback")

	var body struct {
		Error struct {
			Details struct {
				PreviousMileageKm  int    `json:"previous_mileage_km"`
				PreviousOccurredOn string `json:"previous_occurred_on"`
				SubmittedMileageKm int    `json:"submitted_mileage_km"`
			} `json:"details"`
		} `json:"error"`
	}
	res.decode(&body)

	if body.Error.Details.PreviousMileageKm != 80_000 {
		t.Errorf("details.previous_mileage_km = %d, want 80000",
			body.Error.Details.PreviousMileageKm)
	}
	if body.Error.Details.SubmittedMileageKm != 60_000 {
		t.Errorf("details.submitted_mileage_km = %d, want 60000",
			body.Error.Details.SubmittedMileageKm)
	}

	// The refusal must not have written anything.
	if km := u.currentMileage(vehicleID); km != 80_000 {
		t.Fatalf("a refused reading changed the cache to %d", km)
	}

	// Source "correction" is the deliberate override — the dashboard was replaced, or the
	// earlier number was wrong.
	u.post(path, map[string]any{"mileage_km": 60_000, "source": "correction"}).
		expect(http.StatusCreated)

	// The cache follows the newest reading, not the largest number.
	if km := u.currentMileage(vehicleID); km != 60_000 {
		t.Fatalf("after a correction current_mileage_km = %d, want 60000", km)
	}
}

// TestBackdatedReadingIsCheckedAgainstItsNeighbours covers the part of RN-01 that is easy to
// get wrong: the comparison is against the reading's neighbours in time, not against the
// vehicle's current mileage. Entering a reading you forgot three months ago has to work.
func TestBackdatedReadingIsCheckedAgainstItsNeighbours(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	u := e.newUser()
	today := e.today()

	// Two anchors: 40.000 km ninety days ago, 50.000 km today.
	vehicleID := u.createVehicle(map[string]any{"current_mileage_km": 50_000})
	u.createReading(vehicleID, 40_000, today.AddDate(0, 0, -90).Format(time.DateOnly))

	path := fmt.Sprintf("/v1/vehicles/%s/odometer", vehicleID)
	between := today.AddDate(0, 0, -45).Format(time.DateOnly)

	// A value that sits between them is fine, even though it is below the current mileage.
	u.post(path, map[string]any{
		"mileage_km": 45_000, "occurred_on": between, "source": "manual",
	}).expect(http.StatusCreated)

	// Below the earlier neighbour is not.
	u.post(path, map[string]any{
		"mileage_km": 30_000, "occurred_on": between, "source": "manual",
	}).expectError(http.StatusUnprocessableEntity, "odometer_rollback")

	// Neither is above the later one.
	u.post(path, map[string]any{
		"mileage_km": 60_000, "occurred_on": between, "source": "manual",
	}).expectError(http.StatusUnprocessableEntity, "odometer_rollback")

	// None of that moved the cache: the newest reading is still today's.
	if km := u.currentMileage(vehicleID); km != 50_000 {
		t.Fatalf("current_mileage_km = %d, want 50000", km)
	}
}

// TestOdometerPaginationWalksEveryReadingExactlyOnce exercises the keyset cursor.
//
// Offset pagination was ruled out (SPEC.md section 7) because a row inserted during a walk
// shifts every later page. A keyset cursor does not, but it has its own failure — a tie on
// the sort column can repeat or skip a row — and only a real database with real ties shows it.
func TestOdometerPaginationWalksEveryReadingExactlyOnce(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	u := e.newUser()
	vehicleID := u.createVehicle(map[string]any{"current_mileage_km": 10_000})

	// All on the same day, so occurred_on ties on every row and the cursor has to break the
	// tie with something else.
	today := e.today().Format(time.DateOnly)
	const readings = 25
	for i := 1; i <= readings; i++ {
		u.createReading(vehicleID, 10_000+i*100, today)
	}

	seen := map[string]bool{}
	cursor := ""
	for page := 0; ; page++ {
		if page > readings {
			t.Fatal("pagination did not terminate")
		}

		url := fmt.Sprintf("/v1/vehicles/%s/odometer?limit=7", vehicleID)
		if cursor != "" {
			url += "&cursor=" + cursor
		}

		var body struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
			NextCursor *string `json:"next_cursor"`
		}
		u.get(url).expect(http.StatusOK).decode(&body)

		for _, reading := range body.Data {
			if seen[reading.ID] {
				t.Fatalf("reading %s appeared on two pages", reading.ID)
			}
			seen[reading.ID] = true
		}

		if body.NextCursor == nil {
			break
		}
		cursor = *body.NextCursor
	}

	// 25 readings plus the opening one recorded when the vehicle was created.
	if len(seen) != readings+1 {
		t.Fatalf("walked %d readings, want %d", len(seen), readings+1)
	}
}

// TestClientSuppliedIdsMakeRetriesIdempotent covers the decision to use a client-generated
// UUIDv7 instead of an idempotency-key table (SPEC.md section 7).
//
// The case it protects against is ordinary: a request succeeds, the response is lost on a
// patchy mobile connection, and the app retries. Without this, the user has two cars.
func TestClientSuppliedIdsMakeRetriesIdempotent(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	u := e.newUser()
	id := "0192c4a1-0000-7000-8000-00000000f00d"

	body := map[string]any{
		"id": id, "brand": "Fiat", "model": "Uno", "current_mileage_km": 30_000,
	}

	first := u.post("/v1/vehicles", body).expect(http.StatusCreated)

	// The retry is answered with the resource that already exists — 200, not a second 201
	// and not a conflict the app would have to teach the user about.
	second := u.post("/v1/vehicles", body).expect(http.StatusOK)

	if first.id() != second.id() {
		t.Fatalf("retry returned a different vehicle: %s then %s", first.id(), second.id())
	}

	var page struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	u.get("/v1/vehicles").expect(http.StatusOK).decode(&page)
	if len(page.Data) != 1 {
		t.Fatalf("a retried creation left %d vehicles behind", len(page.Data))
	}
}

// ---------- helpers ----------

func (u *user) currentMileage(vehicleID string) int32 {
	u.t.Helper()

	var body struct {
		CurrentMileageKm int32 `json:"current_mileage_km"`
	}
	u.get("/v1/vehicles/" + vehicleID).expect(http.StatusOK).decode(&body)
	return body.CurrentMileageKm
}

func (u *user) latestReadingID(vehicleID string) string {
	u.t.Helper()

	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	u.get(fmt.Sprintf("/v1/vehicles/%s/odometer?limit=1", vehicleID)).
		expect(http.StatusOK).decode(&body)

	if len(body.Data) == 0 {
		u.t.Fatalf("vehicle %s has no readings", vehicleID)
	}
	return body.Data[0].ID
}

// countRows is for the assertions the API deliberately cannot answer — what is still on
// disk after a soft delete, whether an aggregate wrote half of itself. It is the only place
// these tests look past the HTTP surface, and it never sets up state: a fixture built in SQL
// could construct something the API would have refused.
func (e *env) countRows(t *testing.T, query string, args ...any) int {
	t.Helper()

	var count int
	if err := e.db.Pool.QueryRow(context.Background(), query, args...).Scan(&count); err != nil {
		t.Fatalf("count rows: %v\nquery: %s", err, query)
	}
	return count
}
