package maintenance

// What a vehicle must HAVE for a catalogue item to exist on it.
//
// This is the definition of a part, not a maintenance rule. A fuel filter needs a fuel
// system; a spark plug needs spark ignition; a traction battery needs a high voltage
// system. Nothing here knows about a make, a model or an interval, and it must not grow to
// — that would be the universal automotive database this project deliberately is not
// building.
const (
	RequirementAny           = "any"
	RequirementCombustion    = "combustion"
	RequirementSparkIgnition = "spark_ignition"
	RequirementHighVoltage   = "high_voltage"
)

// Applicability is the verdict for one item on one vehicle.
//
// Three values, not two, and the third is the important one: "we do not know" is a real
// answer and must never collapse into either "yes" or "no". A vehicle whose fuel type
// nobody filled in gets silence, not a guess.
type Applicability int

const (
	ApplicabilityUnknown Applicability = iota
	ApplicabilityYes
	ApplicabilityNo
)

// Powertrain is what a fuel type tells us about how a vehicle is built.
//
// It is the ONLY automatic source of applicability in this system. Everything else — belt
// versus chain, which fluids a particular gearbox takes — is asked, never inferred, because
// there is no reliable data behind an inference and a wrong one produces exactly the false
// recommendation this module exists to prevent.
type Powertrain struct {
	// known is false when the vehicle has no fuel type at all. Every requirement other
	// than "any" then answers ApplicabilityUnknown.
	known bool

	combustion    bool
	sparkIgnition bool
	highVoltage   bool
}

// PowertrainFor reads a vehicle's fuel type. nil means the owner never said.
//
// The mapping is the meaning of the words, not a specification:
//   - a diesel burns fuel by compression, so it has no spark plugs. It has glow plugs,
//     which are a different part and a different item.
//   - an electric has no internal combustion engine at all, so no engine oil, no oil or
//     air filter, no fuel filter, no timing belt.
//   - a hybrid has both. `hibrido` covers plug-in hybrids too: the vehicle vocabulary has
//     one value and the components are the same ones.
func PowertrainFor(fuelType *string) Powertrain {
	if fuelType == nil {
		return Powertrain{}
	}

	switch *fuelType {
	case "flex", "gasolina", "etanol", "gnv":
		return Powertrain{known: true, combustion: true, sparkIgnition: true}
	case "diesel":
		return Powertrain{known: true, combustion: true}
	case "hibrido":
		return Powertrain{known: true, combustion: true, sparkIgnition: true, highVoltage: true}
	case "eletrico":
		return Powertrain{known: true, highVoltage: true}
	default:
		// A fuel word this build does not know. Unknown, not "no" — a future value must
		// not silently delete somebody's oil change.
		return Powertrain{}
	}
}

// Known reports whether the vehicle's fuel type told us anything at all.
func (p Powertrain) Known() bool { return p.known }

// HasCombustionEngine is what decides whether the timing question is worth asking.
func (p Powertrain) HasCombustionEngine() bool { return p.combustion }

// Applies answers whether an item with this requirement exists on this vehicle.
func (p Powertrain) Applies(requirement string) Applicability {
	if requirement == RequirementAny || requirement == "" {
		return ApplicabilityYes
	}
	if !p.known {
		return ApplicabilityUnknown
	}

	var has bool
	switch requirement {
	case RequirementCombustion:
		has = p.combustion
	case RequirementSparkIgnition:
		has = p.sparkIgnition
	case RequirementHighVoltage:
		has = p.highVoltage
	default:
		// An unrecognised requirement is a data problem, not a licence to decide. Say
		// nothing rather than hide an item somebody may need.
		return ApplicabilityUnknown
	}

	if has {
		return ApplicabilityYes
	}
	return ApplicabilityNo
}

// requirementsByVerdict splits the requirement vocabulary into what this vehicle has and
// what it does not.
//
// A requirement the powertrain cannot answer for appears in NEITHER list, which is what
// makes the caller leave those plans alone. RequirementAny is in neither either: it applies
// to everything and is never a reason to change a row.
func (p Powertrain) requirementsByVerdict() (satisfied, unsatisfied []string) {
	for _, requirement := range []string{
		RequirementCombustion, RequirementSparkIgnition, RequirementHighVoltage,
	} {
		switch p.Applies(requirement) {
		case ApplicabilityYes:
			satisfied = append(satisfied, requirement)
		case ApplicabilityNo:
			unsatisfied = append(unsatisfied, requirement)
		}
	}
	return satisfied, unsatisfied
}
