package maintenance

// The vehicle's maintenance profile: which catalogue items apply to it, how each one is
// maintained, and what we still do not know.
//
// There is no separate profile table and there must not be one. `maintenance_plans` is
// already the row that joins one vehicle to one catalogue item and carries its intervals
// and its origin — it IS the profile. What this file adds is the small amount of
// information a plan cannot hold: the questions nobody has answered yet.
//
// Deliberately NOT a rules engine, a DSL or a plugin system. It is a fixed list of
// questions in Go, each naming the catalogue slugs its answers decide. Adding the next
// question is appending to a slice; there is nothing to configure and nothing to interpret
// at runtime.

// Question ids. Contract: the app posts these back.
const (
	// QuestionTimingDrive is the one technical fact worth interrupting somebody for. A
	// snapped timing belt destroys the engine, and the old model asked every car about a
	// belt — including cars that have a chain and cars that have no engine at all.
	QuestionTimingDrive = "timing_drive"
)

// Answer values. `AnswerUnknown` is a first-class answer, not a refusal: it is recorded, it
// stops the question coming back, and it produces no plan for either side.
const (
	AnswerTimingBelt  = "belt"
	AnswerTimingChain = "chain"
	AnswerUnknown     = "unknown"
)

// Catalogue slugs a profile answer decides. Global entries only.
const (
	slugTimingBelt  = "correia_dentada"
	slugTimingChain = "corrente_comando"
)

// Profile status, as the app reads it.
const (
	// ProfileUnknown — no plan at all. The app says so plainly and offers to add one; it
	// does not invent a schedule.
	ProfileUnknown = "unknown"
	// ProfileIncomplete — there are plans, and something is still open.
	ProfileIncomplete = "incomplete"
	// ProfileReady — nothing left to ask.
	ProfileReady = "ready"
)

// ProfileOption is one answer, and what it decides.
type ProfileOption struct {
	Value string
	Label string

	// Applicable and NotApplicable name catalogue slugs. Answering "corrente" turns the
	// chain on and the belt off in one move, which is what stops the app from replacing
	// one rigid assumption with another.
	//
	// An option that decides nothing — "não sei" — simply leaves both empty.
	Applicable    []string
	NotApplicable []string
}

// ProfileQuestion is something about the vehicle that cannot be derived and has to be
// asked.
type ProfileQuestion struct {
	ID     string
	Prompt string
	Help   string

	// Requires reuses the powertrain vocabulary: the question is only worth asking of a
	// vehicle that has the thing. Asking an electric car about its timing belt is the
	// same bug as reminding it to change the oil.
	Requires string

	Options []ProfileOption
}

// profileQuestions is the whole list. One entry today, and the shape is the extension
// point: a new question is a new element, not a new table and not a new abstraction.
var profileQuestions = []ProfileQuestion{
	{
		ID:       QuestionTimingDrive,
		Requires: RequirementCombustion,
		Prompt:   "Seu carro usa correia dentada ou corrente?",
		Help: "Se a correia arrebentar, o motor vai junto — por isso vale saber. " +
			"Está no manual, e o mecânico também sabe.",
		Options: []ProfileOption{
			{
				Value:         AnswerTimingBelt,
				Label:         "Correia dentada",
				Applicable:    []string{slugTimingBelt},
				NotApplicable: []string{slugTimingChain},
			},
			{
				Value:         AnswerTimingChain,
				Label:         "Corrente de comando",
				Applicable:    []string{slugTimingChain},
				NotApplicable: []string{slugTimingBelt},
			},
			{
				Value: AnswerUnknown,
				Label: "Não sei",
			},
		},
	},
}

func findProfileQuestion(id string) (ProfileQuestion, bool) {
	for _, question := range profileQuestions {
		if question.ID == id {
			return question, true
		}
	}
	return ProfileQuestion{}, false
}

func (q ProfileQuestion) option(value string) (ProfileOption, bool) {
	for _, option := range q.Options {
		if option.Value == value {
			return option, true
		}
	}
	return ProfileOption{}, false
}

// openQuestions is what the vehicle still has not told us.
//
// A question already answered never comes back, including when the answer was "não sei".
// That is the whole point of storing the answer: the difference between "never asked" and
// "asked, and they do not know" is the difference between a useful prompt and nagging.
func openQuestions(powertrain Powertrain, answers map[string]string) []ProfileQuestion {
	out := make([]ProfileQuestion, 0, len(profileQuestions))
	for _, question := range profileQuestions {
		if _, answered := answers[question.ID]; answered {
			continue
		}
		if powertrain.Applies(question.Requires) != ApplicabilityYes {
			continue
		}
		out = append(out, question)
	}
	return out
}

// Profile is the answer to "what does this vehicle actually need?".
type Profile struct {
	Status string

	// PowertrainKnown is false when the vehicle has no fuel type. Everything that depends
	// on having an engine is then unresolved, and the app asks for the fuel rather than
	// guessing at the components.
	PowertrainKnown bool

	// PlanCount excludes items the vehicle does not have; NotApplicable counts exactly
	// those, so the configuration screen can offer to undo one.
	PlanCount     int
	NotApplicable int

	// MissingHistory counts plans whose item has never been recorded AND that nobody has
	// been asked about. A "não sei" answer leaves this number alone — the prompt is meant
	// to disappear once it has been addressed, even when the answer was no answer.
	MissingHistory int

	Questions []ProfileQuestion
	Answers   map[string]string
}

// buildProfile derives the profile from what is already loaded. dues must include the
// not-applicable plans, which is the one call site that asks for them.
func buildProfile(powertrain Powertrain, dues []Due, answers map[string]string) Profile {
	profile := Profile{
		PowertrainKnown: powertrain.Known(),
		Questions:       openQuestions(powertrain, answers),
		Answers:         answers,
	}

	for _, due := range dues {
		if due.Plan.Strategy == StrategyNotApplicable {
			profile.NotApplicable++
			continue
		}
		profile.PlanCount++
		if due.Status == StatusNoBaseline && due.Plan.HistoryStatus == HistoryNotAsked {
			profile.MissingHistory++
		}
	}

	switch {
	case profile.PlanCount == 0:
		profile.Status = ProfileUnknown
	case len(profile.Questions) > 0 || !profile.PowertrainKnown:
		profile.Status = ProfileIncomplete
	default:
		profile.Status = ProfileReady
	}
	return profile
}

// strategyFor turns a catalogue item plus a powertrain verdict into the plan's strategy.
//
// The bool reports whether a plan should exist at all: an unknown verdict produces no row,
// because a row would be a claim. Silence is the conservative answer and the one the
// product wants — "ainda não sabemos" beats both a false reminder and a false absence.
func strategyFor(defaultStrategy string, verdict Applicability) (string, bool) {
	switch verdict {
	case ApplicabilityYes:
		if defaultStrategy == "" {
			return StrategyPeriodic, true
		}
		return defaultStrategy, true
	case ApplicabilityNo:
		return StrategyNotApplicable, true
	default:
		return "", false
	}
}
