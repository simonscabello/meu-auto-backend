// Package config loads and validates process configuration from the environment.
//
// Every value the process needs is read once, at boot, and validated together so a
// misconfigured deployment fails immediately with the full list of problems instead of
// panicking on the first request that happens to need a missing value (SPEC.md D-06).
//
// Nothing here is specific to a hosting provider. Railway injects PORT and DATABASE_URL
// natively; a VPS supplies the same names through the environment. Moving between them
// is a change of variables, never a change of code.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
)

const (
	EnvDevelopment = "development"
	EnvProduction  = "production"

	// A shorter secret makes HS256 brute-forceable within reach of a motivated attacker.
	minJWTSecretLength = 32
)

// Config holds every setting the process needs. It is immutable after Load.
type Config struct {
	AppEnv      string
	Port        string
	DatabaseURL string
	JWTSecret   string
	LogLevel    slog.Level
	CORSOrigins []string

	// Transactional e-mail. Empty in development, where LogMailer stands in.
	ResendAPIKey string
	MailFrom     string

	// Deep link the password reset e-mail points at. The token is appended as ?token=.
	PasswordResetURL string

	// TrustProxy makes rate limiting read X-Forwarded-For. Set it only when a proxy is
	// actually in front (Railway, Caddy, nginx) — see httpx.ClientIP.
	TrustProxy bool

	// The FIPE provider behind the vehicle catalogue.
	//
	// The URL has a working default, so nothing has to be set for the catalogue to
	// function; the variable exists so the integration suite can point it at a test server
	// and so a self-hosted mirror is a config change rather than a deploy.
	//
	// The token is optional — without it the provider allows 500 requests a day, with a
	// free one 1000. It NEVER leaves this process: it travels to the provider as a header,
	// is never rendered into a URL, never logged, and never appears in any response.
	FipeAPIURL   string
	FipeAPIToken string
}

// TimeZone is the zone every civil-date decision is made in. The product is
// Brazil-only by design (PRODUCT.md), so this is a constant, not a per-user setting.
const TimeZone = "America/Sao_Paulo"

// JWTIssuer is the "iss" claim on every access token. It is a constant rather than a
// setting: changing it invalidates every token already in the wild, which is a decision,
// not a knob to turn per environment.
const JWTIssuer = "meu-auto"

// IsProduction reports whether the process is running in the production environment.
func (c Config) IsProduction() bool { return c.AppEnv == EnvProduction }

// Load reads configuration from the environment and validates it.
//
// It accumulates every problem before returning so a broken deployment surfaces all of
// its misconfiguration at once, rather than one variable per restart.
func Load() (Config, error) {
	var problems []string

	cfg := Config{
		AppEnv:      envOr("APP_ENV", EnvDevelopment),
		Port:        envOr("PORT", "8080"),
		DatabaseURL: os.Getenv("DATABASE_URL"),
		JWTSecret:   os.Getenv("JWT_SECRET"),
		CORSOrigins: splitAndTrim(envOr("CORS_ORIGINS", corsDefaultFor(envOr("APP_ENV", EnvDevelopment)))),

		ResendAPIKey:     os.Getenv("RESEND_API_KEY"),
		MailFrom:         os.Getenv("MAIL_FROM"),
		PasswordResetURL: envOr("PASSWORD_RESET_URL", "meuauto://redefinir-senha"),
		TrustProxy:       strings.EqualFold(envOr("TRUST_PROXY", "false"), "true"),

		// No default spelled out here, on purpose: the provider's public URL is the
		// catalogue's business, and repeating it in this package would put one constant in
		// two files that must never disagree. Empty means "whatever the client considers
		// its default" — see fipe.New.
		FipeAPIURL:   strings.TrimSpace(os.Getenv("FIPE_API_URL")),
		FipeAPIToken: strings.TrimSpace(os.Getenv("FIPE_API_TOKEN")),
	}

	switch cfg.AppEnv {
	case EnvDevelopment, EnvProduction:
	default:
		problems = append(problems, fmt.Sprintf(
			"APP_ENV must be %q or %q, got %q", EnvDevelopment, EnvProduction, cfg.AppEnv))
	}

	if cfg.DatabaseURL == "" {
		problems = append(problems, "DATABASE_URL is required")
	}

	switch {
	case cfg.JWTSecret == "":
		problems = append(problems, "JWT_SECRET is required")
	case len(cfg.JWTSecret) < minJWTSecretLength:
		problems = append(problems, fmt.Sprintf(
			"JWT_SECRET must be at least %d characters, got %d",
			minJWTSecretLength, len(cfg.JWTSecret)))
	}

	level, err := parseLogLevel(envOr("LOG_LEVEL", "info"))
	if err != nil {
		problems = append(problems, err.Error())
	}
	cfg.LogLevel = level

	// A wildcard origin in production would let any site drive the API with a stolen
	// token. Catch it at boot rather than in a pen test.
	//
	// An EMPTY list is valid and is the correct production setting here: the only client
	// is a mobile app, which sends no Origin header and is not subject to CORS at all.
	// Listing a domain nobody browses from would be noise pretending to be security.
	if cfg.IsProduction() && containsWildcard(cfg.CORSOrigins) {
		problems = append(problems, `CORS_ORIGINS must not be "*" in production`)
	}

	// Without a real provider the fallback mailer writes the reset link to the log, and a
	// password reset link in production logs is account takeover for anyone who can read
	// them. Refuse to start rather than degrade silently.
	if cfg.IsProduction() {
		if cfg.ResendAPIKey == "" {
			problems = append(problems,
				"RESEND_API_KEY is required in production (password reset sends real e-mail)")
		}
		if cfg.MailFrom == "" {
			problems = append(problems, "MAIL_FROM is required in production")
		}
	}

	if len(problems) > 0 {
		return Config{}, fmt.Errorf("invalid configuration:\n  - %s",
			strings.Join(problems, "\n  - "))
	}
	return cfg, nil
}

// corsDefaultFor picks the default browser origin policy.
//
// Development gets "*" so a browser-based tool can poke the API. Production gets nothing:
// the client is a mobile app, so the safe default is to emit no CORS headers at all, and
// an operator who genuinely needs a browser origin has to name it explicitly.
func corsDefaultFor(appEnv string) string {
	if appEnv == EnvProduction {
		return ""
	}
	return "*"
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func splitAndTrim(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func containsWildcard(origins []string) bool {
	for _, o := range origins {
		if o == "*" {
			return true
		}
	}
	return false
}

func parseLogLevel(raw string) (slog.Level, error) {
	switch strings.ToLower(raw) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, errors.New(
			`LOG_LEVEL must be one of "debug", "info", "warn", "error", got ` + raw)
	}
}
