// Command api runs the Meu Auto HTTP API.
//
// Boot order is deliberate: configuration is validated before anything else, so a
// misconfigured deployment dies immediately with a readable message instead of accepting
// traffic it cannot serve.
//
// The object graph itself lives in internal/app, so that the integration suite exercises
// the same wiring this command serves rather than a copy of it.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	// Embeds the zoneinfo database in the binary. The distroless runtime image has no
	// system timezone files, and America/Sao_Paulo is load-bearing for every "what is
	// today" decision in the domain.
	_ "time/tzdata"

	"github.com/simonscabello/meu-auto-backend/db"
	"github.com/simonscabello/meu-auto-backend/internal/app"
	"github.com/simonscabello/meu-auto-backend/internal/platform/config"
	"github.com/simonscabello/meu-auto-backend/internal/platform/database"
	"github.com/simonscabello/meu-auto-backend/internal/platform/logging"
	"github.com/simonscabello/meu-auto-backend/internal/platform/mailer"
	"github.com/simonscabello/meu-auto-backend/internal/platform/push"
	"github.com/simonscabello/meu-auto-backend/internal/platform/storage"
)

const (
	// Long enough for an in-flight request to finish, short enough to stay inside the
	// window a platform allows between SIGTERM and SIGKILL.
	shutdownTimeout = 15 * time.Second

	readHeaderTimeout = 10 * time.Second
	readTimeout       = 30 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 120 * time.Second
)

func main() {
	if err := run(); err != nil {
		// The logger may not exist yet — a config failure happens before it is built.
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := logging.New(cfg.LogLevel, cfg.AppEnv)
	slog.SetDefault(log)

	pool, err := database.NewPool(context.Background(), cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := database.Migrate(cfg.DatabaseURL, db.Migrations, log); err != nil {
		return err
	}

	// The zoneinfo database is embedded via the blank time/tzdata import above: the distroless
	// runtime image carries no system timezone files, so without it this call fails in
	// production and succeeds on every developer machine.
	location, err := time.LoadLocation(config.TimeZone)
	if err != nil {
		return fmt.Errorf("load timezone %s: %w", config.TimeZone, err)
	}

	var mail mailer.Mailer
	if cfg.ResendAPIKey != "" {
		mail = mailer.NewResend(cfg.ResendAPIKey, cfg.MailFrom)
	} else {
		// config.Load only allows this in development.
		log.Warn("RESEND_API_KEY is not set: password reset links will be written to the log")
		mail = mailer.LogMailer{Log: log}
	}

	var photos storage.Store
	if cfg.HasBucket() {
		bucket := storage.NewS3(storage.S3Config{
			Endpoint:        cfg.BucketEndpoint,
			Bucket:          cfg.BucketName,
			AccessKeyID:     cfg.BucketAccessKeyID,
			SecretAccessKey: cfg.BucketSecretAccessKey,
			Region:          cfg.BucketRegion,
			PathStyle:       cfg.BucketPathStyle,
		})
		// In the background and only logged: the answer is for whoever reads the deploy
		// log, and a slow bucket must not delay the API coming up.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := bucket.Ping(ctx); err != nil {
				log.Error("photo bucket unreachable: uploads will fail", slog.Any("error", err))
				return
			}
			log.Info("photo bucket reachable")
		}()
		photos = bucket
	} else {
		// config.Load only allows this in development.
		log.Warn("BUCKET_* is not set: profile photos are kept in memory and lost on restart")
		photos = storage.NewMemory()
	}

	// Reminders are optional, like the catalogue token: without a key the API works exactly
	// as before and nothing is recorded as sent. The debug switch wins over a key, so the
	// text can be checked against real data without anybody's phone buzzing.
	var sender push.Sender
	switch {
	case cfg.NotificationsDebug:
		log.Warn("NOTIFICATIONS_DEBUG is on: push reminders are written to the log, not sent")
		sender = push.Log{Logger: log}
	case cfg.FCMAccount != nil:
		log.Info("push reminders on", slog.String("firebase_project", cfg.FCMAccount.ProjectID))
		sender = push.NewFCM(*cfg.FCMAccount)
	default:
		log.Info("FCM_SERVICE_ACCOUNT is not set: push reminders are off")
	}

	application := app.New(cfg, app.Deps{
		Pool:     pool,
		Mailer:   mail,
		Location: location,
		Log:      log,
		Photos:   photos,
		Push:     sender,
	})

	server := &http.Server{
		Addr:    net.JoinHostPort("", cfg.Port),
		Handler: application.Handler,

		// Without these a single slow or malicious client can hold a connection open
		// indefinitely. Go applies no timeouts by default.
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	// Buffered so the goroutine can report and exit even if nobody is selecting yet.
	serverErr := make(chan error, 1)
	go func() {
		// The logger already carries env; repeating it here would emit a duplicate key.
		log.Info("server listening", slog.String("addr", server.Addr))

		if err := server.ListenAndServe(); err != nil &&
			!errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	// Railway sends SIGTERM on every redeploy, and so does `docker stop`. Draining in
	// flight requests is the difference between a clean deploy and a handful of users
	// seeing a failed save.
	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The morning job lives as long as the process and stops with the same signal. A
	// reminder half-way through a run when the signal comes is recorded or not, and either
	// way it is never sent twice (notification_log).
	if application.Reminders != nil {
		go application.Reminders.Start(ctx)
	}

	select {
	case err := <-serverErr:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
		log.Info("shutdown signal received, draining connections")
	}

	// A fresh context: the one above is already cancelled by the signal.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}

	log.Info("server stopped")
	return nil
}
