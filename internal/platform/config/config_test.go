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
