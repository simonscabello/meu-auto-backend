// Package integration exercises the API the way the Flutter app does: over HTTP, through
// the real router, against a real PostgreSQL.
//
// Nothing here reaches into a service or a repository directly. That is the point — the
// layers below the pure logic in maintenance/due.go had never been covered by a test, and
// the gaps that matter (a wrong join, a missing ownership filter, a transaction that
// commits half of an aggregate) are invisible from inside a single layer.
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/simonscabello/meu-auto-backend/internal/app"
	"github.com/simonscabello/meu-auto-backend/internal/platform/config"
	"github.com/simonscabello/meu-auto-backend/test/testdb"
)

func TestMain(m *testing.M) {
	// The request middleware logs through slog.Default — cmd/api sets it at boot, and in a
	// test binary it is still the stdlib handler writing to stderr. Ninety rejected
	// requests from the authorisation matrix would bury the one line that matters.
	//
	// TEST_LOG=1 puts it back when a failure needs the request log to explain itself.
	if os.Getenv("TEST_LOG") == "" {
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	}
	os.Exit(testdb.Main(m))
}

// testJWTSecret is fixed and meaningless. config.Load refuses anything under 32
// characters, and a random one per run would make a failing token impossible to inspect.
const testJWTSecret = "integration-test-secret-not-a-real-one"

// unreachableFipeURL is where the vehicle catalogue points unless a test says otherwise.
//
// NOT the real provider, and this is deliberate rather than tidy. Port 1 on the loopback
// refuses instantly, so a test that reaches the catalogue without meaning to fails fast
// and locally instead of spending one of five hundred daily requests from somebody's
// network — every run, on every machine, in CI.
//
// A test that wants the catalogue to work passes withFipeServer.
const unreachableFipeURL = "http://127.0.0.1:1"

// envOption tunes one test's world. Variadic on newEnv so the sixty existing calls keep
// meaning exactly what they meant.
type envOption func(*config.Config)

// withFipeServer points the vehicle catalogue at a stand-in for the FIPE provider.
//
// The REAL client runs against it — this substitutes the server, not the code. Status
// mapping, path building, header handling and JSON decoding are all exercised, which a
// hand-written fake client would have quietly skipped.
func withFipeServer(baseURL string) envOption {
	return func(cfg *config.Config) { cfg.FipeAPIURL = baseURL }
}

var emailCounter atomic.Uint64

// env is one test's world: its own database, its own router, its own rate limiters.
type env struct {
	t       *testing.T
	db      *testdb.DB
	handler http.Handler
	mailer  *captureMailer

	// location is what the API resolves "today" against. Tests that care about a due date
	// compute the expected value from env.today() rather than from time.Now, so a run at
	// 23:50 in São Paulo does not disagree with the server about which day it is.
	location *time.Location
}

// newEnv boots the application against a throwaway database.
//
// It calls app.New — the same function cmd/api serves — so a module added to the wiring
// is covered here without anyone remembering to update a fixture.
func newEnv(t *testing.T, opts ...envOption) *env {
	t.Helper()

	db := testdb.New(t)

	location, err := time.LoadLocation(config.TimeZone)
	if err != nil {
		t.Fatalf("load timezone %s: %v", config.TimeZone, err)
	}

	mail := &captureMailer{}

	cfg := config.Config{
		AppEnv:           config.EnvDevelopment,
		Port:             "0",
		DatabaseURL:      db.URL,
		JWTSecret:        testJWTSecret,
		LogLevel:         slog.LevelError,
		CORSOrigins:      []string{"*"},
		PasswordResetURL: "meuauto://redefinir-senha",
		TrustProxy:       false,
		FipeAPIURL:       unreachableFipeURL,
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	return &env{
		t:  t,
		db: db,
		handler: app.New(cfg, app.Deps{
			Pool:     db.Pool,
			Mailer:   mail,
			Location: location,
			Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		}),
		mailer:   mail,
		location: location,
	}
}

// today is the civil date the server will use, in the product's timezone.
func (e *env) today() time.Time {
	now := time.Now().In(e.location)
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}

// ---------- users ----------

// user is an authenticated caller.
type user struct {
	*client

	// env is carried so a fixture can ask the server's question — "what is today in São
	// Paulo" — rather than the test machine's.
	env *env

	ID           string
	Email        string
	Password     string
	RefreshToken string
}

// newUser registers a fresh account and returns a client already carrying its token.
//
// Register, not login: login is rate limited by e-mail and by IP, and a suite that sets up
// its fixtures through it would eventually start failing for reasons that have nothing to
// do with what it was testing.
func (e *env) newUser() *user {
	e.t.Helper()

	email := fmt.Sprintf("user%d@example.test", emailCounter.Add(1))
	const password = "senha-de-teste-123"

	res := e.anonymous().post("/v1/auth/register", map[string]any{
		"name":     "Usuária de Teste",
		"email":    email,
		"password": password,
	})
	res.expect(http.StatusCreated)

	var body struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	res.decode(&body)

	return &user{
		client:       e.anonymous().withToken(body.AccessToken),
		env:          e,
		ID:           body.User.ID,
		Email:        email,
		Password:     password,
		RefreshToken: body.RefreshToken,
	}
}

// ---------- HTTP client ----------

// client issues requests against the router in memory. There is no listening socket: the
// handler under test is the whole stack, and a real port would only add a way for tests to
// interfere with each other.
type client struct {
	t       *testing.T
	handler http.Handler
	token   string
}

func (e *env) anonymous() *client {
	return &client{t: e.t, handler: e.handler}
}

func (c *client) withToken(token string) *client {
	clone := *c
	clone.token = token
	return &clone
}

func (c *client) get(path string) *response           { return c.do(http.MethodGet, path, nil) }
func (c *client) post(path string, b any) *response   { return c.do(http.MethodPost, path, b) }
func (c *client) patch(path string, b any) *response  { return c.do(http.MethodPatch, path, b) }
func (c *client) delete(path string, b any) *response { return c.do(http.MethodDelete, path, b) }

func (c *client) do(method, path string, body any) *response {
	c.t.Helper()

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			c.t.Fatalf("encode request body for %s %s: %v", method, path, err)
		}
		reader = bytes.NewReader(encoded)
	}

	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	rec := httptest.NewRecorder()
	c.handler.ServeHTTP(rec, req)

	return &response{
		t:      c.t,
		method: method,
		path:   path,
		Status: rec.Code,
		Header: rec.Header(),
		Body:   rec.Body.Bytes(),
	}
}

// response is a recorded reply, with assertions that report the body on failure — a bare
// "want 200, got 422" sends the reader back to the terminal to reproduce it by hand.
type response struct {
	t      *testing.T
	method string
	path   string

	Status int
	Header http.Header
	Body   []byte
}

func (r *response) expect(status int) *response {
	r.t.Helper()
	if r.Status != status {
		r.t.Fatalf("%s %s: status = %d, want %d\nbody: %s",
			r.method, r.path, r.Status, status, r.Body)
	}
	return r
}

// expectError asserts both the HTTP status and the stable error code from the envelope.
//
// Both, always: the app switches on the code in the body, so a handler that returns the
// right status with the wrong code is broken in the way that reaches a user.
func (r *response) expectError(status int, code string) *response {
	r.t.Helper()
	r.expect(status)

	if got := r.errorCode(); got != code {
		r.t.Fatalf("%s %s: error code = %q, want %q\nbody: %s",
			r.method, r.path, got, code, r.Body)
	}
	return r
}

func (r *response) errorCode() string {
	r.t.Helper()

	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(r.Body, &body); err != nil {
		r.t.Fatalf("%s %s: response is not the error envelope: %v\nbody: %s",
			r.method, r.path, err, r.Body)
	}
	return body.Error.Code
}

func (r *response) decode(dst any) {
	r.t.Helper()
	if err := json.Unmarshal(r.Body, dst); err != nil {
		r.t.Fatalf("%s %s: decode response: %v\nbody: %s", r.method, r.path, err, r.Body)
	}
}

// json decodes the body into a generic map, for assertions about one field.
func (r *response) json() map[string]any {
	r.t.Helper()
	out := map[string]any{}
	r.decode(&out)
	return out
}

// id reads the "id" field of a created resource.
func (r *response) id() string {
	r.t.Helper()

	value, ok := r.json()["id"].(string)
	if !ok || value == "" {
		r.t.Fatalf("%s %s: response has no id\nbody: %s", r.method, r.path, r.Body)
	}
	return value
}

// ---------- collaborators ----------

// captureMailer stands in for Resend. It records instead of sending, so the password reset
// flow can be tested end to end — including the token, which never appears in a response.
type captureMailer struct {
	sent []sentEmail
}

type sentEmail struct {
	To       string
	Name     string
	ResetURL string
}

func (m *captureMailer) SendPasswordReset(_ context.Context, to, name, resetURL string) error {
	m.sent = append(m.sent, sentEmail{To: to, Name: name, ResetURL: resetURL})
	return nil
}

// lastResetToken returns the token from the most recent reset e-mail.
func (m *captureMailer) lastResetToken(t *testing.T) string {
	t.Helper()

	if len(m.sent) == 0 {
		t.Fatal("no password reset e-mail was sent")
	}
	_, token, found := strings.Cut(m.sent[len(m.sent)-1].ResetURL, "token=")
	if !found || token == "" {
		t.Fatalf("reset URL carries no token: %q", m.sent[len(m.sent)-1].ResetURL)
	}
	return token
}
