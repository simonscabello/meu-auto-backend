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
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/simonscabello/meu-auto-backend/internal/platform/push"
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

	// The S3-compatible bucket profile photos live in (a Railway Bucket in production).
	// Required in production; in development, leaving all of them empty keeps photos in
	// memory, which is enough to exercise the flow and loses them on restart.
	BucketEndpoint        string
	BucketName            string
	BucketAccessKeyID     string
	BucketSecretAccessKey string
	BucketRegion          string
	BucketPathStyle       bool

	// The Android build the app is told it can update to (GET /v1/app-version).
	//
	// The APK is published as a GitHub Release, not through a store, so nothing outside the
	// app can tell an owner that a new version exists. Both are optional, and the version is
	// the switch: publishing a release and asking every phone to install it are separate
	// decisions, and until APP_LATEST_VERSION names one, nobody is asked.
	AppLatestVersion string
	AppAPKURL        string

	// Push reminders through Firebase Cloud Messaging (SPEC.md D-19).
	//
	// FCMAccount is FCM_SERVICE_ACCOUNT parsed at boot — the key file the Firebase console
	// downloads, base64-encoded the way it is pasted into Railway. Nil when unset, and then
	// no reminder is sent and none is recorded as sent: the API, and a developer machine,
	// work exactly as before. A key that cannot be read refuses the deploy instead, and
	// Railway keeps the previous one serving.
	FCMAccount *push.Credentials

	// NotificationsDebug writes each reminder to the log instead of sending it, so the text
	// and the recipients can be checked with no Firebase account at all.
	NotificationsDebug bool

	// RemindersHour is the hour, in America/Sao_Paulo, the reminders go out: 9 unless
	// REMINDERS_HOUR says otherwise. The variable exists to try the whole path on a real
	// phone now rather than tomorrow morning — set it to the current hour, see the push
	// arrive, delete it. When reminders go out for good is a product decision, not a knob.
	RemindersHour int
}

// appVersionPattern is the pubspec's `version:` — three numbers and, optionally, the build
// number after "+". It is exactly what the app compares with its own.
var appVersionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+(\+\d+)?$`)

// HasBucket reports whether object storage is configured.
func (c Config) HasBucket() bool {
	return c.BucketEndpoint != "" && c.BucketName != "" &&
		c.BucketAccessKeyID != "" && c.BucketSecretAccessKey != ""
}

// TimeZone is the zone every civil-date decision is made in. The product is
// Brazil-only by design (PRODUCT.md), so this is a constant, not a per-user setting.
const TimeZone = "America/Sao_Paulo"

// defaultRemindersHour: the morning, with the day ahead to do something about the
// reminder. SPEC.md D-19.
const defaultRemindersHour = 9

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

		BucketEndpoint:        strings.TrimSpace(os.Getenv("BUCKET_ENDPOINT")),
		BucketName:            strings.TrimSpace(os.Getenv("BUCKET_NAME")),
		BucketAccessKeyID:     strings.TrimSpace(os.Getenv("BUCKET_ACCESS_KEY_ID")),
		BucketSecretAccessKey: strings.TrimSpace(os.Getenv("BUCKET_SECRET_ACCESS_KEY")),
		BucketRegion:          strings.TrimSpace(envOr("BUCKET_REGION", "auto")),
		BucketPathStyle:       strings.EqualFold(envOr("BUCKET_PATH_STYLE", "false"), "true"),

		// The release is tagged "v1.2.0", and that is what gets pasted into the panel. The
		// "v" is the tag's, not the version's, so it is dropped rather than refused.
		AppLatestVersion: strings.TrimPrefix(strings.TrimSpace(os.Getenv("APP_LATEST_VERSION")), "v"),
		AppAPKURL:        strings.TrimSpace(os.Getenv("APP_APK_URL")),

		NotificationsDebug: strings.EqualFold(envOr("NOTIFICATIONS_DEBUG", "false"), "true"),
		RemindersHour:      defaultRemindersHour,
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
		// Photos kept in memory would vanish on every deploy while the rows still named
		// them. Refuse to start rather than lose uploads quietly.
		if !cfg.HasBucket() {
			problems = append(problems, "BUCKET_ENDPOINT, BUCKET_NAME, BUCKET_ACCESS_KEY_ID "+
				"and BUCKET_SECRET_ACCESS_KEY are required in production (profile photos)")
		}
	}

	// A version the app cannot compare would be ignored on every phone without a word, and a
	// version with nowhere to download it asks people to do something they cannot. Railway
	// only routes to a deploy that boots, so refusing here keeps the previous one serving.
	if cfg.AppLatestVersion != "" {
		if !appVersionPattern.MatchString(cfg.AppLatestVersion) {
			problems = append(problems, fmt.Sprintf(
				`APP_LATEST_VERSION must look like "1.2.0" or "1.2.0+7", got %q`,
				cfg.AppLatestVersion))
		}
		if cfg.AppAPKURL == "" {
			problems = append(problems, "APP_APK_URL is required when APP_LATEST_VERSION is set")
		}
	}
	if cfg.AppAPKURL != "" {
		if problem := checkAPKURL(cfg.AppAPKURL, cfg.IsProduction()); problem != "" {
			problems = append(problems, problem)
		}
	}

	if raw := strings.TrimSpace(os.Getenv("REMINDERS_HOUR")); raw != "" {
		hour, err := strconv.Atoi(raw)
		if err != nil || hour < 0 || hour > 23 {
			problems = append(problems, fmt.Sprintf(
				"REMINDERS_HOUR must be an hour from 0 to 23, got %q", raw))
		} else {
			cfg.RemindersHour = hour
		}
	}

	if raw := strings.TrimSpace(os.Getenv("FCM_SERVICE_ACCOUNT")); raw != "" {
		account, problem := parseFCMAccount(raw)
		if problem != "" {
			problems = append(problems, problem)
		} else {
			cfg.FCMAccount = &account
		}
	}

	if len(problems) > 0 {
		return Config{}, fmt.Errorf("invalid configuration:\n  - %s",
			strings.Join(problems, "\n  - "))
	}
	return cfg, nil
}

// checkAPKURL returns what is wrong with the download link, or "" when nothing is.
//
// Plain http is allowed outside production, so the whole flow can be tried against a file
// served from the development machine; a phone must never be sent to fetch an installer
// over a connection anyone on the network can rewrite.
func checkAPKURL(raw string, production bool) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Sprintf("APP_APK_URL must be an absolute URL, got %q", raw)
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && !production:
	default:
		return fmt.Sprintf("APP_APK_URL must use https, got %q", raw)
	}
	return ""
}

// parseFCMAccount reads FCM_SERVICE_ACCOUNT, returning what is wrong with it or "".
//
// Base64 is what DEPLOY.md tells the owner to paste, because a JSON file full of quotes
// and newlines does not survive every panel. The raw JSON is accepted too: pasting the
// file as it came is the obvious mistake, and nothing is gained by refusing it. The
// problem never repeats the value — it ends up in a deploy log.
func parseFCMAccount(raw string) (push.Credentials, string) {
	decoded := []byte(raw)
	if !strings.HasPrefix(raw, "{") {
		var err error
		decoded, err = base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return push.Credentials{}, "FCM_SERVICE_ACCOUNT must be the service account JSON, base64-encoded"
		}
	}
	account, err := push.ParseServiceAccount(decoded)
	if err != nil {
		return push.Credentials{}, "FCM_SERVICE_ACCOUNT " + err.Error()
	}
	return account, ""
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
