// Package fipe talks to the Parallelum FIPE API, and is the only thing in this codebase
// that knows that provider exists.
//
// It lives under internal/catalog rather than under internal/platform because it is not
// generic: it knows about brands, models and fuel, which is domain vocabulary, and
// platform packages carry none. It sits beside internal/catalog/db for the same reason
// that package does — both are places the catalogue module gets rows from, one local and
// one remote, and neither is visible to any other module.
//
// # What this package will not do
//
//   - It does not return its own JSON shapes to anything above it. Callers get the types
//     declared here, and the catalogue service translates those into rows and DTOs. The
//     day the provider renames a field, the change stops at this file.
//   - It does not decide what the user is told. Failures come back as the sentinels below;
//     turning one into an HTTP response is the service's job.
//   - It does not retry. See Client.get.
//
// # The provider's contract, confirmed against the live API
//
//	GET /{type}/brands                                    → [{code, name}]
//	GET /{type}/brands/{b}/models                         → [{code, name}]
//	GET /{type}/brands/{b}/models/{m}/years               → [{code, name}]
//	GET /{type}/brands/{b}/models/{m}/years/{y}           → one vehicle, with the price
//
// {type} is "cars" | "motorcycles" | "trucks". A year code is "2017-6" — the model year
// and the provider's fuel code — except for a brand new car, which is "32000-1". Prices
// arrive as the string "R$ 70.470,00" and reference months as "agosto de 2026".
//
// Authentication is the X-Subscription-Token header and is optional: without it the quota
// is 500 requests a day, with a free token 1000. That quota is the whole reason the
// catalogue is mirrored into Postgres.
package fipe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL is the provider's v2 API. v1 is still served but returns Portuguese field
// names and no fuel acronym, and it is the version the provider documents as superseded.
const DefaultBaseURL = "https://parallelum.com.br/fipe/api/v2"

// requestTimeout bounds a single call to the provider.
//
// A constant rather than a setting: there is no deployment where a different value would
// be right, and a knob nobody turns is a knob that is wrong in production. Eight seconds
// is generous for a JSON list and still well inside the patience of somebody who just
// tapped a dropdown.
const requestTimeout = 8 * time.Second

// maxResponseBytes caps what will be read from the provider.
//
// The largest real response is a model list, tens of kilobytes. This is not a tuning
// parameter; it is the difference between a misbehaving third party costing us a request
// and costing us the process.
const maxResponseBytes = 4 << 20 // 4 MiB

// VehicleType is the provider's vocabulary, which is not ours: it says "cars" where the
// rest of this codebase says "car". Keeping the two apart in the type system is what stops
// the provider's plural leaking into a database column.
type VehicleType string

const (
	Cars        VehicleType = "cars"
	Motorcycles VehicleType = "motorcycles"
	Trucks      VehicleType = "trucks"
)

// Failures a caller can act on differently. Everything else is Err Unavailable.
var (
	// ErrNotFound is a 404 from the provider: the brand, model or year does not exist
	// there. It means our mirror is out of step, not that the caller did anything wrong.
	ErrNotFound = errors.New("fipe: not found upstream")

	// ErrRateLimited is the daily quota. Deliberately distinct from ErrUnavailable so the
	// service can log it as the operational problem it is — a quota that runs out at
	// midday is a signal to buy a token, not to debug a timeout.
	ErrRateLimited = errors.New("fipe: rate limited upstream")

	// ErrUnavailable covers every transport failure and every 5xx: DNS, connection
	// refused, TLS, timeout, 502. From here they are one thing — the provider cannot
	// answer right now.
	ErrUnavailable = errors.New("fipe: provider unavailable")

	// ErrInvalidResponse is a 200 whose body is not what the contract says. It is kept
	// apart from ErrUnavailable because it never resolves itself: a retry in five minutes
	// fixes an outage and does nothing at all for a changed schema.
	ErrInvalidResponse = errors.New("fipe: invalid response from provider")
)

// NamedCode is a brand, a model or a year — the provider returns the same two fields for
// all three.
type NamedCode struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// Vehicle is the provider's detail response.
//
// Only the three fields the catalogue actually uses are declared. The provider also sends
// brand, model, modelYear, fuel, fuelAcronym, vehicleType and — on some plans — a
// priceHistory array; every one of those is either already in our own tables or something
// nothing reads, and a decoded field that nothing reads is an invitation to decide later
// what it was for. The wire format is documented by the verbatim payload in the test.
type Vehicle struct {
	// CodeFipe identifies the vehicle in the FIPE table ("005340-6"). Catalogue data
	// rather than price data — it does not change month to month.
	CodeFipe string `json:"codeFipe"`

	// Price arrives as "R$ 70.470,00" and is parsed into centavos by the caller.
	Price string `json:"price"`

	// ReferenceMonth arrives as "agosto de 2026".
	ReferenceMonth string `json:"referenceMonth"`
}

// Client is an HTTP client for one provider account.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
	log     *slog.Logger
}

// New builds a client. An empty baseURL falls back to the provider's public API; an empty
// token means unauthenticated, which the provider allows at a lower quota.
func New(baseURL, token string, log *slog.Logger) *Client {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		baseURL: baseURL,
		token:   strings.TrimSpace(token),
		// The per-request context carries the real deadline; this is the backstop for a
		// caller that somehow arrives with a context that never cancels.
		http: &http.Client{Timeout: requestTimeout},
		log:  log,
	}
}

// Brands returns every brand the provider knows for a vehicle type.
func (c *Client) Brands(ctx context.Context, vehicleType VehicleType) ([]NamedCode, error) {
	var out []NamedCode
	err := c.get(ctx, &out, "brands", string(vehicleType), "brands")
	return out, err
}

// Models returns every model of one brand.
func (c *Client) Models(ctx context.Context, vehicleType VehicleType, brandCode string) ([]NamedCode, error) {
	var out []NamedCode
	err := c.get(ctx, &out, "models", string(vehicleType), "brands", brandCode, "models")
	return out, err
}

// Years returns every model year of one model.
func (c *Client) Years(ctx context.Context, vehicleType VehicleType, brandCode, modelCode string) ([]NamedCode, error) {
	var out []NamedCode
	err := c.get(ctx, &out, "years",
		string(vehicleType), "brands", brandCode, "models", modelCode, "years")
	return out, err
}

// Vehicle returns the detail of one model year, including the FIPE code and the current
// price.
func (c *Client) Vehicle(ctx context.Context, vehicleType VehicleType, brandCode, modelCode, yearCode string) (Vehicle, error) {
	var out Vehicle
	err := c.get(ctx, &out, "vehicle",
		string(vehicleType), "brands", brandCode, "models", modelCode, "years", yearCode)
	if err != nil {
		return Vehicle{}, err
	}
	// A 200 with an empty FIPE code is not a vehicle. Letting it through would write a row
	// claiming a price belongs to a code that identifies nothing.
	if strings.TrimSpace(out.CodeFipe) == "" {
		return Vehicle{}, fmt.Errorf("%w: vehicle detail has no codeFipe", ErrInvalidResponse)
	}
	return out, nil
}

// get performs one request and decodes the body into dst.
//
// # No retry, deliberately
//
// The obvious place for one is ErrUnavailable. It is not worth it here. The free quota is
// 500 requests a day and a retry doubles the cost of exactly the failures where it helps
// least — a rate limit retried is a rate limit hit twice, and an outage retried is an
// outage waited through with the user's request held open. A transient blip costs one tap,
// and the next tap is the retry, made by a person who knows whether they still want the
// answer. If this ever changes, the place for it is here and the condition is
// ErrUnavailable alone: a 4xx must never be retried, since nothing about it will differ.
//
// # No caller-supplied path segments
//
// Every segment comes from a column in our own database, and each one is escaped anyway.
// A value that reached this function straight from a request body would let a client aim
// our credentials at a URL of their choosing.
func (c *Client) get(ctx context.Context, dst any, operation string, segments ...string) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	endpoint := c.baseURL
	for _, segment := range segments {
		endpoint += "/" + url.PathEscape(segment)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		// A malformed URL is a bug here, not an outage there.
		return fmt.Errorf("%w: build request: %v", ErrInvalidResponse, err)
	}
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("X-Subscription-Token", c.token)
	}

	started := time.Now()
	res, err := c.http.Do(req)
	elapsed := time.Since(started)

	if err != nil {
		// The error text can contain the full URL but never the token — that travels as a
		// header, and headers are not part of an *url.Error.
		c.logRequest(ctx, operation, 0, elapsed, err)
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer func() { _ = res.Body.Close() }()

	if err := statusError(res.StatusCode); err != nil {
		c.logRequest(ctx, operation, res.StatusCode, elapsed, err)
		return err
	}

	body, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes))
	if err != nil {
		c.logRequest(ctx, operation, res.StatusCode, elapsed, err)
		return fmt.Errorf("%w: read body: %v", ErrUnavailable, err)
	}

	if err := json.Unmarshal(body, dst); err != nil {
		// The body is not logged. It is somebody else's payload and it is large; the
		// status and the decode error are what tell us whether the contract moved.
		c.logRequest(ctx, operation, res.StatusCode, elapsed, err)
		return fmt.Errorf("%w: decode body: %v", ErrInvalidResponse, err)
	}

	c.logRequest(ctx, operation, res.StatusCode, elapsed, nil)
	return nil
}

// statusError maps an HTTP status onto one of the sentinels, or nil for success.
func statusError(status int) error {
	switch {
	case status >= 200 && status < 300:
		return nil
	case status == http.StatusNotFound:
		return ErrNotFound
	case status == http.StatusTooManyRequests:
		return ErrRateLimited
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		// A rejected token is our misconfiguration, not the caller's problem. It is
		// reported as unavailable because from the user's side that is exactly what it
		// is; the log line below is what tells an operator which it was.
		return fmt.Errorf("%w: provider rejected our credentials (status %d)",
			ErrUnavailable, status)
	case status >= 500:
		return fmt.Errorf("%w: provider returned status %d", ErrUnavailable, status)
	default:
		// A 400 usually means we sent something the provider does not recognise — a
		// vehicle type it does not serve, a malformed code. It will not resolve on its
		// own, so it is an invalid exchange rather than an outage.
		return fmt.Errorf("%w: provider returned status %d", ErrInvalidResponse, status)
	}
}

// logRequest emits one structured line per external call.
//
// External calls are rare by design and each one spends quota, so every single one is
// worth a line. The fields are the ones an operator would filter on; the token, the
// headers and the body are not among them.
func (c *Client) logRequest(ctx context.Context, operation string, status int, elapsed time.Duration, err error) {
	if c.log == nil {
		return
	}
	attrs := []any{
		slog.String("provider", "fipe_parallelum"),
		slog.String("operation", operation),
		slog.Int("status", status),
		slog.Int64("duration_ms", elapsed.Milliseconds()),
	}
	if err != nil {
		c.log.ErrorContext(ctx, "fipe request failed", append(attrs, slog.Any("error", err))...)
		return
	}
	c.log.InfoContext(ctx, "fipe request completed", attrs...)
}
