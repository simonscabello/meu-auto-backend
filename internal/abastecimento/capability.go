package abastecimento

import "github.com/simonscabello/meu-auto-backend/internal/platform/validate"

// Refueling is what a vehicle's fuel_type says it can burn.
//
// It is the only automatic source of this answer. Nothing here knows about a table of
// abastecimentos, a make or a model — the same discipline as maintenance/powertrain.go.
// Unknown never becomes "no": a missing or future fuel word must not refuse a fill.
type Refueling struct {
	Supported bool
	FuelTypes []string
}

var (
	fuelsGasolinaEtanol = []string{FuelGasolina, FuelEtanol}
	fuelsGasolina       = []string{FuelGasolina}
	fuelsEtanol         = []string{FuelEtanol}
	fuelsDiesel         = []string{FuelDiesel}
	fuelsGNV            = []string{FuelGNV, FuelGasolina}
	fuelsPermissive     = []string{FuelGasolina, FuelEtanol, FuelDiesel, FuelGNV}
	fuelsNone           = []string{}
)

// RefuelingFor reads a vehicle's fuel type. nil means the owner never said.
func RefuelingFor(fuelType *string) Refueling {
	if fuelType == nil {
		return Refueling{Supported: true, FuelTypes: copyFuels(fuelsPermissive)}
	}

	switch *fuelType {
	case "flex":
		return Refueling{Supported: true, FuelTypes: copyFuels(fuelsGasolinaEtanol)}
	case "gasolina":
		return Refueling{Supported: true, FuelTypes: copyFuels(fuelsGasolina)}
	case "etanol":
		return Refueling{Supported: true, FuelTypes: copyFuels(fuelsEtanol)}
	case "diesel":
		return Refueling{Supported: true, FuelTypes: copyFuels(fuelsDiesel)}
	case "gnv":
		return Refueling{Supported: true, FuelTypes: copyFuels(fuelsGNV)}
	case "hibrido":
		return Refueling{Supported: true, FuelTypes: copyFuels(fuelsGasolinaEtanol)}
	case "eletrico":
		return Refueling{Supported: false, FuelTypes: copyFuels(fuelsNone)}
	default:
		return Refueling{Supported: true, FuelTypes: copyFuels(fuelsPermissive)}
	}
}

func copyFuels(fuels []string) []string {
	out := make([]string, len(fuels))
	copy(out, fuels)
	return out
}

// Capability is the port vehicle needs: a value type because the answer is a pure
// function of fuel_type. Primitive returns, no shared struct — vehicle.RefuelingPort.
type Capability struct{}

func (Capability) Capability(fuelType *string) (supported bool, fuelTypes []string) {
	r := RefuelingFor(fuelType)
	return r.Supported, r.FuelTypes
}

func errIfFuelRejected(fuel string, vehicleFuelType *string, message string) error {
	capability := RefuelingFor(vehicleFuelType)
	if !capability.Supported {
		errs := validate.New()
		errs.Add("fuel", "Este veículo não aceita abastecimento.")
		return errs.Err(message)
	}
	for _, allowed := range capability.FuelTypes {
		if fuel == allowed {
			return nil
		}
	}
	errs := validate.New()
	errs.Add("fuel", "Combustível não aceito para este veículo.")
	return errs.Err(message)
}
