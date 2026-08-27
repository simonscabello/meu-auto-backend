package vehicle

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/simonscabello/meu-auto-backend/internal/platform/apperr"
	"github.com/simonscabello/meu-auto-backend/internal/platform/auth"
	"github.com/simonscabello/meu-auto-backend/internal/platform/httpx"
)

// Handler exposes the vehicle and odometer endpoints. It only translates: parse,
// delegate, render. Every decision lives in Service.
type Handler struct {
	service *Service
	tokens  *auth.TokenService
}

func NewHandler(service *Service, tokens *auth.TokenService) *Handler {
	return &Handler{service: service, tokens: tokens}
}

// Mount registers the module's routes under the caller's prefix (/v1).
//
// Every route sits behind the auth middleware — there is no anonymous read of a vehicle.
func (h *Handler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(auth.Middleware(h.tokens))

		// Flat patterns rather than nested chi.Route calls. Route mounts a subrouter at
		// its prefix, and the maintenance module also hangs endpoints off
		// /vehicles/{vehicleID} — two subrouters on overlapping prefixes make chi panic at
		// startup. Registering full patterns lets both modules share the subtree.
		r.Get("/vehicles", h.list)
		r.Post("/vehicles", h.create)

		r.Get("/vehicles/{vehicleID}", h.get)
		r.Patch("/vehicles/{vehicleID}", h.update)
		r.Delete("/vehicles/{vehicleID}", h.remove)

		r.Get("/vehicles/{vehicleID}/odometer", h.listReadings)
		r.Post("/vehicles/{vehicleID}/odometer", h.createReading)

		// Flat by id, because a reading id is globally unique and the client already
		// holds it — making the caller repeat the vehicle id would add a way to get the
		// pair wrong.
		r.Delete("/odometer/{readingID}", h.deleteReading)
	})
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	userID, err := callerID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	vehicles, err := h.service.List(r.Context(), userID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	// An empty list is [], never null: a client that does `for (v in data)` should not
	// have to special-case "no vehicles yet".
	out := make([]vehicleResponse, 0, len(vehicles))
	for _, v := range vehicles {
		out = append(out, h.service.toVehicleResponse(v))
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"data": out})
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	userID, err := callerID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	req, err := httpx.DecodeBody[createVehicleRequest](r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	vehicle, created, err := h.service.Create(r.Context(), userID, req)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	// 201 for a real create, 200 when the client retried something that already
	// succeeded. Same body either way.
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.JSON(w, r, status, h.service.toVehicleResponse(vehicle))
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	userID, vehicleID, err := callerAndVehicle(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	vehicle, err := h.service.Get(r.Context(), userID, vehicleID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, h.service.toVehicleResponse(vehicle))
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	userID, vehicleID, err := callerAndVehicle(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	req, err := httpx.DecodeBody[updateVehicleRequest](r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	vehicle, err := h.service.Update(r.Context(), userID, vehicleID, req)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, h.service.toVehicleResponse(vehicle))
}

func (h *Handler) remove(w http.ResponseWriter, r *http.Request) {
	userID, vehicleID, err := callerAndVehicle(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	if err := h.service.Delete(r.Context(), userID, vehicleID); err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.NoContent(w)
}

func (h *Handler) listReadings(w http.ResponseWriter, r *http.Request) {
	userID, vehicleID, err := callerAndVehicle(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	pageSize := httpx.QueryInt32(r, "limit", defaultPageSize, 1, maxPageSize)

	readings, nextCursor, err := h.service.ListReadings(
		r.Context(), userID, vehicleID, pageSize, r.URL.Query().Get("cursor"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	out := make([]readingResponse, 0, len(readings))
	for _, reading := range readings {
		out = append(out, toReadingResponse(reading))
	}
	httpx.JSON(w, r, http.StatusOK, readingPage{Data: out, NextCursor: nextCursor})
}

func (h *Handler) createReading(w http.ResponseWriter, r *http.Request) {
	userID, vehicleID, err := callerAndVehicle(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	req, err := httpx.DecodeBody[createReadingRequest](r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	reading, vehicle, err := h.service.CreateReading(r.Context(), userID, vehicleID, req)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	httpx.JSON(w, r, http.StatusCreated, createReadingResponse{
		Reading: toReadingResponse(reading),
		Vehicle: h.service.toVehicleResponse(vehicle),
	})
}

func (h *Handler) deleteReading(w http.ResponseWriter, r *http.Request) {
	userID, err := callerID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	readingID, err := httpx.PathUUID(r, "readingID")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	if err := h.service.DeleteReading(r.Context(), userID, readingID); err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// callerAndVehicle reads both ids every vehicle-scoped route needs.
func callerAndVehicle(r *http.Request) (userID, vehicleID uuid.UUID, err error) {
	if userID, err = callerID(r); err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	if vehicleID, err = httpx.PathUUID(r, "vehicleID"); err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	return userID, vehicleID, nil
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
