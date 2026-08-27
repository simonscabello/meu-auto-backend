package maintenance

import (
	"regexp"
	"strings"
	"time"

	"github.com/simonscabello/meu-auto-backend/internal/maintenance/db"
	"github.com/simonscabello/meu-auto-backend/internal/platform/civil"
	"github.com/simonscabello/meu-auto-backend/internal/platform/validate"
)

// Contract values. The app switches on these (SPEC.md D-01).
const (
	// Origin is where the information in a plan came from.
	//
	// It answers one question — "who says so?" — which is why it is one column rather than
	// an ownership flag beside a provenance flag that would answer it twice.
	// OriginSuggested is this system generic market default; OriginUser is the decision the
	// owner made, and is what a future refresh of the defaults must never overwrite.
	OriginSuggested        = "suggested"
	OriginUser             = "user"
	OriginManufacturer     = "manufacturer"
	OriginManual           = "manual"
	OriginAdmin            = "admin"
	OriginExternalProvider = "external_provider"

	// Strategy is HOW an item is maintained. The old model had only one of these and
	// expressed it as "does the plan have an interval", which could not tell a component
	// with no service schedule from one whose schedule we simply do not know.
	//
	//	StrategyPeriodic        replace every X km or Y months
	//	StrategyInspection      look at it during a service; there may be no replacement
	//	StrategyConditionBased  replace when worn — tyres, pads, battery. An interval here
	//	                        is a "worth checking" horizon, never a deadline, and the
	//	                        app words it that way.
	//	StrategyNoSchedule      the component exists and has no periodic rule
	//	StrategyNotApplicable   the vehicle does not have the component
	StrategyPeriodic       = "periodic"
	StrategyInspection     = "inspection"
	StrategyConditionBased = "condition_based"
	StrategyNoSchedule     = "no_schedule"
	StrategyNotApplicable  = "not_applicable"

	// HistoryStatus is what the owner said about the past when no record exists.
	//
	// HistoryUnknown and HistoryNever are DIFFERENT answers and the product must never
	// merge them: "não sei" is a gap in memory, "nunca foi feito" is a fact about the car.
	HistoryNotAsked = "not_asked"
	HistoryUnknown  = "unknown"
	HistoryNever    = "never"

	KindMaintenance = "maintenance"
	KindCare        = "care"

	// RecordKindPerformed is a service that happened and can be evidenced.
	RecordKindPerformed = "performed"
	// RecordKindDeclared is the owner asserting from memory — a used car bought with
	// "last oil change at 95.000 km" and no receipt (SPEC.md RN-03).
	RecordKindDeclared = "declared"

	recordSourceManual     = "manual"
	recordSourceCorrection = "correction"
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

	IntervalKm     *int32  `json:"interval_km"`
	IntervalMonths *int32  `json:"interval_months"`
	IntervalDays   *int32  `json:"interval_days"`
	AlertKm        *int32  `json:"alert_km"`
	AlertDays      *int32  `json:"alert_days"`
	Strategy       *string `json:"strategy"`
	Notes          *string `json:"notes"`
}

func (r *createPlanRequest) validate() error {
	errs := validate.New()

	if strings.TrimSpace(r.MaintenanceItemID) == "" {
		errs.Add("maintenance_item_id", "Informe o item de manutenção.")
	}
	validateIntervals(errs, r.IntervalKm, r.IntervalMonths, r.IntervalDays)
	validateAlerts(errs, r.AlertKm, r.AlertDays)
	validateStrategy(errs, r.Strategy)

	r.Notes = trimOptional(r.Notes)
	if r.Notes != nil && len(*r.Notes) > maxNotesLength {
		errs.Add("notes", "Observação muito longa.")
	}

	return errs.Err("Não foi possível criar o plano.")
}

type updatePlanRequest struct {
	IntervalKm     *int32 `json:"interval_km"`
	IntervalMonths *int32 `json:"interval_months"`
	IntervalDays   *int32 `json:"interval_days"`
	AlertKm        *int32 `json:"alert_km"`
	AlertDays      *int32 `json:"alert_days"`

	// Strategy is how the owner says this item is maintained on their car, including
	// "not_applicable" for a component it does not have. A system suggestion never blocks
	// a correction — the owner is the one looking at the vehicle.
	Strategy *string `json:"strategy"`

	// HistoryStatus records "não sei" or "nunca foi feito" without inventing a record. A
	// declared record asserts a date and a mileage, and somebody who does not remember has
	// neither — writing one anyway would put a fabricated fact into the service history,
	// which is the one thing this product must never do.
	HistoryStatus *string `json:"history_status"`

	Notes *string `json:"notes"`

	// ClearIntervals turns the plan into one that only groups history and never comes
	// due. It needs its own flag because a null field already means "leave unchanged".
	ClearIntervals bool `json:"clear_intervals"`
	ClearNotes     bool `json:"clear_notes"`
}

func (r *updatePlanRequest) validate() error {
	errs := validate.New()

	if r.ClearIntervals && (r.IntervalKm != nil || r.IntervalMonths != nil || r.IntervalDays != nil) {
		errs.Add("clear_intervals",
			"Não é possível limpar e definir intervalos na mesma requisição.")
	}
	if r.ClearNotes && r.Notes != nil {
		errs.Add("clear_notes",
			"Não é possível limpar e definir a observação na mesma requisição.")
	}
	validateIntervals(errs, r.IntervalKm, r.IntervalMonths, r.IntervalDays)
	validateAlerts(errs, r.AlertKm, r.AlertDays)
	validateStrategy(errs, r.Strategy)
	validateHistoryStatus(errs, r.HistoryStatus)

	r.Notes = trimOptional(r.Notes)
	if r.Notes != nil && len(*r.Notes) > maxNotesLength {
		errs.Add("notes", "Observação muito longa.")
	}

	return errs.Err("Não foi possível atualizar o plano.")
}

// answerProfileRequest is the owner telling us how their car is built.
type answerProfileRequest struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// normalizeAndValidate resolves the answer against the fixed question list. An unknown
// question or an answer that is not one of its options is a 422, never a stored row: the
// vocabulary lives in profile.go and nothing outside it may widen it.
func (r *answerProfileRequest) normalizeAndValidate() (ProfileQuestion, ProfileOption, error) {
	errs := validate.New()

	r.Question = strings.TrimSpace(r.Question)
	r.Answer = strings.TrimSpace(r.Answer)

	question, ok := findProfileQuestion(r.Question)
	if !ok {
		errs.Add("question", "Pergunta desconhecida.")
		return ProfileQuestion{}, ProfileOption{}, errs.Err("Não foi possível salvar a resposta.")
	}

	option, ok := question.option(r.Answer)
	if !ok {
		errs.Add("answer", "Resposta inválida para esta pergunta.")
		return ProfileQuestion{}, ProfileOption{}, errs.Err("Não foi possível salvar a resposta.")
	}

	return question, option, nil
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
	MileageKm      *int32  `json:"mileage_km"`
	Kind           string  `json:"kind"`
	WorkshopName   *string `json:"workshop_name"`
	TotalCostCents *int64  `json:"total_cost_cents"`
	Notes          *string `json:"notes"`

	Items []recordItemRequest `json:"items"`

	// Source is a validation instruction, not the value persisted on the odometer
	// reading this record produces. That reading is always written with
	// source = 'maintenance' and source_maintenance_id set. "correction" only skips
	// CheckOdometerConsistency, the same way POST /odometer does.
	Source string `json:"source"`

	occurredOn time.Time
}

func (r *createRecordRequest) normalizeAndValidate(today time.Time) error {
	errs := validate.New()

	r.occurredOn = parseOccurredOn(errs, r.OccurredOn, today)

	if r.MileageKm != nil && (*r.MileageKm < 0 || *r.MileageKm > maxMileageKm) {
		errs.Add("mileage_km", "Quilometragem inválida.")
	}

	if r.Kind == "" {
		r.Kind = RecordKindPerformed
	}
	if r.Kind != RecordKindPerformed && r.Kind != RecordKindDeclared {
		errs.Add("kind", "Tipo inválido. Use performed ou declared.")
	}

	validateRecordSource(errs, &r.Source)

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

// recordRequiresMileage reports whether any resolved catalogue kind is a service
// (kind = maintenance). A care-only record may omit mileage; a mixed one may not.
func recordRequiresMileage(itemKinds []string) bool {
	for _, kind := range itemKinds {
		if kind != KindCare {
			return true
		}
	}
	return false
}

func errIfMileageMissing(mileageKm *int32, itemKinds []string) error {
	if mileageKm != nil || !recordRequiresMileage(itemKinds) {
		return nil
	}
	errs := validate.New()
	errs.Add("mileage_km", "Informe a quilometragem.")
	return errs.Err("Não foi possível registrar a manutenção.")
}

type updateRecordRequest struct {
	OccurredOn     *string `json:"occurred_on"`
	MileageKm      *int32  `json:"mileage_km"`
	WorkshopName   *string `json:"workshop_name"`
	TotalCostCents *int64  `json:"total_cost_cents"`
	Notes          *string `json:"notes"`

	// Source is a validation instruction, not the value persisted on the odometer
	// reading this record produced. See createRecordRequest.Source.
	Source string `json:"source"`

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

	validateRecordSource(errs, &r.Source)

	return errs.Err("Não foi possível atualizar o registro.")
}

func validateRecordSource(errs validate.Errors, source *string) {
	switch *source {
	case "":
		*source = recordSourceManual
	case recordSourceManual, recordSourceCorrection:
		return
	default:
		errs.Add("source", "Origem inválida. Use manual ou correction.")
	}
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

// planStrategies is the vocabulary a client may set. StrategyNotApplicable is in it on
// purpose: "meu carro não tem isso" is a thing the owner is allowed to say, and it is the
// escape hatch for every vehicle whose configuration this system cannot derive.
var planStrategies = map[string]bool{
	StrategyPeriodic:       true,
	StrategyInspection:     true,
	StrategyConditionBased: true,
	StrategyNoSchedule:     true,
	StrategyNotApplicable:  true,
}

var historyStatuses = map[string]bool{
	HistoryNotAsked: true,
	HistoryUnknown:  true,
	HistoryNever:    true,
}

func validateStrategy(errs validate.Errors, strategy *string) {
	if strategy != nil && !planStrategies[*strategy] {
		errs.Add("strategy", "Estratégia inválida.")
	}
}

func validateHistoryStatus(errs validate.Errors, status *string) {
	if status != nil && !historyStatuses[*status] {
		errs.Add("history_status", "Estado de histórico inválido.")
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
	ID          string `json:"id"`
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	VehicleType string `json:"vehicle_type"`
	IsCustom    bool   `json:"is_custom"`

	// What kind of maintenance this item is, as a concept. The plan for a given vehicle
	// may say something else — including that the vehicle does not have the component.
	//
	// `powertrain_requirement` is deliberately NOT on the wire. Applicability is decided
	// here, against the vehicle, and shipping the raw requirement would invite the app to
	// decide it a second time and disagree.
	DefaultStrategy string `json:"default_strategy"`

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
		DefaultStrategy:       i.DefaultStrategy,
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

	// Strategy is how this item is maintained on THIS vehicle. It is what lets the app
	// word a condition-based item as "vale checar" instead of "vencido", and it is where
	// "não se aplica" lives.
	Strategy string `json:"strategy"`

	// HistoryStatus tells "we never asked" from "asked, and they do not know" — the
	// difference between a useful prompt and nagging.
	HistoryStatus string  `json:"history_status"`
	Notes         *string `json:"notes"`

	// The question to ask when this item has no baseline, and how much it matters, both
	// from the catalogue. They are here so the app builds its history prompt from the plans
	// it already has, instead of carrying a map from technical slug to pt-BR question and a
	// hand-ranked list of slugs — which is how every car ended up being asked about a
	// timing belt.
	HistoryQuestion *string `json:"history_question"`
	HistoryPriority int32   `json:"history_priority"`

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
		ID:              due.Plan.ID.String(),
		ItemID:          due.Plan.ItemID.String(),
		ItemSlug:        due.Plan.ItemSlug,
		ItemName:        due.Plan.ItemName,
		ItemKind:        due.Plan.ItemKind,
		IntervalKm:      due.Plan.IntervalKm,
		IntervalMonths:  due.Plan.IntervalMonths,
		IntervalDays:    due.Plan.IntervalDays,
		AlertKm:         due.Plan.AlertKm,
		AlertDays:       due.Plan.AlertDays,
		Origin:          due.Plan.Origin,
		Strategy:        due.Plan.Strategy,
		HistoryStatus:   due.Plan.HistoryStatus,
		Notes:           due.Plan.Notes,
		HistoryQuestion: due.Plan.HistoryQuestion,
		HistoryPriority: due.Plan.HistoryPriority,
		Status:          due.Status,
		DueAtKm:         due.DueAtKm,
		DueOn:           civil.FormatPtr(due.DueOn),
		RemainingKm:     due.RemainingKm,
		RemainingDays:   due.RemainingDays,
	}
	if due.Last != nil {
		out.LastOccurredOn = civil.FormatPtr(&due.Last.OccurredOn)
		out.LastMileageKm = due.Last.MileageKm
	}
	return out
}

// ---------- profile ----------

// profileResponse answers "what does this vehicle actually need, and what do we still not
// know about it?" in one payload.
//
// The slugs an answer decides are NOT on the wire. The app posts a question id and an
// answer value and reads back the plans; deciding which catalogue entries an answer turns
// on stays here, so there is one definition of the rule and no chance of the two halves
// disagreeing.
type profileResponse struct {
	// unknown (no plan at all) | incomplete (something still open) | ready.
	Status string `json:"status"`

	// False when the vehicle has no fuel type. Everything that depends on having an engine
	// is unresolved, and the app asks for the fuel instead of guessing at components.
	PowertrainKnown bool `json:"powertrain_known"`

	PlanCount           int `json:"plan_count"`
	NotApplicableCount  int `json:"not_applicable_count"`
	MissingHistoryCount int `json:"missing_history_count"`

	Questions []profileQuestionResponse `json:"questions"`

	// What has already been answered, question id to answer value — including "unknown",
	// which is why the question is not being asked again.
	Answers map[string]string `json:"answers"`
}

type profileQuestionResponse struct {
	ID      string                          `json:"id"`
	Prompt  string                          `json:"prompt"`
	Help    string                          `json:"help"`
	Options []profileQuestionOptionResponse `json:"options"`
}

type profileQuestionOptionResponse struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

func toProfileResponse(profile Profile) profileResponse {
	out := profileResponse{
		Status:              profile.Status,
		PowertrainKnown:     profile.PowertrainKnown,
		PlanCount:           profile.PlanCount,
		NotApplicableCount:  profile.NotApplicable,
		MissingHistoryCount: profile.MissingHistory,
		Questions:           make([]profileQuestionResponse, 0, len(profile.Questions)),
		Answers:             profile.Answers,
	}
	if out.Answers == nil {
		// Never null: a client iterating the map should not have to special-case "nothing
		// answered yet".
		out.Answers = map[string]string{}
	}

	for _, question := range profile.Questions {
		rendered := profileQuestionResponse{
			ID:      question.ID,
			Prompt:  question.Prompt,
			Help:    question.Help,
			Options: make([]profileQuestionOptionResponse, 0, len(question.Options)),
		}
		for _, option := range question.Options {
			rendered.Options = append(rendered.Options, profileQuestionOptionResponse{
				Value: option.Value,
				Label: option.Label,
			})
		}
		out.Questions = append(out.Questions, rendered)
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
	MileageKm      *int32  `json:"mileage_km"`
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
		if item.WarrantyKm != nil && r.MileageKm != nil {
			untilKm := *r.MileageKm + *item.WarrantyKm
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
