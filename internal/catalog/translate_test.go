package catalog

import (
	"testing"
	"time"
)

// The translation layer is where a supplier's formatting becomes our data, and it is the
// place a silent mistake is most expensive: a misparsed price is a wrong number shown next
// to somebody's car, and a misparsed month files it under the wrong point in history.
//
// It is pure, so every case here runs without a database, a server or a clock.

func TestParsePriceCents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		want    int64
		wantErr bool
	}{
		{"the shape the provider actually sends", "R$ 70.470,00", 7_047_000, false},
		{"under a thousand, no grouping", "R$ 990,00", 99_000, false},
		{"millions, two grouping separators", "R$ 1.234.567,89", 123_456_789, false},
		{"centavos that are not zero", "R$ 10.690,55", 1_069_055, false},

		// The provider emits a non-breaking space after "R$" on some responses. It is
		// invisible in a diff and would silently fail a naive TrimPrefix.
		{"non-breaking space after the symbol", "R$ 70.470,00", 7_047_000, false},

		{"no centavos at all", "R$ 12.000", 1_200_000, false},
		{"one decimal digit is tenths, not centavos", "R$ 10,5", 1_050, false},
		{"no currency symbol", "70.470,00", 7_047_000, false},
		{"zero", "R$ 0,00", 0, false},

		{"empty", "", 0, true},
		{"no digits", "R$ --", 0, true},
		{"not a price at all", "consulte", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parsePriceCents(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parsePriceCents(%q) = %d, want an error", tt.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parsePriceCents(%q): unexpected error: %v", tt.raw, err)
			}
			if got != tt.want {
				t.Errorf("parsePriceCents(%q) = %d, want %d", tt.raw, got, tt.want)
			}
		})
	}
}

func TestParseReferenceMonth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		want    time.Time
		wantErr bool
	}{
		// Both forms are real and come from the SAME provider on different endpoints: the
		// detail says "agosto de 2026" and the references list says "agosto/2026".
		{"detail endpoint form", "agosto de 2026", date(2026, 8), false},
		{"references endpoint form", "agosto/2026", date(2026, 8), false},

		{"accented month", "março de 2026", date(2026, 3), false},
		{"capitalised", "Janeiro de 2025", date(2025, 1), false},
		{"surrounding whitespace", "  dezembro de 2024 ", date(2024, 12), false},

		{"month we do not know", "smarch de 2026", time.Time{}, true},
		{"year out of range", "agosto de 1600", time.Time{}, true},
		{"not a month at all", "consulte a tabela", time.Time{}, true},
		{"empty", "", time.Time{}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseReferenceMonth(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseReferenceMonth(%q) = %s, want an error", tt.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseReferenceMonth(%q): unexpected error: %v", tt.raw, err)
			}
			if !got.Equal(tt.want) {
				t.Errorf("parseReferenceMonth(%q) = %s, want %s", tt.raw, got, tt.want)
			}
			// A civil date or it does not round trip through a Postgres `date` column.
			if got.Location() != time.UTC {
				t.Errorf("parseReferenceMonth(%q) is in %s, want UTC", tt.raw, got.Location())
			}
		})
	}
}

func TestParseYearCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		code string
		want *int32
	}{
		{"year and fuel code", "2017-6", ptr(int32(2017))},
		{"diesel", "2012-3", ptr(int32(2012))},

		// The provider's bucket for a brand new vehicle. It is a price category, not a
		// model year, and storing 32000 would put it at the top of every sort forever.
		{"zero kilometre pseudo-year", "32000-1", nil},

		{"no fuel suffix", "2020", ptr(int32(2020))},
		{"not a number", "abc-1", nil},
		{"empty", "", nil},
		{"outside the column's range", "1500-1", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := parseYearCode(tt.code)
			switch {
			case tt.want == nil && got != nil:
				t.Fatalf("parseYearCode(%q) = %d, want nil", tt.code, *got)
			case tt.want != nil && got == nil:
				t.Fatalf("parseYearCode(%q) = nil, want %d", tt.code, *tt.want)
			case tt.want != nil && *got != *tt.want:
				t.Errorf("parseYearCode(%q) = %d, want %d", tt.code, *got, *tt.want)
			}
		})
	}
}

// TestFuelMapping is the one that keeps the app from having to know a supplier's
// vocabulary. Each provider word must land on a value vehicles.fuel_type accepts, and an
// unknown word must produce nothing rather than a wrong guess.
func TestFuelMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		yearName  string
		wantLabel string
		wantType  string
	}{
		{"2017 Híbrido", "Híbrido", "hibrido"},
		{"2012 Diesel", "Diesel", "diesel"},
		{"2020 Gasolina", "Gasolina", "gasolina"},
		{"2015 Álcool", "Álcool", "etanol"},
		{"2022 Flex", "Flex", "flex"},
		{"2023 Elétrico", "Elétrico", "eletrico"},

		// A word with no equivalent yields no fuel type. The owner then picks the fuel
		// themselves, which is a far smaller failure than filing a diesel car as petrol.
		{"2019 Vapor", "Vapor", ""},

		// A name with no fuel half at all. Nothing to display, nothing to map.
		{"2019", "", ""},
	}

	// Every mapped value must be one the vehicle module's own validation accepts. This is
	// the seam where the two vocabularies meet, and the constants live in two packages by
	// design — so the agreement is asserted rather than assumed.
	allowed := map[string]bool{
		"flex": true, "gasolina": true, "etanol": true, "diesel": true,
		"gnv": true, "eletrico": true, "hibrido": true,
	}

	for _, tt := range tests {
		t.Run(tt.yearName, func(t *testing.T) {
			t.Parallel()

			label := parseYearLabel(tt.yearName)
			if label != tt.wantLabel {
				t.Errorf("parseYearLabel(%q) = %q, want %q", tt.yearName, label, tt.wantLabel)
			}

			fuelType := fuelTypeFor(label)
			if fuelType != tt.wantType {
				t.Errorf("fuelTypeFor(%q) = %q, want %q", label, fuelType, tt.wantType)
			}
			if fuelType != "" && !allowed[fuelType] {
				t.Errorf("fuelTypeFor(%q) = %q, which vehicles.fuel_type would reject",
					label, fuelType)
			}
		})
	}
}

func date(year int, month time.Month) time.Time {
	return time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
}

func ptr[T any](v T) *T { return &v }
