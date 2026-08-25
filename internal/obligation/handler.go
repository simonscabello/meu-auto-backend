package obligation

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/meu-auto/meu-auto-backend/internal/platform/apperr"
	"github.com/meu-auto/meu-auto-backend/internal/platform/auth"
	"github.com/meu-auto/meu-auto-backend/internal/platform/httpx"
)

// Handler exposes the IPVA, licenciamento and seguro endpoints.
type Handler struct {
	service *Service
	tokens  *auth.TokenService
}

func NewHandler(service *Service, tokens *auth.TokenService) *Handler {
	return &Handler{service: service, tokens: tokens}
}

// Mount registers the module's routes under the caller's prefix (/v1).
//
// Flat patterns, not nested chi.Route: three modules now hang endpoints off
// /vehicles/{vehicleID}, and overlapping subrouters make chi panic at startup.
func (h *Handler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(auth.Middleware(h.tokens))

		r.Get("/vehicles/{vehicleID}/obligations", h.listObligations)
		r.Post("/vehicles/{vehicleID}/obligations", h.createObligation)
		r.Patch("/obligations/{obligationID}", h.updateObligation)
		r.Delete("/obligations/{obligationID}", h.deleteObligation)

		r.Get("/vehicles/{vehicleID}/seguros", h.listSeguros)
		r.Post("/vehicles/{vehicleID}/seguros", h.createSeguro)
		r.Patch("/seguros/{seguroID}", h.updateSeguro)
		r.Delete("/seguros/{seguroID}", h.deleteSeguro)
	})
}

// ---------- obligations ----------

func (h *Handler) listObligations(w http.ResponseWriter, r *http.Request) {
	userID, vehicleID, err := callerAndVehicle(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	var kind *string
	if raw := r.URL.Query().Get("kind"); raw != "" {
		kind = &raw
	}

	obligations, err := h.service.ListObligations(r.Context(), userID, vehicleID, kind)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	today := h.service.Today()
	out := make([]obligationResponse, 0, len(obligations))
	for _, obligation := range obligations {
		out = append(out, toObligationResponse(obligation, today))
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"data": out})
}

func (h *Handler) createObligation(w http.ResponseWriter, r *http.Request) {
	userID, vehicleID, err := callerAndVehicle(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	req, err := httpx.DecodeBody[createObligationRequest](r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	obligation, err := h.service.CreateObligation(r.Context(), userID, vehicleID, req)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusCreated,
		toObligationResponse(obligation, h.service.Today()))
}

func (h *Handler) updateObligation(w http.ResponseWriter, r *http.Request) {
	userID, obligationID, err := callerAndPath(r, "obligationID")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	req, err := httpx.DecodeBody[updateObligationRequest](r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	obligation, err := h.service.UpdateObligation(r.Context(), userID, obligationID, req)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK,
		toObligationResponse(obligation, h.service.Today()))
}

func (h *Handler) deleteObligation(w http.ResponseWriter, r *http.Request) {
	userID, obligationID, err := callerAndPath(r, "obligationID")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	if err := h.service.DeleteObligation(r.Context(), userID, obligationID); err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// ---------- seguros ----------

func (h *Handler) listSeguros(w http.ResponseWriter, r *http.Request) {
	userID, vehicleID, err := callerAndVehicle(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	seguros, err := h.service.ListSeguros(r.Context(), userID, vehicleID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	today := h.service.Today()
	out := make([]seguroResponse, 0, len(seguros))
	for _, seguro := range seguros {
		out = append(out, toSeguroResponse(seguro, today))
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"data": out})
}

func (h *Handler) createSeguro(w http.ResponseWriter, r *http.Request) {
	userID, vehicleID, err := callerAndVehicle(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	req, err := httpx.DecodeBody[createSeguroRequest](r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	seguro, err := h.service.CreateSeguro(r.Context(), userID, vehicleID, req)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusCreated, toSeguroResponse(seguro, h.service.Today()))
}

func (h *Handler) updateSeguro(w http.ResponseWriter, r *http.Request) {
	userID, seguroID, err := callerAndPath(r, "seguroID")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	req, err := httpx.DecodeBody[updateSeguroRequest](r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	seguro, err := h.service.UpdateSeguro(r.Context(), userID, seguroID, req)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, toSeguroResponse(seguro, h.service.Today()))
}

func (h *Handler) deleteSeguro(w http.ResponseWriter, r *http.Request) {
	userID, seguroID, err := callerAndPath(r, "seguroID")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	if err := h.service.DeleteSeguro(r.Context(), userID, seguroID); err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.NoContent(w)
}

func callerAndVehicle(r *http.Request) (userID, vehicleID uuid.UUID, err error) {
	return callerAndPath(r, "vehicleID")
}

func callerAndPath(r *http.Request, param string) (userID, resourceID uuid.UUID, err error) {
	if userID, err = callerID(r); err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	if resourceID, err = httpx.PathUUID(r, param); err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	return userID, resourceID, nil
}

// callerID reads the authenticated user from the context. Reaching the false branch means
// a route was mounted outside the auth middleware — a wiring bug that must fail closed.
func callerID(r *http.Request) (uuid.UUID, error) {
	userID, ok := auth.UserID(r.Context())
	if !ok {
		return uuid.Nil, apperr.Unauthorized("Autenticação necessária.")
	}
	return userID, nil
}
