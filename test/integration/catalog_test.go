package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
)

// The vehicle catalogue is the only part of this API that depends on somebody else's
// server, and that is what these tests are about. Not "does the JSON come back" — whether
// the mirror actually spares the provider, whether a provider having a bad day can stop
// somebody registering their car, and whether two users arriving at once can write the
// same brand twice.
//
// No test here reaches the real provider. newEnv points the catalogue at a port that
// refuses connections, and a test that wants it to work passes withFipeServer.

// ---------- the stand-in provider ----------

// fakeFipe is the provider, with a counter.
//
// The counter is the point. Most of the assertions below are not about the response body
// at all — they are about how many times the provider was asked, which is the whole reason
// this module exists.
type fakeFipe struct {
	*httptest.Server

	requests atomic.Int64

	mu sync.Mutex
	// paths records every request in order, so a test can assert which endpoint was hit
	// and not merely how many were.
	paths []string

	// failWith, when set, makes every response that status. It stands in for the provider
	// being down, rate limited or broken.
	failWith atomic.Int64
}

// newFakeFipe serves the real v2 shapes: brands and models as {code,name}, years as
// {code,name} where the code is "year-fuel", and the detail as the full vehicle object.
//
// The payloads are copied from the live API rather than invented, so a test passing here
// means the parsing works on what the provider actually sends.
func newFakeFipe(t *testing.T) *fakeFipe {
	t.Helper()

	fake := &fakeFipe{}
	fake.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.requests.Add(1)
		fake.mu.Lock()
		fake.paths = append(fake.paths, r.URL.Path)
		fake.mu.Unlock()

		if status := fake.failWith.Load(); status != 0 {
			w.WriteHeader(int(status))
			_, _ = io.WriteString(w, `{"error":"indisponivel"}`)
			return
		}

		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/cars/brands":
			_, _ = io.WriteString(w, `[{"code":"56","name":"Toyota"},{"code":"59","name":"VW - VolksWagen"}]`)

		case "/cars/brands/56/models":
			_, _ = io.WriteString(w, `[{"code":"10044","name":"PRIUS 1.8 16V 5p Aut. (Híbrido)"},`+
				`{"code":"2204","name":"Avalon XLS 3.0"}]`)

		case "/cars/brands/56/models/10044/years":
			// A real year, and the provider's zero-kilometre pseudo-year — which must land
			// as a NULL year rather than the year 32000.
			_, _ = io.WriteString(w, `[{"code":"2017-6","name":"2017 Híbrido"},`+
				`{"code":"2016-6","name":"2016 Híbrido"},`+
				`{"code":"32000-6","name":"32000 Híbrido"}]`)

		case "/cars/brands/56/models/10044/years/2017-6":
			_, _ = io.WriteString(w, `{"vehicleType":1,"price":"R$ 106.900,00","brand":"Toyota",`+
				`"model":"PRIUS 1.8 16V 5p Aut. (Híbrido)","modelYear":2017,"fuel":"Híbrido",`+
				`"codeFipe":"002068-2","referenceMonth":"agosto de 2026","fuelAcronym":"H"}`)

		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":"recurso não encontrado para a referência informada"}`)
		}
	}))
	t.Cleanup(fake.Close)

	return fake
}

func (f *fakeFipe) count() int64 { return f.requests.Load() }

func (f *fakeFipe) lastPath() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.paths) == 0 {
		return ""
	}
	return f.paths[len(f.paths)-1]
}

// ---------- helpers ----------

type catalogEntry struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Year      *int32  `json:"year"`
	FuelLabel *string `json:"fuel_label"`
	FuelType  *string `json:"fuel_type"`
}

func (u *user) catalogList(path string) []catalogEntry {
	u.t.Helper()

	var body struct {
		Data []catalogEntry `json:"data"`
	}
	u.get(path).expect(http.StatusOK).decode(&body)
	return body.Data
}

// drillDown walks the whole picker — brand, model, year — and returns the ids at each
// level. It is the exact sequence the registration screen performs.
func (u *user) drillDown() (brandID, modelID, yearID string) {
	u.t.Helper()

	brands := u.catalogList("/v1/vehicle-brands")
	brandID = findByName(u.t, brands, "Toyota")

	models := u.catalogList("/v1/vehicle-brands/" + brandID + "/models")
	modelID = findByName(u.t, models, "PRIUS 1.8 16V 5p Aut. (Híbrido)")

	years := u.catalogList("/v1/vehicle-models/" + modelID + "/years")
	yearID = findByName(u.t, years, "2017 Híbrido")

	return brandID, modelID, yearID
}

func findByName(t *testing.T, entries []catalogEntry, name string) string {
	t.Helper()

	for _, entry := range entries {
		if entry.Name == name {
			return entry.ID
		}
	}
	t.Fatalf("no catalogue entry named %q in %+v", name, entries)
	return ""
}

// ---------- cache miss, then cache hit ----------

// TestCatalogFetchesOnceThenServesFromPostgres is the whole premise of the module.
//
// The first request for each level costs one call to the provider. Every request after it,
// forever and for every user, costs none.
func TestCatalogFetchesOnceThenServesFromPostgres(t *testing.T) {
	t.Parallel()

	fake := newFakeFipe(t)
	e := newEnv(t, withFipeServer(fake.URL))
	u := e.newUser()

	// ---------- brands ----------

	brands := u.catalogList("/v1/vehicle-brands")
	if len(brands) != 2 {
		t.Fatalf("brands = %+v, want the two the provider serves", brands)
	}
	if fake.count() != 1 {
		t.Fatalf("brand list cost %d provider requests, want 1", fake.count())
	}

	u.catalogList("/v1/vehicle-brands")
	u.catalogList("/v1/vehicle-brands")
	if fake.count() != 1 {
		t.Fatalf("repeat brand lists cost %d provider requests, want the first one only",
			fake.count())
	}

	// A DIFFERENT user, which is the case that matters: the mirror is shared, not
	// per-account. If this fetched again, the cache would be worthless at any real scale.
	other := e.newUser()
	other.catalogList("/v1/vehicle-brands")
	if fake.count() != 1 {
		t.Fatalf("a second user's brand list cost %d provider requests, want 0",
			fake.count()-1)
	}

	brandID := findByName(t, brands, "Toyota")

	// ---------- models ----------

	models := u.catalogList("/v1/vehicle-brands/" + brandID + "/models")
	if len(models) != 2 {
		t.Fatalf("models = %+v, want two", models)
	}
	if fake.count() != 2 {
		t.Fatalf("provider requests = %d after the model list, want 2", fake.count())
	}

	other.catalogList("/v1/vehicle-brands/" + brandID + "/models")
	if fake.count() != 2 {
		t.Fatalf("a cached model list cost another provider request (%d)", fake.count())
	}

	modelID := findByName(t, models, "PRIUS 1.8 16V 5p Aut. (Híbrido)")

	// ---------- years ----------

	years := u.catalogList("/v1/vehicle-models/" + modelID + "/years")
	if len(years) != 3 {
		t.Fatalf("years = %+v, want three", years)
	}
	if fake.count() != 3 {
		t.Fatalf("provider requests = %d after the year list, want 3", fake.count())
	}

	other.catalogList("/v1/vehicle-models/" + modelID + "/years")
	if fake.count() != 3 {
		t.Fatalf("a cached year list cost another provider request (%d)", fake.count())
	}

	// ---------- what the year rows actually hold ----------

	byName := map[string]catalogEntry{}
	for _, year := range years {
		byName[year.Name] = year
	}

	real2017 := byName["2017 Híbrido"]
	if real2017.Year == nil || *real2017.Year != 2017 {
		t.Errorf("2017 entry has year %v, want 2017", real2017.Year)
	}
	if real2017.FuelLabel == nil || *real2017.FuelLabel != "Híbrido" {
		t.Errorf("fuel_label = %v, want the provider's word", real2017.FuelLabel)
	}
	// The translated value is the one the app sends straight back to POST /v1/vehicles.
	// If this were "Híbrido" the vehicle's CHECK constraint would reject it.
	if real2017.FuelType == nil || *real2017.FuelType != "hibrido" {
		t.Errorf("fuel_type = %v, want \"hibrido\"", real2017.FuelType)
	}

	zeroKm := byName["32000 Híbrido"]
	if zeroKm.ID == "" {
		t.Fatal("the zero-kilometre entry was dropped; it is a real option for a new car")
	}
	if zeroKm.Year != nil {
		t.Errorf("the zero-kilometre entry has year %d — 32000 is a price bucket, not a year",
			*zeroKm.Year)
	}

	// The ordering the picker depends on: newest first, with the new-vehicle entry above
	// everything.
	if years[0].Name != "32000 Híbrido" || years[1].Name != "2017 Híbrido" {
		t.Errorf("years came back as %q, %q — want the zero-km entry first, then newest down",
			years[0].Name, years[1].Name)
	}
}

// ---------- the detail and its price ----------

func TestCatalogDetailStoresAndReusesThePrice(t *testing.T) {
	t.Parallel()

	fake := newFakeFipe(t)
	e := newEnv(t, withFipeServer(fake.URL))
	u := e.newUser()

	_, _, yearID := u.drillDown()
	afterDrillDown := fake.count()

	var detail struct {
		ID       string  `json:"id"`
		Name     string  `json:"name"`
		Year     *int32  `json:"year"`
		FuelType *string `json:"fuel_type"`
		FipeCode *string `json:"fipe_code"`
		Brand    struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			VehicleType string `json:"vehicle_type"`
		} `json:"brand"`
		Model struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"model"`
		FipePrice *struct {
			PriceCents     int64  `json:"price_cents"`
			ReferenceMonth string `json:"reference_month"`
		} `json:"fipe_price"`
	}
	u.get("/v1/vehicle-model-years/" + yearID).expect(http.StatusOK).decode(&detail)

	if fake.count() != afterDrillDown+1 {
		t.Fatalf("the detail cost %d provider requests, want 1", fake.count()-afterDrillDown)
	}

	if detail.FipeCode == nil || *detail.FipeCode != "002068-2" {
		t.Errorf("fipe_code = %v, want 002068-2", detail.FipeCode)
	}
	if detail.Brand.Name != "Toyota" || detail.Model.Name != "PRIUS 1.8 16V 5p Aut. (Híbrido)" {
		t.Errorf("detail carries brand %q / model %q", detail.Brand.Name, detail.Model.Name)
	}
	if detail.Brand.VehicleType != "car" {
		t.Errorf("vehicle_type = %q, want car", detail.Brand.VehicleType)
	}

	if detail.FipePrice == nil {
		t.Fatal("fipe_price is null on a successful fetch")
	}
	// "R$ 106.900,00" as centavos. A float here would be off by a rounding error and
	// nobody would notice until it was somebody's car.
	if detail.FipePrice.PriceCents != 10_690_000 {
		t.Errorf("price_cents = %d, want 10690000", detail.FipePrice.PriceCents)
	}
	// "agosto de 2026" as the first day of that month.
	if detail.FipePrice.ReferenceMonth != "2026-08-01" {
		t.Errorf("reference_month = %q, want 2026-08-01", detail.FipePrice.ReferenceMonth)
	}

	// A second read inside the TTL is served from Postgres.
	before := fake.count()
	u.get("/v1/vehicle-model-years/" + yearID).expect(http.StatusOK)
	if fake.count() != before {
		t.Errorf("a fresh stored price still cost %d provider requests", fake.count()-before)
	}
}

// ---------- the provider failing ----------

// TestCatalogReportsUpstreamFailureWithoutLeakingIt covers the case where we have nothing
// to serve. The client must learn that the failure is not theirs, and must not receive a
// stack of somebody else's HTTP status.
func TestCatalogReportsUpstreamFailureWithoutLeakingIt(t *testing.T) {
	t.Parallel()

	fake := newFakeFipe(t)
	e := newEnv(t, withFipeServer(fake.URL))
	u := e.newUser()

	for _, status := range []int{
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		// The daily quota. It is OURS and shared, so it must not come back as
		// rate_limited — that code tells the app this user is going too fast, which would
		// be a lie it then repeats to them.
		http.StatusTooManyRequests,
	} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			fake.failWith.Store(int64(status))
			defer fake.failWith.Store(0)

			res := u.get("/v1/vehicle-brands").
				expectError(http.StatusServiceUnavailable, "upstream_unavailable")

			// The message is ours, in pt-BR, and displayable. Nothing about the provider,
			// its status code or its name reaches the client.
			body := res.json()
			payload, _ := body["error"].(map[string]any)
			message, _ := payload["message"].(string)

			if message == "" {
				t.Fatalf("no message in the error envelope: %s", res.Body)
			}

			// Everything we say is scanned except details.request_id — see
			// scannableError for why that one field is dropped rather than the check
			// loosened.
			scanned := scannableError(t, res.Body)
			for _, leak := range []string{"fipe", "parallelum", "502", "503", "429", "http"} {
				if strings.Contains(scanned, leak) {
					t.Errorf("the response leaks %q to the client: %s", leak, res.Body)
				}
			}
		})
	}
}

// scannableError renders an error envelope as lowercase text for the leak check above,
// with details.request_id removed.
//
// The request id says nothing about anybody upstream — support uses it to find the log
// line — and it is the one field here that is not ours to police. Two ways it breaks the
// check: a generated UUIDv7 is hex, and hex spells 502, 503 or 429 often enough to trip
// roughly 0.9% of ids (the observed failure was 01a04393-4a21-7257-b35c-503dbe292d06);
// and the id can instead be echoed from the client's own X-Request-Id header, so the app
// could fail this test by choosing an unlucky one. Either way the raw body failed at
// random on the id rather than on a leak.
//
// Dropping the one opaque field, rather than narrowing the check to message and code,
// keeps the rest of the envelope under it: the code, the message, and any detail added
// later all still have to come back clean.
func scannableError(t *testing.T, raw []byte) string {
	t.Helper()

	var body struct {
		Error struct {
			Code    string         `json:"code"`
			Message string         `json:"message"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("response is not the error envelope: %v\nbody: %s", err, raw)
	}
	delete(body.Error.Details, "request_id")

	rendered, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("re-encode the error envelope: %v", err)
	}
	return strings.ToLower(string(rendered))
}

// TestCatalogServesFromPostgresWhileTheProviderIsDown is the payoff of mirroring.
//
// Once a branch is stored, the provider can be gone entirely and the registration flow
// still works — because nothing calls it.
func TestCatalogServesFromPostgresWhileTheProviderIsDown(t *testing.T) {
	t.Parallel()

	fake := newFakeFipe(t)
	e := newEnv(t, withFipeServer(fake.URL))
	u := e.newUser()

	brandID, modelID, _ := u.drillDown()

	// From here the provider answers nothing at all.
	fake.failWith.Store(http.StatusInternalServerError)
	before := fake.count()

	if got := len(u.catalogList("/v1/vehicle-brands")); got != 2 {
		t.Errorf("brands = %d entries while the provider is down, want 2", got)
	}
	if got := len(u.catalogList("/v1/vehicle-brands/" + brandID + "/models")); got != 2 {
		t.Errorf("models = %d entries while the provider is down, want 2", got)
	}
	if got := len(u.catalogList("/v1/vehicle-models/" + modelID + "/years")); got != 3 {
		t.Errorf("years = %d entries while the provider is down, want 3", got)
	}

	if fake.count() != before {
		t.Errorf("the provider was called %d times for data we already had",
			fake.count()-before)
	}
}

// TestCatalogDetailDegradesWithoutAPrice is the decision that keeps a supplier's outage
// from becoming a blocked registration.
//
// The detail is the last screen before POST /v1/vehicles. Everything on it except the
// valuation comes out of our own database, so a provider failure returns 200 with
// fipe_price null — not a 503 that stops somebody adding their car over a decoration.
func TestCatalogDetailDegradesWithoutAPrice(t *testing.T) {
	t.Parallel()

	fake := newFakeFipe(t)
	e := newEnv(t, withFipeServer(fake.URL))
	u := e.newUser()

	_, _, yearID := u.drillDown()

	fake.failWith.Store(http.StatusInternalServerError)

	var detail struct {
		ID       string  `json:"id"`
		Name     string  `json:"name"`
		FipeCode *string `json:"fipe_code"`
		Brand    struct {
			Name string `json:"name"`
		} `json:"brand"`
		FipePrice *struct {
			PriceCents int64 `json:"price_cents"`
		} `json:"fipe_price"`
	}
	u.get("/v1/vehicle-model-years/" + yearID).expect(http.StatusOK).decode(&detail)

	if detail.FipePrice != nil {
		t.Errorf("fipe_price = %+v, want null when the provider cannot be reached",
			detail.FipePrice)
	}
	// The part that matters for registration is all there.
	if detail.ID != yearID || detail.Name != "2017 Híbrido" || detail.Brand.Name != "Toyota" {
		t.Errorf("the catalogue half of the detail is incomplete: %+v", detail)
	}
}

// TestCatalogDetailServesAStalePriceRatherThanNone is the middle rung of the same ladder:
// a number from last month beats no number, and collected_at is on the wire so the app can
// tell which it got.
func TestCatalogDetailServesAStalePriceRatherThanNone(t *testing.T) {
	t.Parallel()

	fake := newFakeFipe(t)
	e := newEnv(t, withFipeServer(fake.URL))
	u := e.newUser()

	_, _, yearID := u.drillDown()
	u.get("/v1/vehicle-model-years/" + yearID).expect(http.StatusOK)

	// Age the stored price past the TTL, then take the provider away. Reaching into the
	// database is acceptable here and only here: there is no API that makes a row older,
	// and the alternative is a test that sleeps for a week.
	e.ageStoredPrices(t, "8 days")
	fake.failWith.Store(http.StatusServiceUnavailable)

	var detail struct {
		FipePrice *struct {
			PriceCents     int64  `json:"price_cents"`
			ReferenceMonth string `json:"reference_month"`
			CollectedAt    string `json:"collected_at"`
		} `json:"fipe_price"`
	}
	u.get("/v1/vehicle-model-years/" + yearID).expect(http.StatusOK).decode(&detail)

	if detail.FipePrice == nil {
		t.Fatal("fipe_price is null although a stale one was stored")
	}
	if detail.FipePrice.PriceCents != 10_690_000 {
		t.Errorf("price_cents = %d, want the stored value", detail.FipePrice.PriceCents)
	}
	if detail.FipePrice.CollectedAt == "" {
		t.Error("collected_at is empty — the app cannot tell a stale price from a fresh one")
	}
}

// TestStalePriceIsRefetchedWhenTheProviderIsBack is the other half: the TTL genuinely
// expires rather than pinning the first value forever.
func TestStalePriceIsRefetchedWhenTheProviderIsBack(t *testing.T) {
	t.Parallel()

	fake := newFakeFipe(t)
	e := newEnv(t, withFipeServer(fake.URL))
	u := e.newUser()

	_, _, yearID := u.drillDown()
	u.get("/v1/vehicle-model-years/" + yearID).expect(http.StatusOK)

	before := fake.count()
	e.ageStoredPrices(t, "8 days")

	u.get("/v1/vehicle-model-years/" + yearID).expect(http.StatusOK)
	if fake.count() != before+1 {
		t.Fatalf("a stale price cost %d provider requests, want exactly 1",
			fake.count()-before)
	}
	if !strings.HasSuffix(fake.lastPath(), "/years/2017-6") {
		t.Errorf("the refresh hit %q, want the vehicle detail endpoint", fake.lastPath())
	}

	// And the refreshed row is fresh again: a third read costs nothing.
	before = fake.count()
	u.get("/v1/vehicle-model-years/" + yearID).expect(http.StatusOK)
	if fake.count() != before {
		t.Errorf("the refreshed price was not treated as fresh (%d more requests)",
			fake.count()-before)
	}
}

// ---------- concurrency ----------

// TestConcurrentSyncsDoNotDuplicate is the race described in the brief: two users tap the
// same brand at the same instant, both find nothing, and both go to the provider.
//
// That is allowed — the duplicate call is a wasted request, not a bug. What is not allowed
// is two sets of rows. The UNIQUE constraint plus ON CONFLICT is what prevents it, with no
// lock of any kind, distributed or otherwise.
func TestConcurrentSyncsDoNotDuplicate(t *testing.T) {
	t.Parallel()

	fake := newFakeFipe(t)
	e := newEnv(t, withFipeServer(fake.URL))

	// Separate users, because that is the real shape of the race: different sessions
	// arriving together, not one client firing twice.
	const callers = 8
	users := make([]*user, callers)
	for i := range users {
		users[i] = e.newUser()
	}

	// ---------- brands, from a cold catalogue ----------

	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, u := range users {
		wg.Add(1)
		go func(u *user) {
			defer wg.Done()
			<-start
			u.get("/v1/vehicle-brands").expect(http.StatusOK)
		}(u)
	}
	close(start)
	wg.Wait()

	// Two brands in the payload, two rows in the table — no matter how many callers ran.
	if got := e.countRows(t, "SELECT count(*) FROM vehicle_brands"); got != 2 {
		t.Fatalf("vehicle_brands holds %d rows after %d concurrent syncs, want 2",
			got, callers)
	}
	if got := e.countRows(t, "SELECT count(*) FROM vehicle_catalog_syncs"); got != 1 {
		t.Errorf("vehicle_catalog_syncs holds %d rows, want 1", got)
	}

	brandID := findByName(t, users[0].catalogList("/v1/vehicle-brands"), "Toyota")

	// ---------- models, same race one level down ----------

	start = make(chan struct{})
	for _, u := range users {
		wg.Add(1)
		go func(u *user) {
			defer wg.Done()
			<-start
			u.get("/v1/vehicle-brands/" + brandID + "/models").expect(http.StatusOK)
		}(u)
	}
	close(start)
	wg.Wait()

	if got := e.countRows(t, "SELECT count(*) FROM vehicle_models"); got != 2 {
		t.Fatalf("vehicle_models holds %d rows after %d concurrent syncs, want 2",
			got, callers)
	}

	// Every caller must also have been served the same thing. A race that returns a
	// half-written list to one of them is as broken as one that duplicates.
	for i, u := range users {
		if got := len(u.catalogList("/v1/vehicle-brands/" + brandID + "/models")); got != 2 {
			t.Errorf("caller %d sees %d models, want 2", i, got)
		}
	}
}

// TestResyncUpdatesRatherThanDuplicates covers the upsert path directly: the same brand
// list arriving twice must refresh the rows, keep their ids, and not touch the record that
// their models are already known.
func TestResyncUpdatesRatherThanDuplicates(t *testing.T) {
	t.Parallel()

	fake := newFakeFipe(t)
	e := newEnv(t, withFipeServer(fake.URL))
	u := e.newUser()

	brandID, modelID, _ := u.drillDown()

	// Force the brand list to be fetched again by clearing only the sync marker. The rows
	// stay, so the second sync is a pure upsert over existing keys.
	e.exec(t, "DELETE FROM vehicle_catalog_syncs")

	before := fake.count()
	u.catalogList("/v1/vehicle-brands")
	if fake.count() != before+1 {
		t.Fatalf("clearing the marker cost %d provider requests, want 1", fake.count()-before)
	}

	if got := e.countRows(t, "SELECT count(*) FROM vehicle_brands"); got != 2 {
		t.Errorf("vehicle_brands holds %d rows after a re-sync, want 2", got)
	}

	// The ids survived, which is what makes a vehicle's foreign key safe across a re-sync.
	if got := findByName(t, u.catalogList("/v1/vehicle-brands"), "Toyota"); got != brandID {
		t.Errorf("the brand id changed on re-sync: %s → %s", brandID, got)
	}

	// And the models were not forgotten: re-syncing the brand list must not reset
	// models_synced_at, or every brand list refresh would invalidate the whole tree.
	before = fake.count()
	u.catalogList("/v1/vehicle-brands/" + brandID + "/models")
	if fake.count() != before {
		t.Errorf("a brand re-sync discarded the stored model list (%d extra requests)",
			fake.count()-before)
	}
	_ = modelID
}

// TestEmptyProviderResponseIsNotCached is the one failure this design could not recover
// from on its own.
//
// A provider glitch that answers 200 with `[]` must not leave a branch permanently blank.
// Nothing is persisted and nothing is marked synced, so the next request asks again — and
// when the provider comes back, so does the catalogue, with no intervention.
func TestEmptyProviderResponseIsNotCached(t *testing.T) {
	t.Parallel()

	var empty atomic.Bool
	empty.Store(true)
	var requests atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if empty.Load() {
			_, _ = io.WriteString(w, `[]`)
			return
		}
		_, _ = io.WriteString(w, `[{"code":"56","name":"Toyota"}]`)
	}))
	defer server.Close()

	e := newEnv(t, withFipeServer(server.URL))
	u := e.newUser()

	// Two requests during the glitch: both come back empty, and both went out — proof the
	// first one did not get cached.
	if got := len(u.catalogList("/v1/vehicle-brands")); got != 0 {
		t.Fatalf("brands = %d entries, want 0", got)
	}
	if got := len(u.catalogList("/v1/vehicle-brands")); got != 0 {
		t.Fatalf("brands = %d entries on the retry, want 0", got)
	}
	if requests.Load() != 2 {
		t.Fatalf("provider requests = %d, want 2 — an empty response was cached",
			requests.Load())
	}
	if got := e.countRows(t, "SELECT count(*) FROM vehicle_catalog_syncs"); got != 0 {
		t.Errorf("an empty response marked the collection synced (%d rows)", got)
	}

	// The provider recovers, with nothing to clean up first.
	empty.Store(false)
	if got := len(u.catalogList("/v1/vehicle-brands")); got != 1 {
		t.Fatalf("brands = %d entries after recovery, want 1", got)
	}
	if got := e.countRows(t, "SELECT count(*) FROM vehicle_brands"); got != 1 {
		t.Errorf("vehicle_brands holds %d rows after recovery, want 1", got)
	}
}

// ---------- not found ----------

func TestCatalogUnknownIdsAreNotFound(t *testing.T) {
	t.Parallel()

	fake := newFakeFipe(t)
	e := newEnv(t, withFipeServer(fake.URL))
	u := e.newUser()

	missing := "00000000-0000-0000-0000-000000000000"
	before := fake.count()

	u.get("/v1/vehicle-brands/"+missing+"/models").
		expectError(http.StatusNotFound, "not_found")
	u.get("/v1/vehicle-models/"+missing+"/years").
		expectError(http.StatusNotFound, "not_found")
	u.get("/v1/vehicle-model-years/"+missing).
		expectError(http.StatusNotFound, "not_found")

	// The security property: an id from a request body never becomes part of an outbound
	// URL. Every one of those was rejected against our own tables, before any thought of
	// calling anybody.
	if fake.count() != before {
		t.Errorf("an unknown id caused %d provider requests — a caller can aim our "+
			"credentials at a URL of their choosing", fake.count()-before)
	}

	u.get("/v1/vehicle-brands/not-a-uuid/models").
		expectError(http.StatusNotFound, "not_found")
}

// TestCatalogRejectsUnsupportedVehicleTypes keeps the catalogue inside product scope.
//
// The schema, the provider client and the type map all handle motorcycles; POST
// /v1/vehicles does not. Offering a motorcycle picker that dead-ends at the last step
// would be worse than not offering one.
func TestCatalogRejectsUnsupportedVehicleTypes(t *testing.T) {
	t.Parallel()

	fake := newFakeFipe(t)
	e := newEnv(t, withFipeServer(fake.URL))
	u := e.newUser()

	before := fake.count()
	for _, vehicleType := range []string{"motorcycle", "truck", "cars", "aviao"} {
		u.get("/v1/vehicle-brands?vehicle_type="+vehicleType).
			expectError(http.StatusUnprocessableEntity, "validation_failed")
	}
	if fake.count() != before {
		t.Errorf("a rejected vehicle type still reached the provider (%d requests)",
			fake.count()-before)
	}

	// Absent means car, so the app does not have to send it.
	u.get("/v1/vehicle-brands").expect(http.StatusOK)
	u.get("/v1/vehicle-brands?vehicle_type=car").expect(http.StatusOK)
}

// ---------- registering a vehicle from the catalogue ----------

// TestRegisterVehicleFromCatalogue is the flow the whole feature exists for, end to end.
func TestRegisterVehicleFromCatalogue(t *testing.T) {
	t.Parallel()

	fake := newFakeFipe(t)
	e := newEnv(t, withFipeServer(fake.URL))
	u := e.newUser()

	brandID, modelID, yearID := u.drillDown()

	var detail struct {
		Name     string  `json:"name"`
		Year     *int32  `json:"year"`
		FuelType *string `json:"fuel_type"`
		FipeCode *string `json:"fipe_code"`
		Brand    struct {
			Name string `json:"name"`
		} `json:"brand"`
		Model struct {
			Name string `json:"name"`
		} `json:"model"`
	}
	u.get("/v1/vehicle-model-years/" + yearID).expect(http.StatusOK).decode(&detail)

	// The app sends back exactly what it was shown. Every text field is a snapshot of what
	// the owner confirmed; the id is the link.
	created := u.post("/v1/vehicles", map[string]any{
		"brand":                 detail.Brand.Name,
		"model":                 detail.Model.Name,
		"model_year":            *detail.Year,
		"fuel_type":             *detail.FuelType,
		"fipe_code":             *detail.FipeCode,
		"catalog_model_year_id": yearID,
		"plate":                 "ABC1D23",
		"current_mileage_km":    98_450,
	}).expect(http.StatusCreated)

	var vehicle struct {
		ID                 string  `json:"id"`
		Brand              string  `json:"brand"`
		Model              string  `json:"model"`
		ModelYear          *int32  `json:"model_year"`
		FuelType           *string `json:"fuel_type"`
		FipeCode           *string `json:"fipe_code"`
		CatalogBrandID     *string `json:"catalog_brand_id"`
		CatalogModelID     *string `json:"catalog_model_id"`
		CatalogModelYearID *string `json:"catalog_model_year_id"`
	}
	created.decode(&vehicle)

	// The three ids are stored, and the brand and model were DERIVED from the leaf — the
	// client never sent them, which is what makes an inconsistent triple impossible.
	if vehicle.CatalogModelYearID == nil || *vehicle.CatalogModelYearID != yearID {
		t.Errorf("catalog_model_year_id = %v, want %s", vehicle.CatalogModelYearID, yearID)
	}
	if vehicle.CatalogBrandID == nil || *vehicle.CatalogBrandID != brandID {
		t.Errorf("catalog_brand_id = %v, want %s (derived server-side)",
			vehicle.CatalogBrandID, brandID)
	}
	if vehicle.CatalogModelID == nil || *vehicle.CatalogModelID != modelID {
		t.Errorf("catalog_model_id = %v, want %s (derived server-side)",
			vehicle.CatalogModelID, modelID)
	}

	// The snapshot.
	if vehicle.Brand != "Toyota" || vehicle.Model != "PRIUS 1.8 16V 5p Aut. (Híbrido)" {
		t.Errorf("snapshot = %q / %q", vehicle.Brand, vehicle.Model)
	}
	if vehicle.FuelType == nil || *vehicle.FuelType != "hibrido" {
		t.Errorf("fuel_type = %v — the catalogue's translated value was rejected",
			vehicle.FuelType)
	}
	if vehicle.FipeCode == nil || *vehicle.FipeCode != "002068-2" {
		t.Errorf("fipe_code = %v, want 002068-2", vehicle.FipeCode)
	}

	// And it survives a re-read.
	var reread struct {
		CatalogModelYearID *string `json:"catalog_model_year_id"`
		FipeCode           *string `json:"fipe_code"`
	}
	u.get("/v1/vehicles/" + vehicle.ID).expect(http.StatusOK).decode(&reread)
	if reread.CatalogModelYearID == nil || *reread.CatalogModelYearID != yearID {
		t.Errorf("the link did not survive a re-read: %v", reread.CatalogModelYearID)
	}
	if reread.FipeCode == nil || *reread.FipeCode != "002068-2" {
		t.Errorf("the fipe_code did not survive a re-read: %v", reread.FipeCode)
	}
}

// TestVehicleSnapshotSurvivesACatalogueRename is the reason the vehicle keeps its own copy.
//
// The supplier renames its own descriptions. A service history that rewrites itself when
// that happens is worth less at resale than one that does not, and this history is the
// product's asset.
func TestVehicleSnapshotSurvivesACatalogueRename(t *testing.T) {
	t.Parallel()

	fake := newFakeFipe(t)
	e := newEnv(t, withFipeServer(fake.URL))
	u := e.newUser()

	brandID, modelID, yearID := u.drillDown()

	vehicleID := u.post("/v1/vehicles", map[string]any{
		"brand":                 "Toyota",
		"model":                 "PRIUS 1.8 16V 5p Aut. (Híbrido)",
		"model_year":            2017,
		"catalog_model_year_id": yearID,
	}).expect(http.StatusCreated).id()

	// The supplier tidies its descriptions, and a re-sync brings the new ones in.
	//
	// Scoped by id rather than renaming every row: the other brand's models were never
	// synced, so pointing at it by accident would send the assertion below to the provider
	// instead of to the catalogue.
	e.exec(t, "UPDATE vehicle_models SET name = 'Prius 1.8 Hybrid Automatic' WHERE id = $1",
		mustUUID(t, modelID))
	e.exec(t, "UPDATE vehicle_brands SET name = 'TOYOTA DO BRASIL' WHERE id = $1",
		mustUUID(t, brandID))

	var vehicle struct {
		Brand string `json:"brand"`
		Model string `json:"model"`
	}
	u.get("/v1/vehicles/" + vehicleID).expect(http.StatusOK).decode(&vehicle)

	if vehicle.Brand != "Toyota" || vehicle.Model != "PRIUS 1.8 16V 5p Aut. (Híbrido)" {
		t.Fatalf("the vehicle now reads %q / %q — the catalogue rewrote somebody's history",
			vehicle.Brand, vehicle.Model)
	}

	// The catalogue itself did change, which is what makes the assertion above meaningful
	// rather than a test of nothing. Read back through the same ids the vehicle points at.
	models := u.catalogList("/v1/vehicle-brands/" + brandID + "/models")
	if got := findByName(t, models, "Prius 1.8 Hybrid Automatic"); got != modelID {
		t.Fatalf("the catalogue did not actually change: model %s is not %s", got, modelID)
	}
}

// TestVehicleRejectsAnUnknownCatalogueSelection is the "do not trust ids from the app"
// rule. An id that resolves to nothing must never become a foreign key.
func TestVehicleRejectsAnUnknownCatalogueSelection(t *testing.T) {
	t.Parallel()

	fake := newFakeFipe(t)
	e := newEnv(t, withFipeServer(fake.URL))
	u := e.newUser()

	body := map[string]any{"brand": "Toyota", "model": "Corolla"}

	body["catalog_model_year_id"] = "00000000-0000-0000-0000-000000000000"
	u.post("/v1/vehicles", body).expectError(http.StatusNotFound, "not_found")

	body["catalog_model_year_id"] = "not-a-uuid"
	u.post("/v1/vehicles", body).
		expectError(http.StatusUnprocessableEntity, "validation_failed")

	// A brand id in the model-year field is still not a model year. The three levels are
	// separate tables, so pointing at the wrong one resolves to nothing.
	brandID := findByName(t, u.catalogList("/v1/vehicle-brands"), "Toyota")
	body["catalog_model_year_id"] = brandID
	u.post("/v1/vehicles", body).expectError(http.StatusNotFound, "not_found")

	if got := e.countRows(t, "SELECT count(*) FROM vehicles"); got != 0 {
		t.Errorf("%d vehicles were created from rejected selections", got)
	}
}

// TestVehicleWithoutCatalogueStillWorks is the compatibility guarantee. The app already
// installed does not know these fields exist, and it must keep working exactly as it did.
func TestVehicleWithoutCatalogueStillWorks(t *testing.T) {
	t.Parallel()

	e := newEnv(t) // no FIPE server at all: the catalogue is unreachable here.
	u := e.newUser()

	var vehicle struct {
		Brand              string  `json:"brand"`
		CatalogBrandID     *string `json:"catalog_brand_id"`
		CatalogModelID     *string `json:"catalog_model_id"`
		CatalogModelYearID *string `json:"catalog_model_year_id"`
		FipeCode           *string `json:"fipe_code"`
	}
	u.post("/v1/vehicles", map[string]any{
		"brand":              "Fiat",
		"model":              "Uno",
		"model_year":         2015,
		"current_mileage_km": 120_000,
	}).expect(http.StatusCreated).decode(&vehicle)

	if vehicle.Brand != "Fiat" {
		t.Errorf("brand = %q", vehicle.Brand)
	}
	// All three null, and null in the JSON rather than a zero uuid — which would look like
	// a real id to a client.
	if vehicle.CatalogBrandID != nil || vehicle.CatalogModelID != nil || vehicle.CatalogModelYearID != nil {
		t.Errorf("a hand-typed vehicle carries catalogue links: %+v", vehicle)
	}
	if vehicle.FipeCode != nil {
		t.Errorf("fipe_code = %v, want null", vehicle.FipeCode)
	}
}

// TestUpdateVehicleRelinksTheCatalogue covers "I picked the wrong version".
func TestUpdateVehicleRelinksTheCatalogue(t *testing.T) {
	t.Parallel()

	fake := newFakeFipe(t)
	e := newEnv(t, withFipeServer(fake.URL))
	u := e.newUser()

	_, _, yearID := u.drillDown()

	vehicleID := u.createVehicle()

	var updated struct {
		CatalogBrandID     *string `json:"catalog_brand_id"`
		CatalogModelYearID *string `json:"catalog_model_year_id"`
		FipeCode           *string `json:"fipe_code"`
		Brand              string  `json:"brand"`
	}
	u.patch("/v1/vehicles/"+vehicleID, map[string]any{
		"brand":                 "Toyota",
		"catalog_model_year_id": yearID,
		"fipe_code":             "002068-2",
	}).expect(http.StatusOK).decode(&updated)

	if updated.CatalogModelYearID == nil || *updated.CatalogModelYearID != yearID {
		t.Errorf("catalog_model_year_id = %v, want %s", updated.CatalogModelYearID, yearID)
	}
	if updated.CatalogBrandID == nil {
		t.Error("catalog_brand_id was not derived on update")
	}
	if updated.Brand != "Toyota" || updated.FipeCode == nil || *updated.FipeCode != "002068-2" {
		t.Errorf("the snapshot was not updated alongside the link: %+v", updated)
	}

	// A PATCH that does not mention the catalogue leaves the link alone.
	u.patch("/v1/vehicles/"+vehicleID, map[string]any{"nickname": "Priuszinho"}).
		expect(http.StatusOK).decode(&updated)
	if updated.CatalogModelYearID == nil || *updated.CatalogModelYearID != yearID {
		t.Errorf("an unrelated PATCH cleared the catalogue link: %v", updated.CatalogModelYearID)
	}
}

// ---------- database helpers ----------
//
// countRows already exists in odometer_test.go and is reused here. These two are what it
// does not cover: a plain exec, and ageing a row past the freshness window — which has no
// API, and whose only alternative is a test that sleeps for a week.

func (e *env) exec(t *testing.T, sql string, args ...any) {
	t.Helper()

	if _, err := e.db.Pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

// ageStoredPrices moves every stored price back in time.
func (e *env) ageStoredPrices(t *testing.T, interval string) {
	t.Helper()

	e.exec(t, "UPDATE vehicle_fipe_prices SET collected_at = now() - $1::interval", interval)
}

// mustUUID parses an id the API handed back, so it can be used as a typed parameter rather
// than interpolated into SQL.
func mustUUID(t *testing.T, raw string) uuid.UUID {
	t.Helper()

	parsed, err := uuid.Parse(raw)
	if err != nil {
		t.Fatalf("parse uuid %q: %v", raw, err)
	}
	return parsed
}
