package integration

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// Authorisation is the one thing in this API that has to be right on every single route.
// SPEC.md RN-07 states the rule and CLAUDE.md repeats it: every query filters by
// ownership, and a resource the caller may not see is reported as 404, never 403 —
// "forbidden" would confirm that the id exists, which is all an attacker needs to probe.
//
// A test per endpoint would have covered today's endpoints and none of tomorrow's. So the
// rule is expressed as a table, and TestEveryProtectedRouteIsInTheMatrix walks the router
// and fails if a route exists that the table does not mention. Adding an endpoint without
// deciding how it is authorised breaks the build.

// isolation is what a route does when a stranger calls it.
type isolation int

const (
	// hiddenFromStrangers: the route addresses a specific resource, and a caller who does
	// not own it must be told it does not exist.
	hiddenFromStrangers isolation = iota

	// callerScoped: the route addresses the caller's own data (/v1/me) or a global
	// catalogue every account may read. There is no other user's resource to hide, so
	// isolation is asserted by the focused tests further down instead.
	callerScoped
)

// protectedRoute is one authenticated endpoint and how to call it.
type protectedRoute struct {
	method  string
	pattern string // the chi pattern, matched against the router by the coverage guard

	// path builds a concrete URL pointing at the owner's resources.
	path func(f *ownedResources) string

	// body is sent on the cross-user call. It has to be *valid*: a handler decodes before
	// it delegates, so an invalid body would be rejected at 422 and the request would
	// never reach the ownership check the test is here to exercise.
	body func(f *ownedResources) any

	isolation isolation
}

// ownedResources is one full set of resources belonging to a single account.
type ownedResources struct {
	vehicleID    string
	readingID    string
	planID       string
	recordID     string
	obligationID string
	seguroID     string
	itemID       string
}

func newOwnedResources(u *user) *ownedResources {
	u.t.Helper()

	vehicleID := u.createVehicle()
	return &ownedResources{
		vehicleID:    vehicleID,
		readingID:    u.createReading(vehicleID, 51_000, ""),
		planID:       u.firstPlanID(vehicleID),
		recordID:     u.createRecord(vehicleID, 52_000, ""),
		obligationID: u.createObligation(vehicleID),
		seguroID:     u.createSeguro(vehicleID),
		itemID:       u.firstItemID(),
	}
}

// placeholderCatalogID stands in for a catalogue id in the routes below. Nothing in this
// file ever reaches a handler with it: the anonymous matrix is refused by the middleware,
// and the isolation tests only walk hiddenFromStrangers routes.
const placeholderCatalogID = "00000000-0000-0000-0000-000000000000"

func protectedRoutes() []protectedRoute {
	// Bodies that satisfy validation without depending on any state of their own. A PATCH
	// with {} is valid everywhere here: an absent field means "leave unchanged".
	empty := func(*ownedResources) any { return map[string]any{} }

	return []protectedRoute{
		// ---------- identity ----------
		{http.MethodGet, "/v1/me",
			func(*ownedResources) string { return "/v1/me" }, nil, callerScoped},
		{http.MethodPatch, "/v1/me",
			func(*ownedResources) string { return "/v1/me" },
			func(*ownedResources) any { return map[string]any{"name": "Nome Novo"} }, callerScoped},
		{http.MethodDelete, "/v1/me",
			func(*ownedResources) string { return "/v1/me" },
			func(*ownedResources) any { return map[string]any{"password": "irrelevante-aqui"} },
			callerScoped},

		// ---------- vehicle ----------
		{http.MethodGet, "/v1/vehicles",
			func(*ownedResources) string { return "/v1/vehicles" }, nil, callerScoped},
		{http.MethodPost, "/v1/vehicles",
			func(*ownedResources) string { return "/v1/vehicles" },
			func(*ownedResources) any {
				return map[string]any{"brand": "Fiat", "model": "Uno"}
			}, callerScoped},

		{http.MethodGet, "/v1/vehicles/{vehicleID}", vehiclePath(""), nil, hiddenFromStrangers},
		{http.MethodPatch, "/v1/vehicles/{vehicleID}", vehiclePath(""), empty, hiddenFromStrangers},
		{http.MethodDelete, "/v1/vehicles/{vehicleID}", vehiclePath(""), nil, hiddenFromStrangers},

		{http.MethodGet, "/v1/vehicles/{vehicleID}/odometer",
			vehiclePath("/odometer"), nil, hiddenFromStrangers},
		{http.MethodPost, "/v1/vehicles/{vehicleID}/odometer",
			vehiclePath("/odometer"),
			func(*ownedResources) any { return map[string]any{"mileage_km": 60_000} },
			hiddenFromStrangers},
		{http.MethodDelete, "/v1/odometer/{readingID}",
			func(f *ownedResources) string { return "/v1/odometer/" + f.readingID },
			nil, hiddenFromStrangers},

		// ---------- vehicle catalogue ----------
		//
		// callerScoped, and the reason is worth stating: the catalogue is reference data
		// every account may read — brands and models are public facts, and there is no
		// other user's resource to hide behind them.
		//
		// What these routes DO gate is cost. A request that misses the mirror spends part
		// of a daily quota shared by every user, so they sit behind the token even though
		// the data is not secret. The ids below are fixed placeholders: the anonymous
		// matrix is refused before any handler runs, and the two isolation tests skip
		// callerScoped routes entirely.
		{http.MethodGet, "/v1/vehicle-brands",
			func(*ownedResources) string { return "/v1/vehicle-brands" }, nil, callerScoped},
		{http.MethodGet, "/v1/vehicle-brands/{brandID}/models",
			func(*ownedResources) string {
				return "/v1/vehicle-brands/" + placeholderCatalogID + "/models"
			}, nil, callerScoped},
		{http.MethodGet, "/v1/vehicle-models/{modelID}/years",
			func(*ownedResources) string {
				return "/v1/vehicle-models/" + placeholderCatalogID + "/years"
			}, nil, callerScoped},
		{http.MethodGet, "/v1/vehicle-model-years/{modelYearID}",
			func(*ownedResources) string {
				return "/v1/vehicle-model-years/" + placeholderCatalogID
			}, nil, callerScoped},

		// ---------- maintenance ----------
		{http.MethodGet, "/v1/maintenance-items",
			func(*ownedResources) string { return "/v1/maintenance-items" }, nil, callerScoped},
		{http.MethodPost, "/v1/maintenance-items",
			func(*ownedResources) string { return "/v1/maintenance-items" },
			func(*ownedResources) any {
				return map[string]any{"name": "Item personalizado", "default_interval_km": 10_000}
			}, callerScoped},

		{http.MethodGet, "/v1/vehicles/{vehicleID}/maintenance-plans",
			vehiclePath("/maintenance-plans"), nil, hiddenFromStrangers},
		{http.MethodPost, "/v1/vehicles/{vehicleID}/maintenance-plans",
			vehiclePath("/maintenance-plans"),
			func(f *ownedResources) any {
				return map[string]any{"maintenance_item_id": f.itemID}
			}, hiddenFromStrangers},
		{http.MethodGet, "/v1/maintenance-plans/{planID}",
			func(f *ownedResources) string { return "/v1/maintenance-plans/" + f.planID },
			nil, hiddenFromStrangers},
		{http.MethodPatch, "/v1/maintenance-plans/{planID}",
			func(f *ownedResources) string { return "/v1/maintenance-plans/" + f.planID },
			empty, hiddenFromStrangers},
		{http.MethodDelete, "/v1/maintenance-plans/{planID}",
			func(f *ownedResources) string { return "/v1/maintenance-plans/" + f.planID },
			nil, hiddenFromStrangers},

		{http.MethodGet, "/v1/vehicles/{vehicleID}/maintenance-profile",
			vehiclePath("/maintenance-profile"), nil, hiddenFromStrangers},
		{http.MethodPost, "/v1/vehicles/{vehicleID}/maintenance-profile/answers",
			vehiclePath("/maintenance-profile/answers"),
			func(*ownedResources) any {
				return map[string]any{"question": "timing_drive", "answer": "unknown"}
			}, hiddenFromStrangers},

		{http.MethodGet, "/v1/vehicles/{vehicleID}/maintenance-records",
			vehiclePath("/maintenance-records"), nil, hiddenFromStrangers},
		{http.MethodPost, "/v1/vehicles/{vehicleID}/maintenance-records",
			vehiclePath("/maintenance-records"),
			func(f *ownedResources) any {
				return map[string]any{
					"mileage_km": 60_000,
					"kind":       "performed",
					"items":      []map[string]any{{"maintenance_item_id": f.itemID}},
				}
			}, hiddenFromStrangers},
		{http.MethodGet, "/v1/maintenance-records/{recordID}",
			func(f *ownedResources) string { return "/v1/maintenance-records/" + f.recordID },
			nil, hiddenFromStrangers},
		{http.MethodPatch, "/v1/maintenance-records/{recordID}",
			func(f *ownedResources) string { return "/v1/maintenance-records/" + f.recordID },
			empty, hiddenFromStrangers},
		{http.MethodDelete, "/v1/maintenance-records/{recordID}",
			func(f *ownedResources) string { return "/v1/maintenance-records/" + f.recordID },
			nil, hiddenFromStrangers},

		// ---------- obligation ----------
		{http.MethodGet, "/v1/vehicles/{vehicleID}/obligations",
			vehiclePath("/obligations"), nil, hiddenFromStrangers},
		{http.MethodPost, "/v1/vehicles/{vehicleID}/obligations",
			vehiclePath("/obligations"),
			func(f *ownedResources) any {
				return map[string]any{
					"kind": "ipva", "reference_year": 2026, "due_on": "2026-03-10",
				}
			}, hiddenFromStrangers},
		{http.MethodGet, "/v1/obligations/{obligationID}",
			func(f *ownedResources) string { return "/v1/obligations/" + f.obligationID },
			nil, hiddenFromStrangers},
		{http.MethodPatch, "/v1/obligations/{obligationID}",
			func(f *ownedResources) string { return "/v1/obligations/" + f.obligationID },
			empty, hiddenFromStrangers},
		{http.MethodDelete, "/v1/obligations/{obligationID}",
			func(f *ownedResources) string { return "/v1/obligations/" + f.obligationID },
			nil, hiddenFromStrangers},

		{http.MethodGet, "/v1/vehicles/{vehicleID}/seguros",
			vehiclePath("/seguros"), nil, hiddenFromStrangers},
		{http.MethodPost, "/v1/vehicles/{vehicleID}/seguros",
			vehiclePath("/seguros"),
			func(f *ownedResources) any {
				return map[string]any{
					"insurer_name": "Seguradora X",
					"starts_on":    "2026-01-01",
					"ends_on":      "2026-12-31",
				}
			}, hiddenFromStrangers},
		{http.MethodGet, "/v1/seguros/{seguroID}",
			func(f *ownedResources) string { return "/v1/seguros/" + f.seguroID },
			nil, hiddenFromStrangers},
		{http.MethodPatch, "/v1/seguros/{seguroID}",
			func(f *ownedResources) string { return "/v1/seguros/" + f.seguroID },
			empty, hiddenFromStrangers},
		{http.MethodDelete, "/v1/seguros/{seguroID}",
			func(f *ownedResources) string { return "/v1/seguros/" + f.seguroID },
			nil, hiddenFromStrangers},

		// ---------- insight ----------
		{http.MethodGet, "/v1/vehicles/{vehicleID}/dashboard",
			vehiclePath("/dashboard"), nil, hiddenFromStrangers},
		{http.MethodGet, "/v1/vehicles/{vehicleID}/alerts",
			vehiclePath("/alerts"), nil, hiddenFromStrangers},
		{http.MethodGet, "/v1/vehicles/{vehicleID}/timeline",
			vehiclePath("/timeline"), nil, hiddenFromStrangers},
	}
}

func vehiclePath(suffix string) func(*ownedResources) string {
	return func(f *ownedResources) string { return "/v1/vehicles/" + f.vehicleID + suffix }
}

// TestProtectedRoutesRejectAnonymousCallers checks the outer gate: no token and a forged
// token are both refused, with the same code, before any handler runs.
//
// Every request here is rejected, so the whole table shares one environment — nothing it
// does can change the state the next case sees.
func TestProtectedRoutesRejectAnonymousCallers(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	owner := e.newUser()
	owned := newOwnedResources(owner)

	// A syntactically valid JWT signed with the wrong key. The middleware must not
	// distinguish it from a missing header: telling an attacker which part of the guess
	// was wrong is help they should not get.
	const forged = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9." +
		"eyJzdWIiOiIwMTkyYzRhMS0wMDAwLTcwMDAtODAwMC0wMDAwMDAwMDAwMDAiLCJpc3MiOiJtZXUtYXV0byJ9." +
		"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

	for _, route := range protectedRoutes() {
		t.Run(route.method+" "+route.pattern, func(t *testing.T) {
			path := route.path(owned)
			body := bodyFor(route, owned)

			e.anonymous().do(route.method, path, body).
				expectError(http.StatusUnauthorized, "unauthorized")

			e.anonymous().withToken(forged).do(route.method, path, body).
				expectError(http.StatusUnauthorized, "unauthorized")

			e.anonymous().withToken("not-even-a-jwt").do(route.method, path, body).
				expectError(http.StatusUnauthorized, "unauthorized")
		})
	}
}

// TestStrangersGetNotFoundNeverForbidden is RN-07 itself: a second account, fully
// authenticated, calling every resource-scoped route with the first account's ids.
//
// The assertion is on the code as much as on the status. 403 and 404 are both refusals;
// only one of them keeps the id secret.
func TestStrangersGetNotFoundNeverForbidden(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	owner := e.newUser()
	owned := newOwnedResources(owner)

	// The stranger has an account and a vehicle of their own, so nothing here is refused
	// for want of a valid session or an empty database.
	stranger := e.newUser()
	stranger.createVehicle(map[string]any{"brand": "Renault", "model": "Kwid"})

	for _, route := range protectedRoutes() {
		if route.isolation != hiddenFromStrangers {
			continue
		}
		t.Run(route.method+" "+route.pattern, func(t *testing.T) {
			stranger.do(route.method, route.path(owned), bodyFor(route, owned)).
				expectError(http.StatusNotFound, "not_found")
		})
	}
}

// TestUnknownResourceIdsAreNotFound covers the other half of the same rule. A stranger and
// a nonexistent id must be indistinguishable — if a real id owned by somebody else gave a
// different answer from a random one, the difference *is* the leak.
func TestUnknownResourceIdsAreNotFound(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	u := e.newUser()
	u.createVehicle()

	missing := &ownedResources{
		vehicleID:    uuid.NewString(),
		readingID:    uuid.NewString(),
		planID:       uuid.NewString(),
		recordID:     uuid.NewString(),
		obligationID: uuid.NewString(),
		seguroID:     uuid.NewString(),
		itemID:       u.firstItemID(),
	}

	for _, route := range protectedRoutes() {
		if route.isolation != hiddenFromStrangers {
			continue
		}
		t.Run(route.method+" "+route.pattern, func(t *testing.T) {
			u.do(route.method, route.path(missing), bodyFor(route, missing)).
				expectError(http.StatusNotFound, "not_found")
		})
	}
}

// TestMalformedResourceIdsAreRejected makes sure a non-UUID in the path is a clean
// refusal rather than a 500 from a failed parse deep in a repository.
func TestMalformedResourceIdsAreRejected(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	u := e.newUser()

	for _, path := range []string{
		"/v1/vehicles/not-a-uuid",
		"/v1/vehicles/not-a-uuid/odometer",
		"/v1/vehicles/not-a-uuid/maintenance-plans",
		"/v1/vehicles/not-a-uuid/maintenance-records",
		"/v1/vehicles/not-a-uuid/obligations",
		"/v1/vehicles/not-a-uuid/seguros",
		"/v1/vehicles/not-a-uuid/dashboard",
		"/v1/vehicles/not-a-uuid/alerts",
		"/v1/vehicles/not-a-uuid/timeline",
		"/v1/maintenance-plans/not-a-uuid",
		"/v1/maintenance-records/not-a-uuid",
		"/v1/obligations/not-a-uuid",
		"/v1/seguros/not-a-uuid",
	} {
		t.Run(path, func(t *testing.T) {
			res := u.get(path)
			if res.Status != http.StatusNotFound && res.Status != http.StatusUnprocessableEntity {
				t.Fatalf("GET %s: status = %d, want 404 or 422\nbody: %s",
					path, res.Status, res.Body)
			}
			// Whatever it decides, it must not be a server error dressed up as one.
			if code := res.errorCode(); code == "internal" {
				t.Fatalf("GET %s: a malformed id produced an internal error\nbody: %s",
					path, res.Body)
			}
		})
	}
}

// TestEveryProtectedRouteIsInTheMatrix is the guard that keeps the two tests above honest.
//
// It walks the router the application actually serves and requires every route to be
// either in the table or in the small list of deliberately public ones. A new endpoint
// therefore cannot be merged without someone stating how it is authorised — which is the
// review conversation worth forcing.
func TestEveryProtectedRouteIsInTheMatrix(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	// Public by design. Keep this list short and read every addition to it twice.
	public := map[string]bool{
		"GET /healthz":                         true,
		"GET /readyz":                          true,
		"POST /v1/auth/register":               true,
		"POST /v1/auth/login":                  true,
		"POST /v1/auth/refresh":                true,
		"POST /v1/auth/logout":                 true,
		"POST /v1/auth/password-reset/request": true,
		"POST /v1/auth/password-reset/confirm": true,
	}

	covered := map[string]bool{}
	for _, route := range protectedRoutes() {
		covered[route.method+" "+route.pattern] = true
	}

	routes, ok := e.handler.(chi.Routes)
	if !ok {
		t.Fatalf("the router is %T, which cannot be walked", e.handler)
	}

	var uncovered, unknownPublic []string
	err := chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		key := method + " " + normalizePattern(route)
		switch {
		case covered[key]:
			delete(covered, key)
		case public[key]:
			delete(public, key)
		default:
			uncovered = append(uncovered, key)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk routes: %v", err)
	}

	if len(uncovered) > 0 {
		sort.Strings(uncovered)
		t.Errorf("these routes are served but have no authorisation test:\n  %s\n\n"+
			"Add each one to protectedRoutes() in this file, or — if it is genuinely "+
			"public — to the `public` list above.", strings.Join(uncovered, "\n  "))
	}

	// The reverse direction: an entry left behind after a route was renamed or removed is
	// a test that silently stopped testing anything.
	for key := range covered {
		unknownPublic = append(unknownPublic, key)
	}
	for key := range public {
		unknownPublic = append(unknownPublic, key)
	}
	if len(unknownPublic) > 0 {
		sort.Strings(unknownPublic)
		t.Errorf("these routes are listed in this file but no longer served:\n  %s",
			strings.Join(unknownPublic, "\n  "))
	}
}

// normalizePattern strips the trailing wildcard chi appends to a mounted subrouter's
// pattern, so "/v1/me/*" and "/v1/me" compare equal.
func normalizePattern(route string) string {
	route = strings.TrimSuffix(route, "/*")
	if route == "" {
		return "/"
	}
	return route
}

func bodyFor(route protectedRoute, f *ownedResources) any {
	if route.body == nil {
		return nil
	}
	return route.body(f)
}

// ---------- the caller-scoped routes, tested one by one ----------

// TestCallerScopedRoutesSeeOnlyTheirOwnData covers what the matrix deliberately does not:
// routes where there is no other user's id in the URL, and isolation is instead a question
// of what the response contains.
func TestCallerScopedRoutesSeeOnlyTheirOwnData(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	alice := e.newUser()
	aliceVehicle := alice.createVehicle(map[string]any{"nickname": "Carro da Alice"})

	bob := e.newUser()
	bob.createVehicle(map[string]any{"nickname": "Carro do Bob"})

	t.Run("GET /v1/me returns the caller", func(t *testing.T) {
		body := bob.get("/v1/me").expect(http.StatusOK).json()
		if body["email"] != bob.Email {
			t.Fatalf("e-mail = %v, want %q", body["email"], bob.Email)
		}
	})

	t.Run("GET /v1/vehicles lists only the caller's vehicles", func(t *testing.T) {
		var page struct {
			Data []struct {
				ID       string  `json:"id"`
				Nickname *string `json:"nickname"`
			} `json:"data"`
		}
		bob.get("/v1/vehicles").expect(http.StatusOK).decode(&page)

		if len(page.Data) != 1 {
			t.Fatalf("Bob sees %d vehicles, want 1", len(page.Data))
		}
		if page.Data[0].ID == aliceVehicle {
			t.Fatal("Bob's vehicle list contains Alice's vehicle")
		}
	})
}

// TestCustomCatalogueItemsAreNotSharedBetweenAccounts guards the one place where a global
// table and a per-user table are the same table. The seeded catalogue is everyone's; an
// item somebody added is theirs.
func TestCustomCatalogueItemsAreNotSharedBetweenAccounts(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	alice := e.newUser()
	bob := e.newUser()

	custom := alice.post("/v1/maintenance-items", map[string]any{
		"name":                "Revisão do meu mecânico",
		"default_interval_km": 12_000,
	}).expect(http.StatusCreated)
	customID := custom.id()

	var bobsCatalogue struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	bob.get("/v1/maintenance-items?vehicle_type=car").expect(http.StatusOK).decode(&bobsCatalogue)

	for _, item := range bobsCatalogue.Data {
		if item.ID == customID {
			t.Fatal("Bob's catalogue contains an item Alice created for herself")
		}
	}
	if len(bobsCatalogue.Data) == 0 {
		t.Fatal("Bob cannot see the shared catalogue at all")
	}

	// And he cannot reach it by id either, by building a plan on top of it.
	bobVehicle := bob.createVehicle()
	bob.post(fmt.Sprintf("/v1/vehicles/%s/maintenance-plans", bobVehicle), map[string]any{
		"maintenance_item_id": customID,
	}).expectError(http.StatusNotFound, "not_found")
}
