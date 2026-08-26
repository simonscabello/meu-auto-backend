package integration

import (
	"net/http"
	"testing"
)

// TestBootAndSeed is the suite's canary. If it fails, nothing below it is worth reading:
// either the container did not come up, the migrations did not apply, or the catalogue
// migration 000005 did not seed.
func TestBootAndSeed(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	e.anonymous().get("/healthz").expect(http.StatusOK)
	e.anonymous().get("/readyz").expect(http.StatusOK)

	u := e.newUser()

	var catalogue struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Slug string `json:"slug"`
		} `json:"data"`
	}
	u.get("/v1/maintenance-items?vehicle_type=car").expect(http.StatusOK).decode(&catalogue)

	if len(catalogue.Data) == 0 {
		t.Fatal("the maintenance catalogue is empty: migration 000005 did not seed")
	}
}

// TestUnknownRouteUsesTheErrorEnvelope covers the decision in SPEC.md section 7 that a
// typo'd path must not be the one response shaped differently from all the others — chi's
// own 404 and 405 answer in text/plain with an empty body.
func TestUnknownRouteUsesTheErrorEnvelope(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	e.anonymous().get("/v1/nao-existe").
		expectError(http.StatusNotFound, "not_found")

	// /healthz is registered for GET only.
	e.anonymous().post("/healthz", nil).
		expectError(http.StatusMethodNotAllowed, "method_not_allowed")
}
