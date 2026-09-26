package notification

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/simonscabello/meu-auto-backend/internal/platform/apperr"
	"github.com/simonscabello/meu-auto-backend/internal/platform/auth"
	"github.com/simonscabello/meu-auto-backend/internal/platform/httpx"
)

type Handler struct {
	service *Service
	tokens  *auth.TokenService
}

func NewHandler(service *Service, tokens *auth.TokenService) *Handler {
	return &Handler{service: service, tokens: tokens}
}

// Mount registers the routes. They live under /me like everything else that belongs to
// the account: no id in the path, always the caller.
func (h *Handler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(auth.Middleware(h.tokens))
		r.Post("/me/devices", h.registerDevice)
		r.Delete("/me/devices", h.forgetDevice)
	})
}

func (h *Handler) registerDevice(w http.ResponseWriter, r *http.Request) {
	userID, err := callerID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	req, err := httpx.DecodeBody[registerDeviceRequest](r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := h.service.RegisterDevice(r.Context(), userID, req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.NoContent(w)
}

func (h *Handler) forgetDevice(w http.ResponseWriter, r *http.Request) {
	userID, err := callerID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	req, err := httpx.DecodeBody[forgetDeviceRequest](r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := h.service.ForgetDevice(r.Context(), userID, req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// callerID reads the authenticated user. Reaching the false branch is a wiring bug, and it
// fails closed rather than proceeding with a zero id.
func callerID(r *http.Request) (uuid.UUID, error) {
	userID, ok := auth.UserID(r.Context())
	if !ok {
		return uuid.Nil, apperr.Unauthorized("Autenticação necessária.")
	}
	return userID, nil
}
