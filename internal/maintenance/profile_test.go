package maintenance

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func notApplicablePlan(slug string) Plan {
	return Plan{
		ID:            uuid.New(),
		ItemID:        uuid.New(),
		ItemSlug:      slug,
		ItemName:      slug,
		ItemKind:      KindMaintenance,
		Strategy:      StrategyNotApplicable,
		HistoryStatus: HistoryNotAsked,
		// Intervals survive on a not-applicable plan so the decision is reversible. They
		// must not leak into a due date — that is what this fixture is here to prove.
		IntervalKm: p(int32(10000)),
		AlertKm:    500,
		AlertDays:  15,
	}
}

// An item the vehicle does not have cannot come due, however long ago it was "last done"
// and however far the car has driven since.
func TestNotApplicableNeverComesDue(t *testing.T) {
	t.Parallel()

	due := ComputeDue(
		notApplicablePlan("troca_oleo"),
		performed(date(2010, time.January, 1), 1000),
		900000,
		date(2026, time.August, 27),
	)

	if due.Status != StatusNotApplicable {
		t.Errorf("Status = %q, want %q", due.Status, StatusNotApplicable)
	}
	if due.DueAtKm != nil || due.DueOn != nil || due.RemainingKm != nil || due.RemainingDays != nil {
		t.Errorf("a component the vehicle does not have produced a due point: %+v", due)
	}
}

// The dashboard orders by severity. "Não se aplica" has to sort below everything, including
// a plan that only groups history — it is not a quieter alert, it is not an alert.
func TestNotApplicableSortsBelowEverything(t *testing.T) {
	t.Parallel()

	if StatusNotApplicable.severity() >= StatusNoInterval.severity() {
		t.Error("nao_se_aplica does not sort below sem_periodicidade")
	}
	if StatusNotApplicable.severity() >= StatusOnTrack.severity() {
		t.Error("nao_se_aplica does not sort below em_dia")
	}
}

func applicablePlan(slug string, history string) Plan {
	return Plan{
		ID:            uuid.New(),
		ItemID:        uuid.New(),
		ItemSlug:      slug,
		ItemName:      slug,
		ItemKind:      KindMaintenance,
		Strategy:      StrategyPeriodic,
		HistoryStatus: history,
		IntervalKm:    p(int32(10000)),
		AlertKm:       500,
		AlertDays:     15,
	}
}

func dueOf(plans ...Plan) []Due {
	return ComputeAll(plans, nil, 50000, date(2026, time.August, 27))
}

func TestProfileCountsWhatIsStillMissing(t *testing.T) {
	t.Parallel()

	dues := dueOf(
		applicablePlan("troca_oleo", HistoryNotAsked),
		applicablePlan("fluido_freio", HistoryNotAsked),
		// Already answered "não sei". It still has no baseline, and it must NOT be counted
		// as something to ask about — the prompt is meant to disappear once it has been
		// addressed, and "não lembro" is an answer.
		applicablePlan("velas", HistoryUnknown),
		notApplicablePlan("correia_dentada"),
	)

	// A flex car: the timing question applies and has not been answered.
	profile := buildProfile(PowertrainFor(p("flex")), dues, map[string]string{})

	if profile.PlanCount != 3 {
		t.Errorf("PlanCount = %d, want 3", profile.PlanCount)
	}
	if profile.NotApplicable != 1 {
		t.Errorf("NotApplicable = %d, want 1", profile.NotApplicable)
	}
	if profile.MissingHistory != 2 {
		t.Errorf("MissingHistory = %d, want 2 — a \"não sei\" was counted as still missing",
			profile.MissingHistory)
	}
	if profile.Status != ProfileIncomplete {
		t.Errorf("Status = %q, want %q", profile.Status, ProfileIncomplete)
	}
}

// The behaviour that made this table worth existing: an answered question does not come
// back, and that includes the answer "não sei".
func TestAnsweredQuestionsStopBeingAsked(t *testing.T) {
	t.Parallel()

	flex := PowertrainFor(p("flex"))

	open := openQuestions(flex, map[string]string{})
	if len(open) != 1 || open[0].ID != QuestionTimingDrive {
		t.Fatalf("open questions for a flex car = %+v, want just timing_drive", open)
	}

	if open := openQuestions(flex, map[string]string{QuestionTimingDrive: AnswerUnknown}); len(open) != 0 {
		t.Errorf("after \"não sei\" the question came back: %+v", open)
	}
	if open := openQuestions(flex, map[string]string{QuestionTimingDrive: AnswerTimingChain}); len(open) != 0 {
		t.Errorf("after \"corrente\" the question came back: %+v", open)
	}
}

// Asking an electric car about its timing belt is the same bug as reminding it to change
// the oil. The question is gated on the vehicle having the thing it is about.
func TestTimingQuestionIsNotAskedOfAnElectric(t *testing.T) {
	t.Parallel()

	if open := openQuestions(PowertrainFor(p("eletrico")), map[string]string{}); len(open) != 0 {
		t.Errorf("an electric car was asked %+v", open)
	}
	if open := openQuestions(PowertrainFor(p("hibrido")), map[string]string{}); len(open) != 1 {
		t.Errorf("a hybrid — which does have a combustion engine — was asked %d questions, want 1",
			len(open))
	}
	// No fuel type: we cannot tell whether there is an engine, so we do not ask about one.
	// The app asks for the fuel instead.
	if open := openQuestions(PowertrainFor(nil), map[string]string{}); len(open) != 0 {
		t.Errorf("a vehicle with no fuel type was asked %+v", open)
	}
}

// A vehicle with no plans must say so plainly rather than have the app invent a schedule.
func TestProfileWithoutPlansIsUnknown(t *testing.T) {
	t.Parallel()

	profile := buildProfile(PowertrainFor(p("flex")), nil, map[string]string{})
	if profile.Status != ProfileUnknown {
		t.Errorf("Status = %q, want %q", profile.Status, ProfileUnknown)
	}
}

// Nothing left to ask, and the powertrain is known: the profile is done.
func TestProfileIsReadyWhenNothingIsOpen(t *testing.T) {
	t.Parallel()

	profile := buildProfile(
		PowertrainFor(p("flex")),
		dueOf(applicablePlan("troca_oleo", HistoryNever)),
		map[string]string{QuestionTimingDrive: AnswerTimingChain},
	)

	if profile.Status != ProfileReady {
		t.Errorf("Status = %q, want %q", profile.Status, ProfileReady)
	}
	// "Nunca foi feito" is an answer about the past, so it is not still missing.
	if profile.MissingHistory != 0 {
		t.Errorf("MissingHistory = %d, want 0", profile.MissingHistory)
	}
}

// A car whose fuel nobody filled in is never "ready": the one gap that blocks deriving
// anything about the engine is still open, and the app has to say so.
func TestProfileIsIncompleteWithoutAFuelType(t *testing.T) {
	t.Parallel()

	profile := buildProfile(PowertrainFor(nil),
		dueOf(applicablePlan("pneus", HistoryNever)), map[string]string{})

	if profile.PowertrainKnown {
		t.Error("PowertrainKnown is true for a vehicle with no fuel type")
	}
	if profile.Status != ProfileIncomplete {
		t.Errorf("Status = %q, want %q", profile.Status, ProfileIncomplete)
	}
}

// The timing answer decides BOTH items in one move. Answering "corrente" must turn the
// chain on and the belt off — swapping one rigid assumption for another is the thing this
// design exists to avoid.
func TestTimingAnswersDecideBothItems(t *testing.T) {
	t.Parallel()

	question, ok := findProfileQuestion(QuestionTimingDrive)
	if !ok {
		t.Fatal("timing_drive is not in the question list")
	}

	chain, ok := question.option(AnswerTimingChain)
	if !ok {
		t.Fatal("timing_drive has no \"chain\" option")
	}
	if len(chain.Applicable) != 1 || chain.Applicable[0] != slugTimingChain {
		t.Errorf("\"corrente\" turns on %v, want [%s]", chain.Applicable, slugTimingChain)
	}
	if len(chain.NotApplicable) != 1 || chain.NotApplicable[0] != slugTimingBelt {
		t.Errorf("\"corrente\" turns off %v, want [%s]", chain.NotApplicable, slugTimingBelt)
	}

	unknown, ok := question.option(AnswerUnknown)
	if !ok {
		t.Fatal("timing_drive has no \"não sei\" option")
	}
	if len(unknown.Applicable) != 0 || len(unknown.NotApplicable) != 0 {
		t.Error("\"não sei\" decided something; it must decide nothing and only be remembered")
	}

	if _, ok := question.option("talvez"); ok {
		t.Error("an answer outside the vocabulary was accepted")
	}
}
