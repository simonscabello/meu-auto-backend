package main

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/simonscabello/meu-auto-backend/internal/identity"
	"github.com/simonscabello/meu-auto-backend/internal/insight"
	"github.com/simonscabello/meu-auto-backend/internal/maintenance"
	"github.com/simonscabello/meu-auto-backend/internal/obligation"
	"github.com/simonscabello/meu-auto-backend/internal/platform/apperr"
	"github.com/simonscabello/meu-auto-backend/internal/platform/config"
	"github.com/simonscabello/meu-auto-backend/internal/platform/httpx"
	"github.com/simonscabello/meu-auto-backend/internal/vehicle"
)

// readinessTimeout bounds the database check so a hung database produces a failed
// readiness probe instead of a hung one.
const readinessTimeout = 2 * time.Second

// newRouter assembles the HTTP surface.
//
// Domain modules mount themselves under /v1. The version prefix is present from the very
// first endpoint: a shipped app cannot be force-updated, so there is no later opportunity
// to add one (SPEC.md D-01).
func newRouter(
	cfg config.Config,
	pool *pgxpool.Pool,
	identityHandler *identity.Handler,
	vehicleHandler *vehicle.Handler,
	maintenanceHandler *maintenance.Handler,
	obligationHandler *obligation.Handler,
	insightHandler *insight.Handler,
) http.Handler {
	r := chi.NewRouter()

	// Order matters: the request id must exist before anything logs, and the recoverer
	// must be inside the logger so a panicking request is still reported.
	r.Use(httpx.RequestIDMiddleware)
	r.Use(httpx.LoggerMiddleware)
	r.Use(httpx.RecovererMiddleware)
	r.Use(httpx.CORSMiddleware(cfg.CORSOrigins))

	// chi's defaults answer with plain text and an empty body. The app parses every
	// failure as the standard error envelope, so a typo'd path must not be the one
	// response shaped differently from all the others.
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, r, apperr.NotFound("Recurso não encontrado."))
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, r, apperr.New(apperr.CodeMethodNotAllowed,
			"Método não permitido para este recurso."))
	})

	// Liveness: the process is running. Deliberately touches nothing else — a database
	// outage must not make the platform kill and restart a healthy process.
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, r, http.StatusOK, map[string]string{"status": "ok"})
	})

	// Readiness: the process can actually serve traffic.
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
		defer cancel()

		if err := pool.Ping(ctx); err != nil {
			httpx.JSON(w, r, http.StatusServiceUnavailable, map[string]string{
				"status": "unavailable",
				"reason": "database",
			})
			return
		}
		httpx.JSON(w, r, http.StatusOK, map[string]string{"status": "ready"})
	})

	r.Route("/v1", func(r chi.Router) {
		identityHandler.Mount(r)
		vehicleHandler.Mount(r)
		maintenanceHandler.Mount(r)
		obligationHandler.Mount(r)
		insightHandler.Mount(r)
	})

	return r
}
