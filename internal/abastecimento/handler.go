package abastecimento

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

// Mount registers the module's routes under the caller's prefix (/v1).
//
// Flat patterns, not nested chi.Route: the vehicle module also owns endpoints under
// /vehicles/{vehicleID}, and two subrouters on overlapping prefixes make chi panic at
// startup.
func (h *Handler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(auth.Middleware(h.tokens))

		r.Get("/vehicles/{vehicleID}/abastecimentos", h.list)
		r.Post("/vehicles/{vehicleID}/abastecimentos", h.create)

		r.Get("/abastecimentos/{abastecimentoID}", h.get)
		r.Patch("/abastecimentos/{abastecimentoID}", h.update)
		r.Delete("/abastecimentos/{abastecimentoID}", h.remove)
	})
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	userID, vehicleID, err := callerAndPath(r, "vehicleID")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	pageSize := httpx.QueryInt32(r, "limit", defaultPageSize, 1, maxPageSize)
	rows, byID, nextCursor, err := h.service.List(
		r.Context(), userID, vehicleID, pageSize, r.URL.Query().Get("cursor"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	out := make([]abastecimentoResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, toResponse(row, byID[row.ID]))
	}
	httpx.JSON(w, r, http.StatusOK, abastecimentoPage{Data: out, NextCursor: nextCursor})
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	userID, vehicleID, err := callerAndPath(r, "vehicleID")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	req, err := httpx.DecodeBody[createRequest](r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	row, consumption, created, err := h.service.Create(r.Context(), userID, vehicleID, req)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.JSON(w, r, status, toResponse(row, consumption))
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	userID, id, err := callerAndPath(r, "abastecimentoID")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	row, consumption, err := h.service.Get(r.Context(), userID, id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, toResponse(row, consumption))
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	userID, id, err := callerAndPath(r, "abastecimentoID")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	req, err := httpx.DecodeBody[updateRequest](r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	row, consumption, err := h.service.Update(r.Context(), userID, id, req)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, toResponse(row, consumption))
}

func (h *Handler) remove(w http.ResponseWriter, r *http.Request) {
	userID, id, err := callerAndPath(r, "abastecimentoID")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	if err := h.service.Delete(r.Context(), userID, id); err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.NoContent(w)
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

func callerID(r *http.Request) (uuid.UUID, error) {
	userID, ok := auth.UserID(r.Context())
	if !ok {
		return uuid.Nil, apperr.Unauthorized("Autenticação necessária.")
	}
	return userID, nil
}
