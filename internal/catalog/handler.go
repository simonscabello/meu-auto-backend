package catalog

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/simonscabello/meu-auto-backend/internal/platform/auth"
	"github.com/simonscabello/meu-auto-backend/internal/platform/httpx"
)

// Handler exposes the vehicle catalogue. It only translates: parse, delegate, render.
// Every decision — including whether to call the provider — lives in Service.
type Handler struct {
	service *Service
	tokens  *auth.TokenService
}

func NewHandler(service *Service, tokens *auth.TokenService) *Handler {
	return &Handler{service: service, tokens: tokens}
}

// Mount registers the module's routes under the caller's prefix (/v1).
//
// # Why these are behind authentication
//
// The catalogue is public data — brands and models are not secrets, and nothing here is
// scoped to an account. It still requires a token, because every request that misses the
// mirror spends part of a daily quota shared by every user. An anonymous endpoint that can
// make us call a third party is an anonymous endpoint that can exhaust us.
//
// # The route shape
//
// Nested collection to list, flat resource by id to read — the same convention as
// /vehicles/{id}/maintenance-plans and /maintenance-plans/{id} (SPEC.md section 7). The
// detail is /vehicle-model-years/{id} rather than nested three levels deep: the app
// already holds that id, and making it repeat the brand and the model would only add ways
// to send an inconsistent triple.
func (h *Handler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(auth.Middleware(h.tokens))

		// Flat patterns, never nested chi.Route calls: two subrouters on overlapping
		// prefixes make chi panic at startup, and more than one module hangs endpoints off
		// shared prefixes here.
		r.Get("/vehicle-brands", h.listBrands)
		r.Get("/vehicle-brands/{brandID}/models", h.listModels)
		r.Get("/vehicle-models/{modelID}/years", h.listModelYears)
		r.Get("/vehicle-model-years/{modelYearID}", h.getModelYear)
	})
}

func (h *Handler) listBrands(w http.ResponseWriter, r *http.Request) {
	brands, err := h.service.ListBrands(r.Context(), r.URL.Query().Get("vehicle_type"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	// An empty list is [], never null: a client that iterates should not have to
	// special-case "nothing yet".
	out := make([]brandResponse, 0, len(brands))
	for _, brand := range brands {
		out = append(out, toBrandResponse(brand))
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"data": out})
}

func (h *Handler) listModels(w http.ResponseWriter, r *http.Request) {
	brandID, err := httpx.PathUUID(r, "brandID")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	models, err := h.service.ListModels(r.Context(), brandID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	out := make([]modelResponse, 0, len(models))
	for _, model := range models {
		out = append(out, toModelResponse(model))
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"data": out})
}

func (h *Handler) listModelYears(w http.ResponseWriter, r *http.Request) {
	modelID, err := httpx.PathUUID(r, "modelID")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	years, err := h.service.ListModelYears(r.Context(), modelID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	out := make([]modelYearResponse, 0, len(years))
	for _, year := range years {
		out = append(out, toModelYearResponse(year))
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"data": out})
}

func (h *Handler) getModelYear(w http.ResponseWriter, r *http.Request) {
	modelYearID, err := httpx.PathUUID(r, "modelYearID")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	detail, err := h.service.ModelYear(r.Context(), modelYearID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, toModelYearDetailResponse(detail))
}
