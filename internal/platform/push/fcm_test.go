package push

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func testKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

func pkcs8PEM(t *testing.T, key *rsa.PrivateKey) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func serviceAccountJSON(t *testing.T, key *rsa.PrivateKey, overrides map[string]any) []byte {
	t.Helper()
	file := map[string]any{
		"type":           "service_account",
		"project_id":     "meu-auto-test",
		"private_key_id": "key-1",
		"private_key":    pkcs8PEM(t, key),
		"client_email":   "firebase-adminsdk@meu-auto-test.iam.gserviceaccount.com",
		"token_uri":      "https://oauth2.googleapis.com/token",
	}
	for k, v := range overrides {
		if v == nil {
			delete(file, k)
			continue
		}
		file[k] = v
	}
	raw, err := json.Marshal(file)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

func TestParseServiceAccountReadsTheConsoleFile(t *testing.T) {
	key := testKey(t)

	creds, err := ParseServiceAccount(serviceAccountJSON(t, key, nil))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if creds.ProjectID != "meu-auto-test" || creds.PrivateKeyID != "key-1" ||
		!strings.HasPrefix(creds.ClientEmail, "firebase-adminsdk@") {
		t.Fatalf("unexpected credentials: %+v", creds)
	}
	if !creds.PrivateKey.Equal(key) {
		t.Fatal("the private key was not read back")
	}
}

func TestParseServiceAccountDefaultsTheTokenURI(t *testing.T) {
	creds, err := ParseServiceAccount(serviceAccountJSON(t, testKey(t), map[string]any{"token_uri": nil}))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if creds.TokenURI != defaultTokenURI {
		t.Fatalf("token uri = %q", creds.TokenURI)
	}
}

func TestParseServiceAccountSaysWhatIsWrongWithoutRepeatingTheKey(t *testing.T) {
	key := testKey(t)
	secret := pkcs8PEM(t, key)

	cases := map[string]struct {
		raw  []byte
		want string
	}{
		"not json":      {[]byte("definitely not json"), "not the JSON key file"},
		"missing parts": {serviceAccountJSON(t, key, map[string]any{"project_id": nil, "client_email": ""}), "project_id, client_email"},
		"wrong kind":    {serviceAccountJSON(t, key, map[string]any{"type": "authorized_user"}), `"authorized_user"`},
		"not pem":       {serviceAccountJSON(t, key, map[string]any{"private_key": "abc"}), "not PEM"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseServiceAccount(tc.raw)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "PRIVATE KEY") || strings.Contains(err.Error(), secret[40:80]) {
				t.Fatalf("error repeats the key: %q", err)
			}
		})
	}
}

// fakeGoogle stands in for both the token endpoint and FCM.
type fakeGoogle struct {
	t   *testing.T
	key *rsa.PrivateKey

	tokenRequests atomic.Int32
	sends         []map[string]any
	authHeaders   []string
	paths         []string

	// sendStatus and sendBody script FCM's answer; zero means 200.
	sendStatus int
	sendBody   string
}

func (g *fakeGoogle) server() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		g.tokenRequests.Add(1)
		if err := r.ParseForm(); err != nil {
			g.t.Errorf("token form: %v", err)
		}
		if got := r.Form.Get("grant_type"); got != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
			g.t.Errorf("grant_type = %q", got)
		}
		claims := jwt.MapClaims{}
		parsed, err := jwt.ParseWithClaims(r.Form.Get("assertion"), claims,
			func(token *jwt.Token) (any, error) { return &g.key.PublicKey, nil },
			jwt.WithValidMethods([]string{"RS256"}))
		if err != nil || !parsed.Valid {
			g.t.Errorf("assertion does not verify: %v", err)
		}
		if claims["scope"] != messagingScope {
			g.t.Errorf("scope = %v", claims["scope"])
		}
		if claims["iss"] != "firebase-adminsdk@meu-auto-test.iam.gserviceaccount.com" {
			g.t.Errorf("iss = %v", claims["iss"])
		}
		if parsed != nil && parsed.Header["kid"] != "key-1" {
			g.t.Errorf("kid = %v", parsed.Header["kid"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"ya29.test","expires_in":3599,"token_type":"Bearer"}`)
	})
	mux.HandleFunc("/v1/projects/", func(w http.ResponseWriter, r *http.Request) {
		g.paths = append(g.paths, r.URL.Path)
		g.authHeaders = append(g.authHeaders, r.Header.Get("Authorization"))
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			g.t.Errorf("send body: %v", err)
		}
		g.sends = append(g.sends, body)
		if g.sendStatus != 0 {
			w.WriteHeader(g.sendStatus)
			_, _ = io.WriteString(w, g.sendBody)
			return
		}
		_, _ = io.WriteString(w, `{"name":"projects/meu-auto-test/messages/1"}`)
	})
	return httptest.NewServer(mux)
}

func newTestFCM(t *testing.T) (*FCM, *fakeGoogle) {
	t.Helper()
	key := testKey(t)
	google := &fakeGoogle{t: t, key: key}
	srv := google.server()
	t.Cleanup(srv.Close)

	creds, err := ParseServiceAccount(serviceAccountJSON(t, key, map[string]any{"token_uri": srv.URL + "/token"}))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	fcm := NewFCM(creds)
	fcm.endpoint = srv.URL
	return fcm, google
}

func TestSendDeliversToTheReminderChannel(t *testing.T) {
	fcm, google := newTestFCM(t)

	err := fcm.Send(context.Background(), "device-token", Message{
		Title: "Gol",
		Body:  "IPVA 2026 · Vence hoje",
		Data:  map[string]string{"vehicle_id": "v1"},
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	if len(google.sends) != 1 {
		t.Fatalf("sends = %d", len(google.sends))
	}
	if google.paths[0] != "/v1/projects/meu-auto-test/messages:send" {
		t.Fatalf("path = %q", google.paths[0])
	}
	if google.authHeaders[0] != "Bearer ya29.test" {
		t.Fatalf("authorization = %q", google.authHeaders[0])
	}
	message := google.sends[0]["message"].(map[string]any)
	if message["token"] != "device-token" {
		t.Fatalf("token = %v", message["token"])
	}
	notification := message["notification"].(map[string]any)
	if notification["title"] != "Gol" || notification["body"] != "IPVA 2026 · Vence hoje" {
		t.Fatalf("notification = %v", notification)
	}
	if message["data"].(map[string]any)["vehicle_id"] != "v1" {
		t.Fatalf("data = %v", message["data"])
	}
	android := message["android"].(map[string]any)
	if android["notification"].(map[string]any)["channel_id"] != ChannelID {
		t.Fatalf("android = %v", android)
	}
}

func TestOneAccessTokenServesAMorningOfSends(t *testing.T) {
	fcm, google := newTestFCM(t)
	now := time.Now()
	fcm.now = func() time.Time { return now }

	for range 3 {
		if err := fcm.Send(context.Background(), "t", Message{Title: "x"}); err != nil {
			t.Fatalf("send: %v", err)
		}
	}
	if got := google.tokenRequests.Load(); got != 1 {
		t.Fatalf("token requests = %d, want 1", got)
	}

	// Close to the hour, a new one is asked for before the old one runs out.
	now = now.Add(56 * time.Minute)
	if err := fcm.Send(context.Background(), "t", Message{Title: "x"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if got := google.tokenRequests.Load(); got != 2 {
		t.Fatalf("token requests = %d, want 2", got)
	}
}

func TestOnlyARefusedTokenIsReportedAsUnregistered(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
		dead   bool
	}{
		"unregistered": {
			http.StatusNotFound,
			`{"error":{"code":404,"status":"NOT_FOUND","message":"Requested entity was not found.",
			  "details":[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"UNREGISTERED"}]}}`,
			true,
		},
		"malformed token": {
			http.StatusBadRequest,
			`{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"The registration token is not a valid FCM registration token"}}`,
			true,
		},
		// Both of these are a mistake on this side. Pruning on them would unsubscribe
		// every phone after one bad deploy.
		"wrong project": {
			http.StatusNotFound,
			`{"error":{"code":404,"status":"NOT_FOUND","message":"Requested entity was not found."}}`,
			false,
		},
		"malformed message": {
			http.StatusBadRequest,
			`{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"Invalid value at 'message.android.priority'"}}`,
			false,
		},
		"server down": {http.StatusServiceUnavailable, `{}`, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fcm, google := newTestFCM(t)
			google.sendStatus, google.sendBody = tc.status, tc.body

			err := fcm.Send(context.Background(), "t", Message{Title: "x"})
			if err == nil {
				t.Fatal("expected an error")
			}
			if got := errors.Is(err, ErrUnregistered); got != tc.dead {
				t.Fatalf("unregistered = %v, want %v (%v)", got, tc.dead, err)
			}
		})
	}
}

func TestARefusedTokenRequestIsAnError(t *testing.T) {
	key := testKey(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"Invalid JWT Signature."}`)
	}))
	t.Cleanup(srv.Close)

	creds, err := ParseServiceAccount(serviceAccountJSON(t, key, map[string]any{"token_uri": srv.URL}))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	fcm := NewFCM(creds)
	fcm.endpoint = srv.URL

	err = fcm.Send(context.Background(), "t", Message{Title: "x"})
	if err == nil || !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("err = %v", err)
	}
	if errors.Is(err, ErrUnregistered) {
		t.Fatal("a credential problem is not a dead token")
	}
}

// The form the token request is sent as, so a regression in encoding shows here and not
// as an opaque 400 from Google.
func TestTheAssertionTravelsAsAForm(t *testing.T) {
	values := url.Values{"grant_type": {"a"}, "assertion": {"b.c.d"}}
	if got := values.Encode(); got != "assertion=b.c.d&grant_type=a" {
		t.Fatalf("encoded = %q", got)
	}
}
