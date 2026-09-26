package config

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
)

// serviceAccountFile is the key file the Firebase console downloads, with a key made up
// for the test.
func serviceAccountFile(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	raw, err := json.Marshal(map[string]string{
		"type":           "service_account",
		"project_id":     "meu-auto-test",
		"private_key_id": "key-1",
		"private_key":    string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"client_email":   "firebase-adminsdk@meu-auto-test.iam.gserviceaccount.com",
		"token_uri":      "https://oauth2.googleapis.com/token",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

// Nobody needs a Firebase account to run the project.
func TestPushIsOptional(t *testing.T) {
	setProductionEnv(t)
	t.Setenv("FCM_SERVICE_ACCOUNT", "")
	t.Setenv("NOTIFICATIONS_DEBUG", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.FCMAccount != nil || cfg.NotificationsDebug {
		t.Errorf("push = %v, debug %v; want off", cfg.FCMAccount, cfg.NotificationsDebug)
	}
}

func TestPushAccountIsReadFromBase64(t *testing.T) {
	setProductionEnv(t)
	t.Setenv("FCM_SERVICE_ACCOUNT", base64.StdEncoding.EncodeToString(serviceAccountFile(t)))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.FCMAccount == nil || cfg.FCMAccount.ProjectID != "meu-auto-test" || cfg.FCMAccount.PrivateKey == nil {
		t.Fatalf("FCMAccount = %+v", cfg.FCMAccount)
	}
}

// Pasting the file as it came is the obvious mistake, and nothing is gained by refusing it.
func TestPushAccountAcceptsTheRawFile(t *testing.T) {
	setProductionEnv(t)
	t.Setenv("FCM_SERVICE_ACCOUNT", string(serviceAccountFile(t)))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.FCMAccount == nil {
		t.Fatal("FCMAccount = nil")
	}
}

// A key that cannot be used refuses the deploy — Railway keeps the previous one serving —
// and the message says why without repeating the secret.
func TestPushAccountThatCannotBeReadRefusesToBoot(t *testing.T) {
	file := serviceAccountFile(t)
	var broken map[string]string
	if err := json.Unmarshal(file, &broken); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	broken["private_key"] = "not a key"
	brokenRaw, _ := json.Marshal(broken)

	cases := map[string]struct {
		value string
		want  string
	}{
		"not base64":  {"%%% definitely not base64 %%%", "base64-encoded"},
		"not json":    {base64.StdEncoding.EncodeToString([]byte("hello")), "not the JSON key file"},
		"broken key":  {base64.StdEncoding.EncodeToString(brokenRaw), "private_key that is not PEM"},
		"cut in half": {string(file[:len(file)/2]), "not the JSON key file"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			setProductionEnv(t)
			t.Setenv("FCM_SERVICE_ACCOUNT", tc.value)

			_, err := Load()
			if err == nil {
				t.Fatal("Load() error = nil, want a problem")
			}
			if !strings.Contains(err.Error(), "FCM_SERVICE_ACCOUNT") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not name the problem %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "PRIVATE KEY") {
				t.Fatalf("error repeats the key: %q", err)
			}
		})
	}
}

func TestNotificationsDebugIsAFlag(t *testing.T) {
	setValidEnv(t)
	t.Setenv("NOTIFICATIONS_DEBUG", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if !cfg.NotificationsDebug {
		t.Error("NotificationsDebug = false, want true")
	}
}
