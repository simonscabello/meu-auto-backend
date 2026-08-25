package insight

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/simonscabello/meu-auto-backend/internal/platform/apperr"
	"github.com/simonscabello/meu-auto-backend/internal/platform/auth"
	"github.com/simonscabello/meu-auto-backend/internal/platform/httpx"
)

// Handler exposes the three read-model endpoints that close MVP-1.
type Handler struct {
	service *Service
	tokens  *auth.TokenService
}

func NewHandler(service *Service, tokens *auth.TokenService) *Handler {
	return &Handler{service: service, tokens: tokens}
}

// Mount registers the module's routes under the caller's prefix (/v1).
//
// Flat patterns, not nested chi.Route: four modules now hang endpoints off
// /vehicles/{vehicleID}, and overlapping subrouters make chi panic at startup.
func (h *Handler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(auth.Middleware(h.tokens))

		r.Get("/vehicles/{vehicleID}/dashboard", h.dashboard)
		r.Get("/vehicles/{vehicleID}/alerts", h.alerts)
		r.Get("/vehicles/{vehicleID}/timeline", h.timeline)
	})
}

func (h *Handler) dashboard(w http.ResponseWriter, r *http.Request) {
	userID, vehicleID, err := callerAndVehicle(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	costMonths := httpx.QueryInt32(r, "cost_months", defaultCostMonths, 1, maxCostMonths)

	dashboard, err := h.service.Dashboard(r.Context(), userID, vehicleID, costMonths)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, dashboard)
}

func (h *Handler) alerts(w http.ResponseWriter, r *http.Request) {
	userID, vehicleID, err := callerAndVehicle(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	alerts, err := h.service.Alerts(r.Context(), userID, vehicleID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"data": alerts})
}

func (h *Handler) timeline(w http.ResponseWriter, r *http.Request) {
	userID, vehicleID, err := callerAndVehicle(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	pageSize := httpx.QueryInt32(r, "limit", defaultPageSize, 1, maxPageSize)

	entries, nextCursor, err := h.service.Timeline(
		r.Context(), userID, vehicleID, pageSize, r.URL.Query().Get("cursor"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	out := make([]timelineEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, toTimelineEntry(entry))
	}
	httpx.JSON(w, r, http.StatusOK, timelinePage{Data: out, NextCursor: nextCursor})
}

func callerAndVehicle(r *http.Request) (userID, vehicleID uuid.UUID, err error) {
	userID, ok := auth.UserID(r.Context())
	if !ok {
		return uuid.Nil, uuid.Nil, apperr.Unauthorized("Autenticação necessária.")
	}
	if vehicleID, err = httpx.PathUUID(r, "vehicleID"); err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	return userID, vehicleID, nil
}
