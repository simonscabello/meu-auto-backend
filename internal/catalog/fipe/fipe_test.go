package fipe

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Nothing here touches the real provider. A test that did would be slow, would fail when
// somebody else's server has a bad afternoon, and would spend a request from a daily quota
// of five hundred — every run, on every machine, forever.
//
// What is exercised instead is the part that is ours: how a status becomes a typed error,
// how a bad payload is refused, and that no request ever goes out without a deadline.

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestBrandsDecodesTheProviderShape uses the exact payload the live v2 API returns.
func TestBrandsDecodesTheProviderShape(t *testing.T) {
	t.Parallel()

	var gotPath, gotToken, gotAccept string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotToken = r.Header.Get("X-Subscription-Token")
		gotAccept = r.Header.Get("Accept")

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[{"code":"1","name":"Acura"},{"code":"59","name":"VW - VolksWagen"}]`)
	}))
	defer server.Close()

	client := New(server.URL, "a-token", silentLogger())

	brands, err := client.Brands(context.Background(), Cars)
	if err != nil {
		t.Fatalf("Brands: %v", err)
	}

	if gotPath != "/cars/brands" {
		t.Errorf("path = %q, want /cars/brands", gotPath)
	}
	// The token travels as a header and nowhere else. If it ever moved into the query
	// string it would land in access logs on machines we do not control.
	if gotToken != "a-token" {
		t.Errorf("X-Subscription-Token = %q, want the configured token", gotToken)
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept = %q, want application/json", gotAccept)
	}

	if len(brands) != 2 || brands[1].Code != "59" || brands[1].Name != "VW - VolksWagen" {
		t.Fatalf("Brands = %+v, want the two entries from the payload", brands)
	}
}

// TestNoTokenSendsNoHeader covers the unauthenticated tier, which is the default and what
// a developer runs against locally.
func TestNoTokenSendsNoHeader(t *testing.T) {
	t.Parallel()

	headerPresent := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, headerPresent = r.Header["X-Subscription-Token"]
		_, _ = io.WriteString(w, `[]`)
	}))
	defer server.Close()

	if _, err := New(server.URL, "", silentLogger()).Brands(context.Background(), Cars); err != nil {
		t.Fatalf("Brands: %v", err)
	}
	if headerPresent {
		t.Error("an empty token still sent the header; it must be omitted entirely")
	}
}

// TestPathSegmentsAreEscaped is a security property, not a formatting one.
//
// Every segment comes from a column in our own database today, but a stored value with a
// slash in it must not be able to reshape the URL — that is how one endpoint becomes
// another, with our credentials attached.
func TestPathSegmentsAreEscaped(t *testing.T) {
	t.Parallel()

	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		_, _ = io.WriteString(w, `[]`)
	}))
	defer server.Close()

	_, err := New(server.URL, "", silentLogger()).
		Models(context.Background(), Cars, "59/../../admin")
	if err != nil {
		t.Fatalf("Models: %v", err)
	}

	if strings.Contains(gotPath, "/../") {
		t.Fatalf("path = %q — a stored value escaped its segment", gotPath)
	}
	if !strings.HasPrefix(gotPath, "/cars/brands/") {
		t.Fatalf("path = %q, want it still rooted at /cars/brands/", gotPath)
	}
}

// TestStatusMapping is the table the service switches on. Each row is a real failure mode
// of the provider, and the sentinel decides what the user is eventually told.
func TestStatusMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"missing upstream", http.StatusNotFound,
			`{"error":"recurso não encontrado para a referência informada"}`, ErrNotFound},

		// The daily quota. Distinct from a plain outage because the fix is different:
		// this one is answered by buying a token, not by waiting.
		{"daily quota exhausted", http.StatusTooManyRequests, `{}`, ErrRateLimited},

		{"provider broken", http.StatusInternalServerError, `{}`, ErrUnavailable},
		{"provider gateway", http.StatusBadGateway, `{}`, ErrUnavailable},

		// A rejected token is our misconfiguration. The caller cannot act on it, so it is
		// reported as unavailable; the log line is what tells an operator which it was.
		{"our token was rejected", http.StatusUnauthorized, `{}`, ErrUnavailable},
		{"our token is forbidden", http.StatusForbidden, `{}`, ErrUnavailable},

		// A 400 will not fix itself: we asked for something the provider does not serve.
		{"we asked for something invalid", http.StatusBadRequest,
			`{"error":"invalid vehicle type"}`, ErrInvalidResponse},

		// A 200 is not enough. The contract moving is a different problem from an outage,
		// and retrying would help one and never the other.
		{"success with a body that is not the contract", http.StatusOK,
			`{"unexpected":"object where a list belongs"}`, ErrInvalidResponse},
		{"success with a truncated body", http.StatusOK, `[{"code":`, ErrInvalidResponse},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer server.Close()

			_, err := New(server.URL, "", silentLogger()).Brands(context.Background(), Cars)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Brands() error = %v, want %v", err, tt.want)
			}
		})
	}
}

// TestConnectionRefusedIsUnavailable covers the transport failing before any status
// exists — DNS, a refused connection, a dead host.
func TestConnectionRefusedIsUnavailable(t *testing.T) {
	t.Parallel()

	// Started and immediately closed: the address is real and nothing is listening on it.
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close()

	_, err := New(url, "", silentLogger()).Brands(context.Background(), Cars)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Brands() error = %v, want ErrUnavailable", err)
	}
}

// TestCancelledContextIsHonoured proves the deadline reaches the request rather than being
// decoration on the signature. A provider that never answers must not hold a request open.
func TestCancelledContextIsHonoured(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		_, _ = io.WriteString(w, `[]`)
	}))
	defer server.Close()
	defer close(release)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	started := time.Now()
	_, err := New(server.URL, "", silentLogger()).Brands(ctx, Cars)

	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Brands() error = %v, want ErrUnavailable", err)
	}
	// Well under the client's own 8s ceiling: the caller's deadline is what applied.
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("the call took %s — the caller's deadline was not honoured", elapsed)
	}
}

// TestVehicleRequiresAFipeCode guards the one field the price row cannot be written
// without. A 200 with an empty codeFipe is a valuation attached to nothing.
func TestVehicleRequiresAFipeCode(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"price":"R$ 70.470,00","referenceMonth":"agosto de 2026"}`)
	}))
	defer server.Close()

	_, err := New(server.URL, "", silentLogger()).
		Vehicle(context.Background(), Cars, "59", "5585", "2012-3")
	if !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("Vehicle() error = %v, want ErrInvalidResponse", err)
	}
}

// TestVehicleDecodesTheProviderShape uses a payload captured verbatim from the live API.
func TestVehicleDecodesTheProviderShape(t *testing.T) {
	t.Parallel()

	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = io.WriteString(w, `{"vehicleType":1,"price":"R$ 70.470,00",`+
			`"brand":"VW - VolksWagen","model":"AMAROK CD2.0 16V/S CD2.0 16V TDI 4x2 Die",`+
			`"modelYear":2012,"fuel":"Diesel","codeFipe":"005329-5",`+
			`"referenceMonth":"agosto de 2026","fuelAcronym":"D"}`)
	}))
	defer server.Close()

	vehicle, err := New(server.URL, "", silentLogger()).
		Vehicle(context.Background(), Cars, "59", "5585", "2012-3")
	if err != nil {
		t.Fatalf("Vehicle: %v", err)
	}

	if gotPath != "/cars/brands/59/models/5585/years/2012-3" {
		t.Errorf("path = %q", gotPath)
	}
	if vehicle.CodeFipe != "005329-5" {
		t.Errorf("CodeFipe = %q, want 005329-5", vehicle.CodeFipe)
	}
	if vehicle.Price != "R$ 70.470,00" {
		t.Errorf("Price = %q", vehicle.Price)
	}
	if vehicle.ReferenceMonth != "agosto de 2026" {
		t.Errorf("ReferenceMonth = %q", vehicle.ReferenceMonth)
	}
	// The payload above is a verbatim capture of the live API and carries brand, model,
	// modelYear, fuel, fuelAcronym and vehicleType as well. Vehicle declares none of them
	// on purpose — decoding must ignore the extras rather than fail on them, which is what
	// the three assertions above prove.
}

// TestDefaultBaseURLWhenUnset keeps the configuration optional: the catalogue works with
// no FIPE variables set at all.
func TestDefaultBaseURLWhenUnset(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"", "   "} {
		if got := New(raw, "", silentLogger()).baseURL; got != DefaultBaseURL {
			t.Errorf("New(%q).baseURL = %q, want %q", raw, got, DefaultBaseURL)
		}
	}

	// A trailing slash in the environment must not produce "//cars/brands".
	if got := New("https://example.test/api/v2/", "", silentLogger()).baseURL; got != "https://example.test/api/v2" {
		t.Errorf("baseURL = %q, want the trailing slash trimmed", got)
	}
}
