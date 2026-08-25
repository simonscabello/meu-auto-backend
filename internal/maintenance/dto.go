package maintenance

import (
	"regexp"
	"strings"
	"time"

	"github.com/meu-auto/meu-auto-backend/internal/maintenance/db"
	"github.com/meu-auto/meu-auto-backend/internal/platform/civil"
	"github.com/meu-auto/meu-auto-backend/internal/platform/validate"
)

// Contract values. The app switches on these (SPEC.md D-01).
const (
	OriginSuggested = "suggested"
	OriginUser      = "user"

	KindMaintenance = "maintenance"
	KindCare        = "care"

	// RecordKindPerformed is a service that happened and can be evidenced.
	RecordKindPerformed = "performed"
	// RecordKindDeclared is the owner asserting from memory — a used car bought with
	// "last oil change at 95.000 km" and no receipt (SPEC.md RN-03).
	RecordKindDeclared = "declared"
)

const (
	defaultAlertKm   int32 = 500
	defaultAlertDays int32 = 15

	minAlertKm, maxAlertKm     int32 = 100, 1000
	minAlertDays, maxAlertDays int32 = 1, 30

	maxTextLength   = 120
	maxNotesLength  = 500
	maxItemsPerRec  = 20
	maxIntervalKm   = 500_000
	maxIntervalMon  = 240
	maxIntervalDays = 3650
	maxMileageKm    = 3_000_000
	maxCostCents    = 100_000_000 // R$ 1.000.000,00

	minYear         = 1900
	defaultPageSize = 50
	maxPageSize     = 200
)

var nonSlugChars = regexp.MustCompile(`[^a-z0-9]+`)

// alertKmFor derives a distance threshold from the interval.
//
// A fixed 500 km would be noise on a 60.000 km timing belt and useless on a 10.000 km oil
// change. A tenth of the interval scales with what is being tracked; the clamp keeps it
// from becoming absurd at either end.
func alertKmFor(intervalKm *int32) int32 {
	if intervalKm == nil {
		return defaultAlertKm
	}
	return clamp(*intervalKm/10, minAlertKm, maxAlertKm)
}

// alertDaysFor derives a time threshold the same way.
//
// The clamp matters most at the short end: "calibrar os pneus a cada 15 dias" with a
// 15-day warning would be permanently due soon, which is the same as no warning at all.
func alertDaysFor(intervalMonths, intervalDays *int32) int32 {
	var total int32
	if intervalMonths != nil {
		total += *intervalMonths * 30
	}
	if intervalDays != nil {
		total += *intervalDays
	}
	if total == 0 {
		return defaultAlertDays
	}
	return clamp(total/10, minAlertDays, maxAlertDays)
}

func clamp(v, lo, hi int32) int32 {
	return min(max(v, lo), hi)
}

// ---------- requests ----------

type createItemRequest struct {
	Name                  string `json:"name"`
	Kind                  string `json:"kind"`
	DefaultIntervalKm     *int32 `json:"default_interval_km"`
	DefaultIntervalMonths *int32 `json:"default_interval_months"`
	DefaultIntervalDays   *int32 `json:"default_interval_days"`

	slug string
}

func (r *createItemRequest) normalizeAndValidate() error {
	errs := validate.New()

	r.Name = strings.TrimSpace(r.Name)
	switch {
	case r.Name == "":
		errs.Add("name", "Informe o nome do item.")
	case len(r.Name) > maxTextLength:
		errs.Add("name", "Nome muito longo.")
	}

	if r.Kind == "" {
		r.Kind = KindMaintenance
	}
	if r.Kind != KindMaintenance && r.Kind != KindCare {
		errs.Add("kind", "Tipo inválido. Use maintenance ou care.")
	}

	validateIntervals(errs, r.DefaultIntervalKm, r.DefaultIntervalMonths, r.DefaultIntervalDays)

	// The slug is derived rather than asked for: it is an internal handle, and making the
	// owner invent one is a question with no good answer for them.
	r.slug = slugify(r.Name)
	if r.slug == "" {
		errs.Add("name", "Use ao menos uma letra ou número no nome.")
	}

	return errs.Err("Não foi possível criar o item.")
}

type createPlanRequest struct {
	ID                string `json:"id"`
	MaintenanceItemID string `json:"maintenance_item_id"`

	IntervalKm     *int32 `json:"interval_km"`
	IntervalMonths *int32 `json:"interval_months"`
	IntervalDays   *int32 `json:"interval_days"`
	AlertKm        *int32 `json:"alert_km"`
	AlertDays      *int32 `json:"alert_days"`
}

func (r *createPlanRequest) validate() error {
	errs := validate.New()

	if strings.TrimSpace(r.MaintenanceItemID) == "" {
		errs.Add("maintenance_item_id", "Informe o item de manutenção.")
	}
	validateIntervals(errs, r.IntervalKm, r.IntervalMonths, r.IntervalDays)
	validateAlerts(errs, r.AlertKm, r.AlertDays)

	return errs.Err("Não foi possível criar o plano.")
}

type updatePlanRequest struct {
	IntervalKm     *int32 `json:"interval_km"`
	IntervalMonths *int32 `json:"interval_months"`
	IntervalDays   *int32 `json:"interval_days"`
	AlertKm        *int32 `json:"alert_km"`
	AlertDays      *int32 `json:"alert_days"`

	// ClearIntervals turns the plan into one that only groups history and never comes
	// due. It needs its own flag because a null field already means "leave unchanged".
	ClearIntervals bool `json:"clear_intervals"`
}

func (r *updatePlanRequest) validate() error {
	errs := validate.New()

	if r.ClearIntervals && (r.IntervalKm != nil || r.IntervalMonths != nil || r.IntervalDays != nil) {
		errs.Add("clear_intervals",
			"Não é possível limpar e definir intervalos na mesma requisição.")
	}
	validateIntervals(errs, r.IntervalKm, r.IntervalMonths, r.IntervalDays)
	validateAlerts(errs, r.AlertKm, r.AlertDays)

	return errs.Err("Não foi possível atualizar o plano.")
}

type recordItemRequest struct {
	MaintenanceItemID string  `json:"maintenance_item_id"`
	Description       *string `json:"description"`
	PartBrand         *string `json:"part_brand"`
	CostCents         *int64  `json:"cost_cents"`
	WarrantyMonths    *int32  `json:"warranty_months"`
	WarrantyKm        *int32  `json:"warranty_km"`
}

type createRecordRequest struct {
	ID string `json:"id"`

	OccurredOn     *string `json:"occurred_on"`
	MileageKm      int32   `json:"mileage_km"`
	Kind           string  `json:"kind"`
	WorkshopName   *string `json:"workshop_name"`
	TotalCostCents *int64  `json:"total_cost_cents"`
	Notes          *string `json:"notes"`

	Items []recordItemRequest `json:"items"`

	occurredOn time.Time
}

func (r *createRecordRequest) normalizeAndValidate(today time.Time) error {
	errs := validate.New()

	r.occurredOn = parseOccurredOn(errs, r.OccurredOn, today)

	if r.MileageKm < 0 || r.MileageKm > maxMileageKm {
		errs.Add("mileage_km", "Quilometragem inválida.")
	}

	if r.Kind == "" {
		r.Kind = RecordKindPerformed
	}
	if r.Kind != RecordKindPerformed && r.Kind != RecordKindDeclared {
		errs.Add("kind", "Tipo inválido. Use performed ou declared.")
	}

	r.WorkshopName = trimOptional(r.WorkshopName)
	if r.WorkshopName != nil && len(*r.WorkshopName) > maxTextLength {
		errs.Add("workshop_name", "Nome da oficina muito longo.")
	}

	r.Notes = trimOptional(r.Notes)
	if r.Notes != nil && len(*r.Notes) > maxNotesLength {
		errs.Add("notes", "Observação muito longa.")
	}

	if r.TotalCostCents != nil && (*r.TotalCostCents < 0 || *r.TotalCostCents > maxCostCents) {
		errs.Add("total_cost_cents", "Valor inválido.")
	}

	// At least one line is required. A record naming no item resets no clock and belongs
	// to no plan — it would be a cost with no meaning to the engine. The catalogue's
	// "Manutenção personalizada" is the escape hatch for anything unnamed.
	switch {
	case len(r.Items) == 0:
		errs.Add("items", "Informe ao menos um item de manutenção.")
	case len(r.Items) > maxItemsPerRec:
		errs.Add("items", "Itens demais em um único registro.")
	}

	seen := make(map[string]bool, len(r.Items))
	for i := range r.Items {
		item := &r.Items[i]

		if strings.TrimSpace(item.MaintenanceItemID) == "" {
			errs.Add("items", "Cada item precisa de um maintenance_item_id.")
			continue
		}
		if seen[item.MaintenanceItemID] {
			errs.Add("items", "O mesmo item aparece duas vezes no registro.")
		}
		seen[item.MaintenanceItemID] = true

		item.Description = trimOptional(item.Description)
		item.PartBrand = trimOptional(item.PartBrand)

		if item.CostCents != nil && (*item.CostCents < 0 || *item.CostCents > maxCostCents) {
			errs.Add("items", "Valor de item inválido.")
		}
		if item.WarrantyMonths != nil && (*item.WarrantyMonths <= 0 || *item.WarrantyMonths > maxIntervalMon) {
			errs.Add("items", "Garantia em meses inválida.")
		}
		if item.WarrantyKm != nil && (*item.WarrantyKm <= 0 || *item.WarrantyKm > maxIntervalKm) {
			errs.Add("items", "Garantia em quilômetros inválida.")
		}
	}

	return errs.Err("Não foi possível registrar a manutenção.")
}

type updateRecordRequest struct {
	OccurredOn     *string `json:"occurred_on"`
	MileageKm      *int32  `json:"mileage_km"`
	WorkshopName   *string `json:"workshop_name"`
	TotalCostCents *int64  `json:"total_cost_cents"`
	Notes          *string `json:"notes"`

	occurredOn *time.Time
}

func (r *updateRecordRequest) normalizeAndValidate(today time.Time) error {
	errs := validate.New()

	if r.OccurredOn != nil {
		parsed := parseOccurredOn(errs, r.OccurredOn, today)
		if !parsed.IsZero() {
			r.occurredOn = &parsed
		}
	}

	if r.MileageKm != nil && (*r.MileageKm < 0 || *r.MileageKm > maxMileageKm) {
		errs.Add("mileage_km", "Quilometragem inválida.")
	}

	r.WorkshopName = trimOptional(r.WorkshopName)
	r.Notes = trimOptional(r.Notes)

	if r.TotalCostCents != nil && (*r.TotalCostCents < 0 || *r.TotalCostCents > maxCostCents) {
		errs.Add("total_cost_cents", "Valor inválido.")
	}

	return errs.Err("Não foi possível atualizar o registro.")
}

// ---------- shared validation ----------

func validateIntervals(errs validate.Errors, km, months, days *int32) {
	if km != nil && (*km <= 0 || *km > maxIntervalKm) {
		errs.Add("interval_km", "Intervalo em quilômetros inválido.")
	}
	if months != nil && (*months <= 0 || *months > maxIntervalMon) {
		errs.Add("interval_months", "Intervalo em meses inválido.")
	}
	if days != nil && (*days <= 0 || *days > maxIntervalDays) {
		errs.Add("interval_days", "Intervalo em dias inválido.")
	}
}

func validateAlerts(errs validate.Errors, alertKm, alertDays *int32) {
	if alertKm != nil && (*alertKm < 0 || *alertKm > maxIntervalKm) {
		errs.Add("alert_km", "Antecedência em quilômetros inválida.")
	}
	if alertDays != nil && (*alertDays < 0 || *alertDays > maxIntervalDays) {
		errs.Add("alert_days", "Antecedência em dias inválida.")
	}
}

func parseOccurredOn(errs validate.Errors, raw *string, today time.Time) time.Time {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return today
	}

	parsed, err := civil.Parse(strings.TrimSpace(*raw))
	switch {
	case err != nil:
		errs.Add("occurred_on", "Use o formato AAAA-MM-DD.")
	case parsed.After(today):
		errs.Add("occurred_on", "A data não pode estar no futuro.")
	case parsed.Year() < minYear:
		errs.Add("occurred_on", "Data muito antiga.")
	default:
		return parsed
	}
	return time.Time{}
}

func slugify(name string) string {
	lowered := strings.ToLower(strings.TrimSpace(name))
	replaced := replaceAccents(lowered)
	return strings.Trim(nonSlugChars.ReplaceAllString(replaced, "_"), "_")
}

// replaceAccents folds the Portuguese accented letters. A full Unicode normalisation would
// pull in golang.org/x/text for a handful of characters that are entirely predictable
// here.
func replaceAccents(s string) string {
	return strings.NewReplacer(
		"á", "a", "à", "a", "ã", "a", "â", "a", "ä", "a",
		"é", "e", "ê", "e", "è", "e", "ë", "e",
		"í", "i", "ì", "i", "î", "i", "ï", "i",
		"ó", "o", "ò", "o", "õ", "o", "ô", "o", "ö", "o",
		"ú", "u", "ù", "u", "û", "u", "ü", "u",
		"ç", "c", "ñ", "n",
	).Replace(s)
}

func trimOptional(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

// ---------- responses ----------

type itemResponse struct {
	ID                    string `json:"id"`
	Slug                  string `json:"slug"`
	Name                  string `json:"name"`
	Kind                  string `json:"kind"`
	VehicleType           string `json:"vehicle_type"`
	IsCustom              bool   `json:"is_custom"`
	DefaultIntervalKm     *int32 `json:"default_interval_km"`
	DefaultIntervalMonths *int32 `json:"default_interval_months"`
	DefaultIntervalDays   *int32 `json:"default_interval_days"`
}

func toItemResponse(i db.MaintenanceItem) itemResponse {
	return itemResponse{
		ID:                    i.ID.String(),
		Slug:                  i.Slug,
		Name:                  i.Name,
		Kind:                  i.Kind,
		VehicleType:           i.VehicleType,
		IsCustom:              i.OwnerUserID != nil,
		DefaultIntervalKm:     i.DefaultIntervalKm,
		DefaultIntervalMonths: i.DefaultIntervalMonths,
		DefaultIntervalDays:   i.DefaultIntervalDays,
	}
}

// planResponse carries the rule and its computed state together, because a plan without
// "when is it due" is not what any screen wants to show.
type planResponse struct {
	ID       string `json:"id"`
	ItemID   string `json:"maintenance_item_id"`
	ItemSlug string `json:"item_slug"`
	ItemName string `json:"item_name"`
	ItemKind string `json:"item_kind"`

	IntervalKm     *int32 `json:"interval_km"`
	IntervalMonths *int32 `json:"interval_months"`
	IntervalDays   *int32 `json:"interval_days"`
	AlertKm        int32  `json:"alert_km"`
	AlertDays      int32  `json:"alert_days"`
	Origin         string `json:"origin"`

	Status        Status  `json:"status"`
	DueAtKm       *int32  `json:"due_at_km"`
	DueOn         *string `json:"due_on"`
	RemainingKm   *int32  `json:"remaining_km"`
	RemainingDays *int32  `json:"remaining_days"`

	LastOccurredOn *string `json:"last_occurred_on"`
	LastMileageKm  *int32  `json:"last_mileage_km"`
}

func toPlanResponse(due Due) planResponse {
	out := planResponse{
		ID:             due.Plan.ID.String(),
		ItemID:         due.Plan.ItemID.String(),
		ItemSlug:       due.Plan.ItemSlug,
		ItemName:       due.Plan.ItemName,
		ItemKind:       due.Plan.ItemKind,
		IntervalKm:     due.Plan.IntervalKm,
		IntervalMonths: due.Plan.IntervalMonths,
		IntervalDays:   due.Plan.IntervalDays,
		AlertKm:        due.Plan.AlertKm,
		AlertDays:      due.Plan.AlertDays,
		Origin:         due.Plan.Origin,
		Status:         due.Status,
		DueAtKm:        due.DueAtKm,
		DueOn:          civil.FormatPtr(due.DueOn),
		RemainingKm:    due.RemainingKm,
		RemainingDays:  due.RemainingDays,
	}
	if due.Last != nil {
		out.LastOccurredOn = civil.FormatPtr(&due.Last.OccurredOn)
		out.LastMileageKm = &due.Last.MileageKm
	}
	return out
}

type recordItemResponse struct {
	ID       string `json:"id"`
	ItemID   string `json:"maintenance_item_id"`
	ItemSlug string `json:"item_slug"`
	ItemName string `json:"item_name"`

	Description *string `json:"description"`
	PartBrand   *string `json:"part_brand"`
	CostCents   *int64  `json:"cost_cents"`

	WarrantyMonths *int32 `json:"warranty_months"`
	WarrantyKm     *int32 `json:"warranty_km"`

	// Derived from the record's date, never stored (SPEC.md RN-05).
	WarrantyUntil *string `json:"warranty_until"`
	// Derived from the record's mileage.
	WarrantyUntilKm *int32 `json:"warranty_until_km"`
}

type recordResponse struct {
	ID        string `json:"id"`
	VehicleID string `json:"vehicle_id"`

	OccurredOn     string  `json:"occurred_on"`
	MileageKm      int32   `json:"mileage_km"`
	Kind           string  `json:"kind"`
	WorkshopName   *string `json:"workshop_name"`
	TotalCostCents int64   `json:"total_cost_cents"`
	Notes          *string `json:"notes"`

	Items []recordItemResponse `json:"items"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func toRecordResponse(r db.MaintenanceRecord, items []db.ListMaintenanceRecordItemsRow) recordResponse {
	out := recordResponse{
		ID:             r.ID.String(),
		VehicleID:      r.VehicleID.String(),
		OccurredOn:     civil.Format(r.OccurredOn),
		MileageKm:      r.MileageKm,
		Kind:           r.Kind,
		WorkshopName:   r.WorkshopName,
		TotalCostCents: r.TotalCostCents,
		Notes:          r.Notes,
		Items:          make([]recordItemResponse, 0, len(items)),
		CreatedAt:      r.CreatedAt,
		UpdatedAt:      r.UpdatedAt,
	}

	for _, item := range items {
		line := recordItemResponse{
			ID:             item.ID.String(),
			ItemID:         item.MaintenanceItemID.String(),
			ItemSlug:       item.ItemSlug,
			ItemName:       item.ItemName,
			Description:    item.Description,
			PartBrand:      item.PartBrand,
			CostCents:      item.CostCents,
			WarrantyMonths: item.WarrantyMonths,
			WarrantyKm:     item.WarrantyKm,
		}
		if item.WarrantyMonths != nil {
			until := civil.AddMonths(r.OccurredOn, int(*item.WarrantyMonths))
			line.WarrantyUntil = civil.FormatPtr(&until)
		}
		if item.WarrantyKm != nil {
			untilKm := r.MileageKm + *item.WarrantyKm
			line.WarrantyUntilKm = &untilKm
		}
		out.Items = append(out.Items, line)
	}
	return out
}

type recordPage struct {
	Data       []recordResponse `json:"data"`
	NextCursor *string          `json:"next_cursor"`
}
