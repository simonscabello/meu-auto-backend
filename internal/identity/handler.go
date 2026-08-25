package identity

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/meu-auto/meu-auto-backend/internal/platform/apperr"
	"github.com/meu-auto/meu-auto-backend/internal/platform/auth"
	"github.com/meu-auto/meu-auto-backend/internal/platform/httpx"
)

// Handler exposes the identity module over HTTP. It only translates: parse, delegate,
// render. Every decision lives in Service.
type Handler struct {
	service    *Service
	tokens     *auth.TokenService
	trustProxy bool
}

func NewHandler(service *Service, tokens *auth.TokenService, trustProxy bool) *Handler {
	return &Handler{service: service, tokens: tokens, trustProxy: trustProxy}
}

// Mount registers the module's routes under the caller's prefix (/v1).
func (h *Handler) Mount(r chi.Router) {
	r.Route("/auth", func(r chi.Router) {
		r.Post("/register", h.register)
		r.Post("/login", h.login)
		r.Post("/refresh", h.refresh)
		r.Post("/logout", h.logout)
		r.Post("/password-reset/request", h.requestPasswordReset)
		r.Post("/password-reset/confirm", h.confirmPasswordReset)
	})

	r.Group(func(r chi.Router) {
		r.Use(auth.Middleware(h.tokens))
		r.Get("/me", h.me)
		r.Patch("/me", h.updateMe)
		r.Delete("/me", h.deleteMe)
	})
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	req, err := httpx.DecodeBody[registerRequest](r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	session, err := h.service.Register(r.Context(), req, r.UserAgent())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusCreated, toSessionResponse(session))
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	req, err := httpx.DecodeBody[loginRequest](r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	session, err := h.service.Login(r.Context(), req,
		r.UserAgent(), httpx.ClientIP(r, h.trustProxy))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, toSessionResponse(session))
}

func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) {
	req, err := httpx.DecodeBody[refreshTokenRequest](r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := req.validate(); err != nil {
		httpx.Error(w, r, err)
		return
	}

	session, err := h.service.Refresh(r.Context(), req.RefreshToken, r.UserAgent())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, toSessionResponse(session))
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	req, err := httpx.DecodeBody[refreshTokenRequest](r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := req.validate(); err != nil {
		httpx.Error(w, r, err)
		return
	}

	if err := h.service.Logout(r.Context(), req.RefreshToken); err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.NoContent(w)
}

func (h *Handler) requestPasswordReset(w http.ResponseWriter, r *http.Request) {
	req, err := httpx.DecodeBody[passwordResetRequestRequest](r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	if err := h.service.RequestPasswordReset(r.Context(), req,
		httpx.ClientIP(r, h.trustProxy)); err != nil {
		httpx.Error(w, r, err)
		return
	}

	// 202, and deliberately vague: the response is identical whether or not the address
	// has an account.
	httpx.JSON(w, r, http.StatusAccepted, map[string]string{
		"message": "Se este e-mail estiver cadastrado, enviaremos um link de redefinição.",
	})
}

func (h *Handler) confirmPasswordReset(w http.ResponseWriter, r *http.Request) {
	req, err := httpx.DecodeBody[passwordResetConfirmRequest](r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	if err := h.service.ConfirmPasswordReset(r.Context(), req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.NoContent(w)
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	userID, err := callerID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	user, err := h.service.Me(r.Context(), userID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, toUserResponse(user))
}

func (h *Handler) updateMe(w http.ResponseWriter, r *http.Request) {
	userID, err := callerID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	req, err := httpx.DecodeBody[updateMeRequest](r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	user, err := h.service.UpdateName(r.Context(), userID, req)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, toUserResponse(user))
}

func (h *Handler) deleteMe(w http.ResponseWriter, r *http.Request) {
	userID, err := callerID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	req, err := httpx.DecodeBody[deleteMeRequest](r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	if err := h.service.DeleteAccount(r.Context(), userID, req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// callerID reads the authenticated user from the context.
//
// Reaching the false branch means a route was mounted outside the auth middleware, which
// is a wiring bug — but it must fail closed, not proceed with a zero uuid.
func callerID(r *http.Request) (uuid.UUID, error) {
	userID, ok := auth.UserID(r.Context())
	if !ok {
		return uuid.Nil, apperr.Unauthorized("Autenticação necessária.")
	}
	return userID, nil
}
