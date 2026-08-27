package abastecimento

import (
	"errors"
	"testing"

	"github.com/simonscabello/meu-auto-backend/internal/platform/apperr"
)

func TestRefuelingForEachFuelType(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		fuel      *string
		supported bool
		fuels     []string
	}{
		{"flex", p("flex"), true, []string{"gasolina", "etanol"}},
		{"gasolina", p("gasolina"), true, []string{"gasolina"}},
		{"etanol", p("etanol"), true, []string{"etanol"}},
		{"diesel", p("diesel"), true, []string{"diesel"}},
		{"gnv", p("gnv"), true, []string{"gnv", "gasolina"}},
		{"hibrido", p("hibrido"), true, []string{"gasolina", "etanol"}},
		{"eletrico", p("eletrico"), false, []string{}},
		{"null", nil, true, []string{"gasolina", "etanol", "diesel", "gnv"}},
		{"unknown", p("hidrogenio"), true, []string{"gasolina", "etanol", "diesel", "gnv"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := RefuelingFor(tc.fuel)
			if got.Supported != tc.supported {
				t.Errorf("Supported = %v, want %v", got.Supported, tc.supported)
			}
			if !equalStrings(got.FuelTypes, tc.fuels) {
				t.Errorf("FuelTypes = %v, want %v", got.FuelTypes, tc.fuels)
			}
			if got.FuelTypes == nil {
				t.Error("FuelTypes is nil, want an empty slice so JSON is [] not null")
			}
		})
	}
}

func TestUnknownAndNullNeverRefuse(t *testing.T) {
	t.Parallel()

	for _, fuel := range []*string{nil, p("hidrogenio")} {
		got := RefuelingFor(fuel)
		if !got.Supported {
			t.Errorf("RefuelingFor(%v) refused; unknown must not become no", fuel)
		}
		if len(got.FuelTypes) == 0 {
			t.Errorf("RefuelingFor(%v) returned an empty list; unknown must be permissive", fuel)
		}
	}
}

func TestErrIfFuelRejectedElectric(t *testing.T) {
	t.Parallel()

	err := errIfFuelRejected(FuelGasolina, p("eletrico"), "Não foi possível registrar o abastecimento.")
	if fieldErrors(t, err)["fuel"] != "Este veículo não aceita abastecimento." {
		t.Errorf("electric: %v", fieldErrors(t, err)["fuel"])
	}
}

func TestErrIfFuelRejectedOutsideVehicleList(t *testing.T) {
	t.Parallel()

	err := errIfFuelRejected(FuelDiesel, p("flex"), "Não foi possível registrar o abastecimento.")
	if fieldErrors(t, err)["fuel"] != "Combustível não aceito para este veículo." {
		t.Errorf("diesel on flex: %v", fieldErrors(t, err)["fuel"])
	}
}

func TestErrIfFuelRejectedAllowsPermissiveWhenFuelTypeIsNull(t *testing.T) {
	t.Parallel()

	if err := errIfFuelRejected(FuelDiesel, nil, "Não foi possível registrar o abastecimento."); err != nil {
		t.Errorf("null fuel_type rejected diesel: %v", err)
	}
}

func TestCapabilityPortMatchesRefuelingFor(t *testing.T) {
	t.Parallel()

	supported, fuels := Capability{}.Capability(p("flex"))
	want := RefuelingFor(p("flex"))
	if supported != want.Supported || !equalStrings(fuels, want.FuelTypes) {
		t.Errorf("Capability() = (%v, %v), want (%v, %v)",
			supported, fuels, want.Supported, want.FuelTypes)
	}
}

func fieldErrors(t *testing.T, err error) map[string]any {
	t.Helper()
	if err == nil {
		return nil
	}
	var appErr *apperr.Error
	if !errors.As(err, &appErr) {
		t.Fatalf("error is %T, want *apperr.Error", err)
	}
	fields, _ := appErr.Details["fields"].(map[string]any)
	return fields
}

func p[T any](v T) *T { return &v }

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
