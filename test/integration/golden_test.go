package integration

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The golden files in test/golden are the response contract, in the sense SPEC.md D-09
// asks for: "o teste falha se um campo sumir".
//
// What they record is the *shape* of a response — every key, and the JSON type of every
// leaf — not the values. A snapshot of values would have to be regenerated on every run,
// because every response here carries fresh ids and timestamps, and a golden file that is
// always stale is a golden file nobody reads. A shape is stable, and it still fails the
// moment a field is renamed, removed, changes type, or turns nullable.
//
// Regenerate with:
//
//	go test ./test/integration -run TestGolden -update
//
// Then read the diff. A change here is a change to what the Flutter app receives, and the
// app cannot be force-updated (SPEC.md D-01) — so the question a diff should provoke is
// not "does the test pass now" but "what happens to the version already installed".

var updateGolden = flag.Bool("update", false,
	"rewrite the files in test/golden from the current responses")

const goldenDir = "../golden"

func TestGoldenResponses(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	// ---------- identity ----------

	registered := e.anonymous().post("/v1/auth/register", map[string]any{
		"name":     "Fulana de Tal",
		"email":    "golden@example.test",
		"password": "senha-de-teste-123",
	}).expect(http.StatusCreated)
	assertGolden(t, "auth_register", registered)

	var session struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	registered.decode(&session)
	u := &user{client: e.anonymous().withToken(session.AccessToken), env: e}

	assertGolden(t, "me_get", u.get("/v1/me").expect(http.StatusOK))

	assertGolden(t, "auth_login", e.anonymous().post("/v1/auth/login", map[string]any{
		"email":    "golden@example.test",
		"password": "senha-de-teste-123",
	}).expect(http.StatusOK))

	assertGolden(t, "auth_refresh", e.anonymous().post("/v1/auth/refresh", map[string]any{
		"refresh_token": session.RefreshToken,
	}).expect(http.StatusOK))

	// ---------- vehicle ----------

	created := u.post("/v1/vehicles", map[string]any{
		"vehicle_type":       "car",
		"brand":              "Volkswagen",
		"model":              "Gol",
		"version":            "1.6 MSI Comfortline",
		"manufacture_year":   2019,
		"model_year":         2020,
		"plate":              "ABC1D23",
		"renavam":            "12345678901",
		"chassis":            "9BWZZZ377VT004251",
		"fuel_type":          "flex",
		"color":              "prata",
		"nickname":           "Golzinho",
		"current_mileage_km": 50_000,
	}).expect(http.StatusCreated)
	assertGolden(t, "vehicle_create", created)

	vehicleID := created.id()
	vehiclePath := "/v1/vehicles/" + vehicleID

	assertGolden(t, "vehicle_get", u.get(vehiclePath).expect(http.StatusOK))
	assertGolden(t, "vehicles_list", u.get("/v1/vehicles").expect(http.StatusOK))

	// ---------- maintenance ----------

	assertGolden(t, "maintenance_items_list",
		u.get("/v1/maintenance-items?vehicle_type=car").expect(http.StatusOK))
	assertGolden(t, "maintenance_plans_list",
		u.get(vehiclePath+"/maintenance-plans").expect(http.StatusOK))

	// A service two years back at a lower mileage. It is the baseline of a used car
	// (RN-03) and, more usefully here, it starts every clock far enough in the past that
	// the due engine has something to say.
	twoYearsAgo := e.today().AddDate(-2, 0, 0)
	record := u.post(vehiclePath+"/maintenance-records", map[string]any{
		"occurred_on":      twoYearsAgo.Format("2006-01-02"),
		"mileage_km":       40_000,
		"kind":             "declared",
		"workshop_name":    "Oficina do Zé",
		"total_cost_cents": 45_000,
		"notes":            "Revisão feita antes da compra.",
		"items": []map[string]any{{
			"maintenance_item_id": u.firstItemID(),
			"description":         "Óleo e filtro",
			"part_brand":          "Mobil",
			"cost_cents":          45_000,
			"warranty_months":     6,
			"warranty_km":         10_000,
		}},
	}).expect(http.StatusCreated)
	assertGolden(t, "maintenance_record_create", record)

	assertGolden(t, "maintenance_record_get",
		u.get("/v1/maintenance-records/"+record.id()).expect(http.StatusOK))
	assertGolden(t, "maintenance_records_list",
		u.get(vehiclePath+"/maintenance-records").expect(http.StatusOK))

	// ---------- odometer ----------

	// 55.000 km after that service, so the km-based plans are comfortably overdue and the
	// alerts snapshot below has real entries in it.
	assertGolden(t, "odometer_create", u.post(vehiclePath+"/odometer", map[string]any{
		"mileage_km": 95_000,
		"source":     "manual",
		"notes":      "Leitura do painel.",
	}).expect(http.StatusCreated))

	assertGolden(t, "odometer_list", u.get(vehiclePath+"/odometer").expect(http.StatusOK))

	// ---------- obligations ----------

	obligation := u.post(vehiclePath+"/obligations", map[string]any{
		"kind":           "ipva",
		"reference_year": e.today().Year(),
		"due_on":         e.today().AddDate(0, 1, 0).Format("2006-01-02"),
		"amount_cents":   120_000,
		"notes":          "Cota única.",
	}).expect(http.StatusCreated)
	assertGolden(t, "obligation_create", obligation)
	assertGolden(t, "obligations_list", u.get(vehiclePath+"/obligations").expect(http.StatusOK))

	seguro := u.post(vehiclePath+"/seguros", map[string]any{
		"insurer_name":    "Seguradora Teste",
		"policy_number":   "APOLICE-1",
		"starts_on":       e.today().AddDate(0, -6, 0).Format("2006-01-02"),
		"ends_on":         e.today().AddDate(0, 6, 0).Format("2006-01-02"),
		"premium_cents":   250_000,
		"emergency_phone": "0800 000 0000",
		"broker_name":     "Corretora Teste",
		"broker_phone":    "11 90000-0000",
		"notes":           "Franquia reduzida.",
	}).expect(http.StatusCreated)
	assertGolden(t, "seguro_create", seguro)
	assertGolden(t, "seguros_list", u.get(vehiclePath+"/seguros").expect(http.StatusOK))

	// ---------- read models ----------

	assertGolden(t, "dashboard", u.get(vehiclePath+"/dashboard").expect(http.StatusOK))
	assertGolden(t, "alerts", u.get(vehiclePath+"/alerts").expect(http.StatusOK))
	assertGolden(t, "timeline", u.get(vehiclePath+"/timeline").expect(http.StatusOK))

	// ---------- the error envelope ----------
	//
	// Every failure the app can see shares one shape (SPEC.md section 7), and the app
	// parses all of them the same way. These snapshots are the reason a details key cannot
	// quietly disappear from under a client that reads it.

	assertGolden(t, "error_validation", u.post("/v1/vehicles", map[string]any{
		"brand": "", "model": "", "plate": "XX",
	}).expectError(http.StatusUnprocessableEntity, "validation_failed"))

	assertGolden(t, "error_not_found",
		u.get("/v1/vehicles/00000000-0000-0000-0000-000000000000").expectError(http.StatusNotFound, "not_found"))

	assertGolden(t, "error_unauthorized",
		e.anonymous().get("/v1/me").expectError(http.StatusUnauthorized, "unauthorized"))

	// The rollback details carry the numbers the app puts in front of the user, so they
	// are contract in exactly the same way the fields of a vehicle are.
	assertGolden(t, "error_odometer_rollback", u.post(vehiclePath+"/odometer", map[string]any{
		"mileage_km": 10,
		"source":     "manual",
	}).expectError(http.StatusUnprocessableEntity, "odometer_rollback"))
}

// ---------- the golden mechanism ----------

// assertGolden compares a response's shape against test/golden/<name>.json.
func assertGolden(t *testing.T, name string, res *response) {
	t.Helper()

	got, err := json.MarshalIndent(map[string]any{
		"status": res.Status,
		"body":   shapeOf(decodeAny(t, res)),
	}, "", "  ")
	if err != nil {
		t.Fatalf("%s: encode shape: %v", name, err)
	}
	got = append(got, '\n')

	path := filepath.Join(goldenDir, name+".json")

	if *updateGolden {
		if err := os.MkdirAll(goldenDir, 0o755); err != nil {
			t.Fatalf("%s: create %s: %v", name, goldenDir, err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("%s: write golden: %v", name, err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: read golden: %v\n\nRun `go test ./test/integration -run TestGolden -update` "+
			"to create it, then read the file before committing it.", name, err)
	}

	if string(normalizeNewlines(want)) != string(got) {
		t.Errorf("%s: the response no longer matches %s\n\n%s\n\n"+
			"If this change is intended, regenerate with `-update` — and decide what it "+
			"does to app versions already installed (SPEC.md D-01).",
			name, path, diffLines(string(normalizeNewlines(want)), string(got)))
	}
}

func decodeAny(t *testing.T, res *response) any {
	t.Helper()

	if len(res.Body) == 0 {
		return nil
	}
	var out any
	if err := json.Unmarshal(res.Body, &out); err != nil {
		t.Fatalf("%s %s: response is not JSON: %v\nbody: %s",
			res.method, res.path, err, res.Body)
	}
	return out
}

// shapeOf replaces every leaf with the name of its JSON type, keeping the structure.
//
// Maps keep their keys — that is the whole point. Arrays collapse to a single merged
// element shape, so a response whose list happens to be one item long today and three
// tomorrow produces the same golden, and a heterogeneous list (the timeline mixes services,
// readings and obligations) records the union of what its entries can carry.
func shapeOf(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = shapeOf(item)
		}
		return out

	case []any:
		if len(v) == 0 {
			return []any{}
		}
		merged := shapeOf(v[0])
		for _, item := range v[1:] {
			merged = mergeShapes(merged, shapeOf(item))
		}
		return []any{merged}

	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "boolean"
	case nil:
		return "null"
	default:
		return fmt.Sprintf("unknown(%T)", value)
	}
}

// mergeShapes unions two shapes.
//
// A key present in one element and absent from another is kept — an optional field is part
// of the contract too. A leaf that is a string in one entry and null in another becomes
// "null|string", which is precisely the fact a client needs to know.
func mergeShapes(a, b any) any {
	aMap, aIsMap := a.(map[string]any)
	bMap, bIsMap := b.(map[string]any)
	if aIsMap && bIsMap {
		out := make(map[string]any, len(aMap))
		for key, value := range aMap {
			out[key] = value
		}
		for key, value := range bMap {
			if existing, ok := out[key]; ok {
				out[key] = mergeShapes(existing, value)
				continue
			}
			out[key] = value
		}
		return out
	}

	aSlice, aIsSlice := a.([]any)
	bSlice, bIsSlice := b.([]any)
	if aIsSlice && bIsSlice {
		switch {
		case len(aSlice) == 0:
			return bSlice
		case len(bSlice) == 0:
			return aSlice
		default:
			return []any{mergeShapes(aSlice[0], bSlice[0])}
		}
	}

	aLeaf, aIsLeaf := a.(string)
	bLeaf, bIsLeaf := b.(string)
	if aIsLeaf && bIsLeaf {
		return mergeLeaves(aLeaf, bLeaf)
	}

	// Shapes of different kinds — an object in one entry and a scalar in another. Record it
	// rather than picking one, so the golden shows something is inconsistent.
	return fmt.Sprintf("mixed(%v|%v)", a, b)
}

// mergeLeaves keeps the alternatives sorted and deduplicated, so "null|string" never shows
// up as "string|null" and flips the golden for no reason.
func mergeLeaves(a, b string) string {
	seen := map[string]bool{}
	for _, part := range append(strings.Split(a, "|"), strings.Split(b, "|")...) {
		seen[part] = true
	}
	parts := make([]string, 0, len(seen))
	for part := range seen {
		parts = append(parts, part)
	}
	sort.Strings(parts)
	return strings.Join(parts, "|")
}

// normalizeNewlines makes the comparison survive a checkout with CRLF line endings, which
// is the default on the Windows machine this repo is developed on.
func normalizeNewlines(b []byte) []byte {
	return []byte(strings.ReplaceAll(string(b), "\r\n", "\n"))
}

// diffLines renders a minimal line-level difference. It is not a diff algorithm; it points
// at the first line that disagrees and shows the neighbourhood, which is all that is needed
// when the two sides are the same document with one field changed.
func diffLines(want, got string) string {
	wantLines := strings.Split(want, "\n")
	gotLines := strings.Split(got, "\n")

	var b strings.Builder
	for i := 0; i < max(len(wantLines), len(gotLines)); i++ {
		wantLine := lineAt(wantLines, i)
		gotLine := lineAt(gotLines, i)
		if wantLine == gotLine {
			continue
		}
		fmt.Fprintf(&b, "line %d:\n  golden: %s\n  actual: %s\n", i+1, wantLine, gotLine)
	}
	if b.Len() == 0 {
		return "(the files differ only in trailing whitespace)"
	}
	return b.String()
}

func lineAt(lines []string, i int) string {
	if i < len(lines) {
		return lines[i]
	}
	return "(absent)"
}
