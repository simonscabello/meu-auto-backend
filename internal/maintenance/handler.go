package maintenance

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/simonscabello/meu-auto-backend/internal/maintenance/db"
	"github.com/simonscabello/meu-auto-backend/internal/platform/apperr"
	"github.com/simonscabello/meu-auto-backend/internal/platform/auth"
	"github.com/simonscabello/meu-auto-backend/internal/platform/httpx"
)

// Handler exposes the maintenance endpoints. It only translates: parse, delegate, render.
type Handler struct {
	service *Service
	tokens  *auth.TokenService
}

func NewHandler(service *Service, tokens *auth.TokenService) *Handler {
	return &Handler{service: service, tokens: tokens}
}

// Mount registers the module's routes under the caller's prefix (/v1).
func (h *Handler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(auth.Middleware(h.tokens))

		// Flat patterns, not nested chi.Route calls: the vehicle module also owns
		// endpoints under /vehicles/{vehicleID}, and two subrouters on overlapping
		// prefixes make chi panic at startup.
		r.Get("/maintenance-items", h.listItems)
		r.Post("/maintenance-items", h.createItem)

		r.Get("/vehicles/{vehicleID}/maintenance-plans", h.listPlans)
		r.Post("/vehicles/{vehicleID}/maintenance-plans", h.createPlan)

		r.Get("/vehicles/{vehicleID}/maintenance-profile", h.getProfile)
		r.Post("/vehicles/{vehicleID}/maintenance-profile/answers", h.answerProfile)
		r.Get("/vehicles/{vehicleID}/maintenance-records", h.listRecords)
		r.Post("/vehicles/{vehicleID}/maintenance-records", h.createRecord)

		// Flat by id: a plan or record id is globally unique and the client already holds
		// it, so making them repeat the vehicle id would only add a way to get the pair
		// wrong.
		r.Get("/maintenance-plans/{planID}", h.getPlan)
		r.Patch("/maintenance-plans/{planID}", h.updatePlan)
		r.Delete("/maintenance-plans/{planID}", h.deletePlan)

		r.Get("/maintenance-records/{recordID}", h.getRecord)
		r.Patch("/maintenance-records/{recordID}", h.updateRecord)
		r.Delete("/maintenance-records/{recordID}", h.deleteRecord)
	})
}

// ---------- catalogue ----------

func (h *Handler) listItems(w http.ResponseWriter, r *http.Request) {
	userID, err := callerID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	var kind *string
	if raw := r.URL.Query().Get("kind"); raw != "" {
		kind = &raw
	}

	items, err := h.service.ListItems(r.Context(), userID,
		r.URL.Query().Get("vehicle_type"), kind)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	out := make([]itemResponse, 0, len(items))
	for _, item := range items {
		out = append(out, toItemResponse(item))
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"data": out})
}

func (h *Handler) createItem(w http.ResponseWriter, r *http.Request) {
	userID, err := callerID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	req, err := httpx.DecodeBody[createItemRequest](r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	item, err := h.service.CreateItem(r.Context(), userID, req)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusCreated, toItemResponse(item))
}

// ---------- plans ----------

func (h *Handler) listPlans(w http.ResponseWriter, r *http.Request) {
	userID, vehicleID, err := callerAndVehicle(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	// Absent, "false" and "0" all mean the same thing, and that default is the product
	// decision: the screens that ask "what does my car need" must not see an item the car
	// does not have. Only the configuration surface opts in.
	includeNotApplicable := httpx.QueryBool(r, "include_not_applicable")

	dues, err := h.service.ListPlans(r.Context(), userID, vehicleID, includeNotApplicable)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	out := make([]planResponse, 0, len(dues))
	for _, due := range dues {
		out = append(out, toPlanResponse(due))
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"data": out})
}

func (h *Handler) createPlan(w http.ResponseWriter, r *http.Request) {
	userID, vehicleID, err := callerAndVehicle(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	req, err := httpx.DecodeBody[createPlanRequest](r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	plan, err := h.service.CreatePlan(r.Context(), userID, vehicleID, req)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusCreated, toBarePlanResponse(plan))
}

func (h *Handler) getPlan(w http.ResponseWriter, r *http.Request) {
	userID, err := callerID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	planID, err := httpx.PathUUID(r, "planID")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	due, err := h.service.GetPlan(r.Context(), userID, planID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, toPlanResponse(due))
}

func (h *Handler) updatePlan(w http.ResponseWriter, r *http.Request) {
	userID, err := callerID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	planID, err := httpx.PathUUID(r, "planID")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	req, err := httpx.DecodeBody[updatePlanRequest](r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	plan, err := h.service.UpdatePlan(r.Context(), userID, planID, req)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, toBarePlanResponse(plan))
}

func (h *Handler) deletePlan(w http.ResponseWriter, r *http.Request) {
	userID, err := callerID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	planID, err := httpx.PathUUID(r, "planID")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	if err := h.service.DeletePlan(r.Context(), userID, planID); err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// ---------- profile ----------

func (h *Handler) getProfile(w http.ResponseWriter, r *http.Request) {
	userID, vehicleID, err := callerAndVehicle(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	profile, err := h.service.Profile(r.Context(), userID, vehicleID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, toProfileResponse(profile))
}

func (h *Handler) answerProfile(w http.ResponseWriter, r *http.Request) {
	userID, vehicleID, err := callerAndVehicle(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	req, err := httpx.DecodeBody[answerProfileRequest](r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	// The whole profile comes back, not just the answer: an answer changes which plans
	// exist, and making the app fetch again to find out would leave a window where the
	// screen and the server disagree.
	profile, err := h.service.AnswerProfileQuestion(r.Context(), userID, vehicleID, req)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, toProfileResponse(profile))
}

// ---------- records ----------

func (h *Handler) listRecords(w http.ResponseWriter, r *http.Request) {
	userID, vehicleID, err := callerAndVehicle(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	pageSize := httpx.QueryInt32(r, "limit", defaultPageSize, 1, maxPageSize)

	records, itemsByRecord, nextCursor, err := h.service.ListRecords(
		r.Context(), userID, vehicleID, pageSize, r.URL.Query().Get("cursor"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	out := make([]recordResponse, 0, len(records))
	for _, record := range records {
		out = append(out, toRecordResponse(record, itemsByRecord[record.ID]))
	}
	httpx.JSON(w, r, http.StatusOK, recordPage{Data: out, NextCursor: nextCursor})
}

func (h *Handler) createRecord(w http.ResponseWriter, r *http.Request) {
	userID, vehicleID, err := callerAndVehicle(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	req, err := httpx.DecodeBody[createRecordRequest](r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	record, items, created, err := h.service.CreateRecord(r.Context(), userID, vehicleID, req)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	// 201 for a real create, 200 when the client retried something that already
	// succeeded.
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.JSON(w, r, status, toRecordResponse(record, items))
}

func (h *Handler) getRecord(w http.ResponseWriter, r *http.Request) {
	userID, recordID, err := callerAndRecord(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	record, items, err := h.service.GetRecord(r.Context(), userID, recordID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, toRecordResponse(record, items))
}

func (h *Handler) updateRecord(w http.ResponseWriter, r *http.Request) {
	userID, recordID, err := callerAndRecord(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	req, err := httpx.DecodeBody[updateRecordRequest](r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	record, items, err := h.service.UpdateRecord(r.Context(), userID, recordID, req)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, toRecordResponse(record, items))
}

func (h *Handler) deleteRecord(w http.ResponseWriter, r *http.Request) {
	userID, recordID, err := callerAndRecord(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	if err := h.service.DeleteRecord(r.Context(), userID, recordID); err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// toBarePlanResponse renders a plan without its due state.
//
// Used on create and update, where the caller already knows nothing has been performed
// since — the list endpoint is what carries the computed state.
func toBarePlanResponse(plan db.MaintenancePlan) map[string]any {
	return map[string]any{
		"id":                  plan.ID.String(),
		"maintenance_item_id": plan.MaintenanceItemID.String(),
		"interval_km":         plan.IntervalKm,
		"interval_months":     plan.IntervalMonths,
		"interval_days":       plan.IntervalDays,
		"alert_km":            plan.AlertKm,
		"alert_days":          plan.AlertDays,
		"origin":              plan.Origin,
		"strategy":            plan.Strategy,
		"history_status":      plan.HistoryStatus,
		"notes":               plan.Notes,
	}
}

func callerAndVehicle(r *http.Request) (userID, vehicleID uuid.UUID, err error) {
	if userID, err = callerID(r); err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	if vehicleID, err = httpx.PathUUID(r, "vehicleID"); err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	return userID, vehicleID, nil
}

func callerAndRecord(r *http.Request) (userID, recordID uuid.UUID, err error) {
	if userID, err = callerID(r); err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	if recordID, err = httpx.PathUUID(r, "recordID"); err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	return userID, recordID, nil
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
