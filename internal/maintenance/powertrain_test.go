package maintenance

import "testing"

// The one automatic source of applicability in the product. Everything here is the meaning
// of a word — an electric car has no engine oil because it has no engine — and nothing here
// is a specification for a make or a model. If a case in this file ever needs a comment
// explaining which manufacturer it came from, it does not belong here.

func TestPowertrainDecidesWhatAVehicleHas(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		fuel        *string
		requirement string
		want        Applicability
	}{
		// Anything with no requirement applies to everything, including a vehicle whose
		// fuel nobody filled in. Brakes are brakes.
		{"tyres on an unknown vehicle", nil, RequirementAny, ApplicabilityYes},
		{"tyres on an electric", p("eletrico"), RequirementAny, ApplicabilityYes},

		// The case the whole change exists for.
		{"engine oil on an electric", p("eletrico"), RequirementCombustion, ApplicabilityNo},
		{"spark plugs on an electric", p("eletrico"), RequirementSparkIgnition, ApplicabilityNo},
		{"engine oil on a flex", p("flex"), RequirementCombustion, ApplicabilityYes},

		// A diesel burns by compression. It has glow plugs, which are a different part.
		{"spark plugs on a diesel", p("diesel"), RequirementSparkIgnition, ApplicabilityNo},
		{"engine oil on a diesel", p("diesel"), RequirementCombustion, ApplicabilityYes},

		// A hybrid has both halves, which is exactly why one rigid rule cannot serve it.
		{"engine oil on a hybrid", p("hibrido"), RequirementCombustion, ApplicabilityYes},
		{"traction battery on a hybrid", p("hibrido"), RequirementHighVoltage, ApplicabilityYes},
		{"traction battery on a flex", p("flex"), RequirementHighVoltage, ApplicabilityNo},

		// No fuel type is not "no engine". It is silence, and silence must stay silence:
		// answering "no" here would delete somebody's oil change.
		{"engine oil with no fuel type", nil, RequirementCombustion, ApplicabilityUnknown},
		{"traction battery with no fuel type", nil, RequirementHighVoltage, ApplicabilityUnknown},

		// A fuel word a future server might send that this build does not know.
		{"engine oil on an unknown fuel", p("hidrogenio"), RequirementCombustion, ApplicabilityUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := PowertrainFor(tc.fuel).Applies(tc.requirement)
			if got != tc.want {
				t.Errorf("Applies(%q) = %v, want %v", tc.requirement, got, tc.want)
			}
		})
	}
}

// An unknown verdict must produce no plan at all. A plan — even one marked not applicable —
// is a claim, and we do not have one to make.
func TestUnknownApplicabilityProducesNoPlan(t *testing.T) {
	t.Parallel()

	if _, ok := strategyFor(StrategyPeriodic, ApplicabilityUnknown); ok {
		t.Error("an unknown verdict produced a plan; it must produce silence")
	}

	strategy, ok := strategyFor(StrategyConditionBased, ApplicabilityYes)
	if !ok || strategy != StrategyConditionBased {
		t.Errorf("applicable item = (%q, %v), want (condition_based, true)", strategy, ok)
	}

	strategy, ok = strategyFor(StrategyPeriodic, ApplicabilityNo)
	if !ok || strategy != StrategyNotApplicable {
		t.Errorf("inapplicable item = (%q, %v), want (not_applicable, true)", strategy, ok)
	}
}

func TestEmptyRequirementAppliesToEveryone(t *testing.T) {
	t.Parallel()

	got := PowertrainFor(p("eletrico")).Applies("")
	if got != ApplicabilityYes {
		t.Errorf("Applies(\"\") = %v, want yes — an empty requirement is brakes, not a guess", got)
	}
}

func TestUnknownRequirementIsSilence(t *testing.T) {
	t.Parallel()

	got := PowertrainFor(p("flex")).Applies("turbo")
	if got != ApplicabilityUnknown {
		t.Errorf("Applies(unknown) = %v, want unknown — a data problem must not hide an item", got)
	}
}

func TestHasCombustionEngine(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		fuel *string
		want bool
	}{
		{"flex", p("flex"), true},
		{"diesel", p("diesel"), true},
		{"hybrid", p("hibrido"), true},
		{"electric", p("eletrico"), false},
		{"null", nil, false},
		{"unknown", p("hidrogenio"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := PowertrainFor(tc.fuel).HasCombustionEngine()
			if got != tc.want {
				t.Errorf("HasCombustionEngine() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRequirementsByVerdict(t *testing.T) {
	t.Parallel()

	dieselYes, dieselNo := PowertrainFor(p("diesel")).requirementsByVerdict()
	if !equalStrings(dieselYes, []string{RequirementCombustion}) {
		t.Errorf("diesel satisfied = %v, want [combustion]", dieselYes)
	}
	if !equalStrings(dieselNo, []string{RequirementSparkIgnition, RequirementHighVoltage}) {
		t.Errorf("diesel unsatisfied = %v, want [spark_ignition high_voltage]", dieselNo)
	}

	electricYes, electricNo := PowertrainFor(p("eletrico")).requirementsByVerdict()
	if !equalStrings(electricYes, []string{RequirementHighVoltage}) {
		t.Errorf("electric satisfied = %v, want [high_voltage]", electricYes)
	}
	if !equalStrings(electricNo, []string{RequirementCombustion, RequirementSparkIgnition}) {
		t.Errorf("electric unsatisfied = %v, want [combustion spark_ignition]", electricNo)
	}

	// Silence: a requirement we cannot answer appears in neither list, so the
	// caller leaves those plans alone. Null and an unknown fuel word are the same.
	for _, fuel := range []*string{nil, p("hidrogenio")} {
		yes, no := PowertrainFor(fuel).requirementsByVerdict()
		if len(yes) != 0 || len(no) != 0 {
			t.Errorf("unknown powertrain split %v into yes=%v no=%v; both must be empty",
				fuel, yes, no)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
