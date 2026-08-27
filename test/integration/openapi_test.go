package integration

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"
)

// api/openapi.yaml is the contract, and it is hand-written (SPEC.md D-03). The Flutter app
// writes its Dart models by hand; the two repositories deploy independently, and a
// shipped app cannot be force-updated — so a route that exists in one and not the other is
// not a documentation problem, it is a client that calls a 404 or an endpoint no client
// knows about. The matching guard on the app side is test/contract/openapi_paths_test.dart.
//
// CLAUDE.md said plainly that nothing automated caught that drift. This does.

const openAPIPath = "../../api/openapi.yaml"

// openAPIDocument is the sliver of the spec this test needs. Decoding into a narrow struct
// rather than a generic map keeps the test from silently passing if `paths` were renamed.
type openAPIDocument struct {
	Paths map[string]map[string]yaml.Node `yaml:"paths"`
}

// httpMethods are the keys under a path item that denote an operation. Everything else
// there — parameters, summary, servers — describes the path, not a route.
var httpMethods = map[string]bool{
	"get": true, "put": true, "post": true, "delete": true,
	"patch": true, "head": true, "options": true, "trace": true,
}

func TestRouterAndOpenAPIAgree(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	documented := readOpenAPIOperations(t)
	served := walkRoutes(t, e.handler)

	missingFromSpec := difference(served, documented)
	if len(missingFromSpec) > 0 {
		t.Errorf("served but absent from %s:\n  %s\n\n"+
			"The app writes its Dart models by hand against this file: an endpoint "+
			"missing there does not exist as far as the app is concerned.",
			filepath.Base(openAPIPath), strings.Join(missingFromSpec, "\n  "))
	}

	missingFromRouter := difference(documented, served)
	if len(missingFromRouter) > 0 {
		t.Errorf("documented in %s but not served:\n  %s\n\n"+
			"The app will call these and get the 404 envelope.",
			filepath.Base(openAPIPath), strings.Join(missingFromRouter, "\n  "))
	}
}

// readOpenAPIOperations returns every documented operation as "METHOD /normalised/path".
func readOpenAPIOperations(t *testing.T) map[string]bool {
	t.Helper()

	raw, err := os.ReadFile(openAPIPath)
	if err != nil {
		t.Fatalf("read %s: %v", openAPIPath, err)
	}

	var doc openAPIDocument
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", openAPIPath, err)
	}
	if len(doc.Paths) == 0 {
		t.Fatalf("%s declares no paths — the contract cannot be empty", openAPIPath)
	}

	operations := make(map[string]bool, len(doc.Paths))
	for path, item := range doc.Paths {
		for key := range item {
			if !httpMethods[strings.ToLower(key)] {
				continue
			}
			operations[strings.ToUpper(key)+" "+normalizeParams(path)] = true
		}
	}
	return operations
}

func walkRoutes(t *testing.T, handler http.Handler) map[string]bool {
	t.Helper()

	routes, ok := handler.(chi.Routes)
	if !ok {
		t.Fatalf("the router is %T, which cannot be walked", handler)
	}

	served := map[string]bool{}
	err := chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		served[method+" "+normalizeParams(normalizePattern(route))] = true
		return nil
	})
	if err != nil {
		t.Fatalf("walk routes: %v", err)
	}
	return served
}

var pathParam = regexp.MustCompile(`\{[^}]*\}`)

// normalizeParams erases the name of a path parameter.
//
// chi uses {vehicleID} and the spec uses {vehicleId}, and neither is wrong: one is a Go
// identifier and the other follows the OpenAPI style the generator expects. What has to
// match is the shape of the path, not the spelling of a placeholder — comparing the names
// would produce a permanently failing test that everyone learns to ignore.
func normalizeParams(path string) string {
	return pathParam.ReplaceAllString(path, "{}")
}

func difference(from, minus map[string]bool) []string {
	var out []string
	for key := range from {
		if !minus[key] {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}
