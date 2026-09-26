package config

import (
	"log/slog"
	"strings"
	"testing"
)

func setValidEnv(t *testing.T) {
	t.Helper()
	t.Setenv("APP_ENV", EnvDevelopment)
	t.Setenv("PORT", "8080")
	t.Setenv("DATABASE_URL", "postgres://u:p@localhost:5432/db?sslmode=disable")
	t.Setenv("JWT_SECRET", strings.Repeat("x", minJWTSecretLength))
	t.Setenv("LOG_LEVEL", "info")
	t.Setenv("CORS_ORIGINS", "*")
}

func TestLoadValid(t *testing.T) {
	setValidEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.Port != "8080" {
		t.Errorf("Port = %q, want 8080", cfg.Port)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want info", cfg.LogLevel)
	}
	if len(cfg.CORSOrigins) != 1 || cfg.CORSOrigins[0] != "*" {
		t.Errorf("CORSOrigins = %v, want [*]", cfg.CORSOrigins)
	}
}

// A broken deployment must surface every problem at once. Reporting them one per restart
// turns a two-minute fix into a twenty-minute one.
func TestLoadReportsEveryProblemAtOnce(t *testing.T) {
	t.Setenv("APP_ENV", "staging")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("JWT_SECRET", "")
	t.Setenv("LOG_LEVEL", "verbose")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want a validation error")
	}

	for _, want := range []string{"APP_ENV", "DATABASE_URL", "JWT_SECRET", "LOG_LEVEL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %s:\n%v", want, err)
		}
	}
}

func TestLoadRejectsShortJWTSecret(t *testing.T) {
	setValidEnv(t)
	t.Setenv("JWT_SECRET", strings.Repeat("x", minJWTSecretLength-1))

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a short JWT_SECRET")
	}
}

// A wildcard origin in production lets any site drive the API with a stolen token.
func TestLoadRejectsWildcardCORSInProduction(t *testing.T) {
	setValidEnv(t)
	t.Setenv("APP_ENV", EnvProduction)
	t.Setenv("CORS_ORIGINS", "https://app.meuauto.com.br, *")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() accepted a wildcard CORS origin in production")
	}
	if !strings.Contains(err.Error(), "CORS_ORIGINS") {
		t.Errorf("error does not mention CORS_ORIGINS:\n%v", err)
	}
}

func TestLoadAppliesDefaults(t *testing.T) {
	t.Setenv("APP_ENV", "")
	t.Setenv("PORT", "")
	t.Setenv("LOG_LEVEL", "")
	t.Setenv("CORS_ORIGINS", "")
	t.Setenv("DATABASE_URL", "postgres://u:p@localhost:5432/db")
	t.Setenv("JWT_SECRET", strings.Repeat("x", minJWTSecretLength))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.AppEnv != EnvDevelopment {
		t.Errorf("AppEnv = %q, want %q", cfg.AppEnv, EnvDevelopment)
	}
	if cfg.Port != "8080" {
		t.Errorf("Port = %q, want 8080", cfg.Port)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want info", cfg.LogLevel)
	}
}

// Production defaults to no browser origin at all. The only client is a mobile app, which
// sends no Origin header — listing a domain nobody browses from would be noise pretending
// to be security.
func TestProductionDefaultsToNoCORSOrigin(t *testing.T) {
	setValidEnv(t)
	t.Setenv("APP_ENV", EnvProduction)
	t.Setenv("CORS_ORIGINS", "")
	t.Setenv("RESEND_API_KEY", "re_test")
	t.Setenv("MAIL_FROM", "Meu Auto <nao-responda@meuauto.com.br>")
	setBucketEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if len(cfg.CORSOrigins) != 0 {
		t.Errorf("CORSOrigins = %v, want empty in production", cfg.CORSOrigins)
	}
}

func TestDevelopmentDefaultsToWildcardCORS(t *testing.T) {
	setValidEnv(t)
	t.Setenv("CORS_ORIGINS", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if len(cfg.CORSOrigins) != 1 || cfg.CORSOrigins[0] != "*" {
		t.Errorf("CORSOrigins = %v, want [*] in development", cfg.CORSOrigins)
	}
}

// Production must refuse to start without a real mail provider: the fallback writes the
// password reset link to the log, which is account takeover for anyone who can read logs.
func TestProductionRequiresMailProvider(t *testing.T) {
	setValidEnv(t)
	t.Setenv("APP_ENV", EnvProduction)
	t.Setenv("CORS_ORIGINS", "")
	t.Setenv("RESEND_API_KEY", "")
	t.Setenv("MAIL_FROM", "")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() accepted production without a mail provider")
	}
	for _, want := range []string{"RESEND_API_KEY", "MAIL_FROM"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %s:\n%v", want, err)
		}
	}
}

func setBucketEnv(t *testing.T) {
	t.Helper()
	t.Setenv("BUCKET_ENDPOINT", "https://t3.storageapi.dev")
	t.Setenv("BUCKET_NAME", "meu-auto-fotos-abc123")
	t.Setenv("BUCKET_ACCESS_KEY_ID", "tid_test")
	t.Setenv("BUCKET_SECRET_ACCESS_KEY", "tsec_test")
}

// Production must refuse to start without a bucket: photos kept in memory would vanish on
// the next deploy while the rows still named them.
func TestProductionRequiresBucket(t *testing.T) {
	setValidEnv(t)
	t.Setenv("APP_ENV", EnvProduction)
	t.Setenv("CORS_ORIGINS", "")
	t.Setenv("RESEND_API_KEY", "re_test")
	t.Setenv("MAIL_FROM", "Meu Auto <nao-responda@meuauto.com.br>")
	t.Setenv("BUCKET_ENDPOINT", "")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "BUCKET_ENDPOINT") {
		t.Fatalf("Load() = %v, want an error naming the bucket variables", err)
	}
}

// Development runs without a bucket; photos then live in memory.
func TestDevelopmentRunsWithoutBucket(t *testing.T) {
	setValidEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.HasBucket() {
		t.Error("HasBucket() = true with no bucket variables set")
	}
	if cfg.BucketRegion != "auto" {
		t.Errorf("BucketRegion = %q, want auto", cfg.BucketRegion)
	}
}

func setProductionEnv(t *testing.T) {
	t.Helper()
	setValidEnv(t)
	setBucketEnv(t)
	t.Setenv("APP_ENV", EnvProduction)
	t.Setenv("CORS_ORIGINS", "")
	t.Setenv("RESEND_API_KEY", "re_test")
	t.Setenv("MAIL_FROM", "Meu Auto <nao-responda@meuauto.com.br>")
}

const testAPKURL = "https://github.com/simonscabello/meu-auto-app/releases/latest/download/meu-auto.apk"

// Nothing announced is the normal state, and the one a new deploy starts in.
func TestAppReleaseIsOptional(t *testing.T) {
	setValidEnv(t)
	t.Setenv("APP_LATEST_VERSION", "")
	t.Setenv("APP_APK_URL", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.AppLatestVersion != "" || cfg.AppAPKURL != "" {
		t.Errorf("app release = %q, %q, want both empty", cfg.AppLatestVersion, cfg.AppAPKURL)
	}
}

// The link is set once and outlives every release; the version follows each one.
func TestAppReleaseLinkWithoutVersion(t *testing.T) {
	setProductionEnv(t)
	t.Setenv("APP_LATEST_VERSION", "")
	t.Setenv("APP_APK_URL", testAPKURL)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.AppAPKURL != testAPKURL {
		t.Errorf("AppAPKURL = %q, want %q", cfg.AppAPKURL, testAPKURL)
	}
}

// The release is tagged "v1.2.0" and that is what gets pasted into the panel.
func TestAppReleaseVersionIsNormalised(t *testing.T) {
	for raw, want := range map[string]string{
		"1.2.0":      "1.2.0",
		"1.2.0+7":    "1.2.0+7",
		"v1.2.0":     "1.2.0",
		" v1.2.0+7 ": "1.2.0+7",
	} {
		t.Run(raw, func(t *testing.T) {
			setProductionEnv(t)
			t.Setenv("APP_LATEST_VERSION", raw)
			t.Setenv("APP_APK_URL", testAPKURL)

			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load() error = %v, want nil", err)
			}
			if cfg.AppLatestVersion != want {
				t.Errorf("AppLatestVersion = %q, want %q", cfg.AppLatestVersion, want)
			}
		})
	}
}

// Every one of these would reach the phones as a notice nobody can act on, or as none at
// all — so the deploy stops instead, and the previous one keeps serving.
func TestAppReleaseRejectsWhatThePhoneCannotUse(t *testing.T) {
	cases := []struct {
		name, version, url, want string
	}{
		{"version the app cannot compare", "1.2", testAPKURL, "APP_LATEST_VERSION"},
		{"words for a version", "latest", testAPKURL, "APP_LATEST_VERSION"},
		{"version with nowhere to download", "1.2.0", "", "APP_APK_URL is required"},
		{"link that is not a link", "", "meu-auto.apk", "APP_APK_URL"},
		{"installer over plain http", "1.2.0", "http://example.com/meu-auto.apk", "https"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setProductionEnv(t)
			t.Setenv("APP_LATEST_VERSION", tc.version)
			t.Setenv("APP_APK_URL", tc.url)

			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load() = %v, want an error mentioning %q", err, tc.want)
			}
		})
	}
}

// Development may serve the file from the machine itself, which has no certificate.
func TestAppReleaseAllowsHTTPInDevelopment(t *testing.T) {
	setValidEnv(t)
	t.Setenv("APP_LATEST_VERSION", "1.2.0")
	t.Setenv("APP_APK_URL", "http://10.0.2.2:8000/meu-auto.apk")

	if _, err := Load(); err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
}
