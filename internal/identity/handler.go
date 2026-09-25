package identity

import (
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/simonscabello/meu-auto-backend/internal/identity/db"
	"github.com/simonscabello/meu-auto-backend/internal/platform/apperr"
	"github.com/simonscabello/meu-auto-backend/internal/platform/auth"
	"github.com/simonscabello/meu-auto-backend/internal/platform/httpx"
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
		r.Put("/me/photo", h.setPhoto)
		r.Delete("/me/photo", h.removePhoto)
		r.Post("/me/password", h.changePassword)
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
	httpx.JSON(w, r, http.StatusCreated, h.session(r, session))
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
	httpx.JSON(w, r, http.StatusOK, h.session(r, session))
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
	httpx.JSON(w, r, http.StatusOK, h.session(r, session))
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
	httpx.JSON(w, r, http.StatusOK, h.user(r, user))
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

	user, err := h.service.UpdateProfile(r.Context(), userID, req)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, h.user(r, user))
}

// setPhoto takes multipart/form-data with the picture in a part named "photo".
//
// Multipart rather than a raw body so a client can use its platform's ordinary upload
// call. The body gets its own ceiling, well above httpx.MaxBodyBytes, and only the one
// part is read — nothing is spooled to disk.
func (h *Handler) setPhoto(w http.ResponseWriter, r *http.Request) {
	userID, err := callerID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	data, err := readPhotoPart(w, r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	user, err := h.service.SetPhoto(r.Context(), userID, data)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, h.user(r, user))
}

func (h *Handler) removePhoto(w http.ResponseWriter, r *http.Request) {
	userID, err := callerID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := h.service.RemovePhoto(r.Context(), userID); err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// user renders an account, signing its photo URL for this response.
func (h *Handler) user(r *http.Request, user db.User) userResponse {
	return toUserResponse(user, h.service.PhotoURL(r.Context(), user))
}

func (h *Handler) session(r *http.Request, s Session) sessionResponse {
	return toSessionResponse(s, h.service.PhotoURL(r.Context(), s.User))
}

func (h *Handler) changePassword(w http.ResponseWriter, r *http.Request) {
	userID, err := callerID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	req, err := httpx.DecodeBody[changePasswordRequest](r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	session, err := h.service.ChangePassword(r.Context(), userID, req, r.UserAgent())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, h.session(r, session))
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

// photoBodySlack is room for the multipart envelope around the picture itself.
const photoBodySlack = 64 << 10

// readPhotoPart reads the "photo" part of a multipart body, at most MaxPhotoBytes of it.
func readPhotoPart(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, MaxPhotoBytes+photoBodySlack)
	reader, err := r.MultipartReader()
	if err != nil {
		return nil, apperr.Validation("Envie a foto como multipart/form-data.",
			map[string]any{"photo": "Envie uma foto."})
	}
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			return nil, apperr.Validation("Não foi possível salvar a foto.",
				map[string]any{"photo": "Envie uma foto."})
		}
		if err != nil {
			return nil, photoReadError(err)
		}
		if part.FormName() != "photo" {
			_ = part.Close()
			continue
		}
		// One byte past the limit is enough to know it is too large.
		data, err := io.ReadAll(io.LimitReader(part, MaxPhotoBytes+1))
		_ = part.Close()
		if err != nil {
			return nil, photoReadError(err)
		}
		if len(data) > MaxPhotoBytes {
			return nil, photoTooLarge()
		}
		return data, nil
	}
}

func photoReadError(err error) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return photoTooLarge()
	}
	return apperr.Wrap(err, apperr.CodeValidation, "Não foi possível ler a foto enviada.")
}
