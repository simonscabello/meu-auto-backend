package catalog

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

// Everything in this file is pure: no context, no database, no clock. It is the layer
// where the provider's vocabulary becomes ours, and it is pure so the mapping can be
// tested exhaustively without a fake HTTP server — the same reason
// internal/maintenance/due.go is.
//
// The provider sends prices as "R$ 70.470,00", reference months as "agosto de 2026", year
// codes as "2017-6" and fuel as "Híbrido". None of those shapes is allowed past this file.

// zeroKmYearCode is the provider's pseudo-year for a brand new vehicle. It is a price
// bucket, not a model year, so it becomes a NULL year rather than the year 32000.
const zeroKmYearCode = 32000

// errUnparseable is internal. Callers get a typed decision — a nil year, a nil fuel — or
// an explicit failure from parsePriceCents and parseReferenceMonth, which are the two
// that cannot sensibly guess.
var errUnparseable = errors.New("catalog: cannot parse provider value")

// portugueseMonths maps the provider's month names. Written out rather than derived from
// a locale: Go's time package has no Portuguese month names, and pulling in a
// localisation library to read twelve words would be a dependency for nothing.
var portugueseMonths = map[string]time.Month{
	"janeiro": time.January, "fevereiro": time.February, "marco": time.March,
	"abril": time.April, "maio": time.May, "junho": time.June,
	"julho": time.July, "agosto": time.August, "setembro": time.September,
	"outubro": time.October, "novembro": time.November, "dezembro": time.December,
}

// fuelTypeByLabel translates the provider's fuel word into the vocabulary
// vehicles.fuel_type accepts.
//
// Keys are already accent-stripped and lowercased by foldAccents. A word that is not here
// yields no fuel type at all rather than a wrong one: the app then shows the provider's
// label and the owner picks the fuel themselves, which is a smaller failure than filing a
// diesel car as petrol.
var fuelTypeByLabel = map[string]string{
	"gasolina": "gasolina",
	"alcool":   "etanol",
	"etanol":   "etanol",
	"diesel":   "diesel",
	"flex":     "flex",
	"hibrido":  "hibrido",
	"eletrico": "eletrico",
	"gnv":      "gnv",
}

// accentFolder strips the accents that appear in the provider's fuel and month words.
//
// Deliberately not a Unicode normalisation pass: the input is a closed set of Portuguese
// words, and a table of the eleven letters that actually occur is easier to read, cannot
// surprise anyone, and adds no dependency.
var accentFolder = strings.NewReplacer(
	"á", "a", "à", "a", "â", "a", "ã", "a",
	"é", "e", "ê", "e",
	"í", "i",
	"ó", "o", "ô", "o", "õ", "o",
	"ú", "u", "ü", "u",
	"ç", "c",
)

func foldAccents(raw string) string {
	return accentFolder.Replace(strings.ToLower(strings.TrimSpace(raw)))
}

// parseYearCode reads the model year out of a provider year code such as "2017-6".
//
// A nil result is a legitimate outcome, not a failure: the provider's "32000" bucket has
// no model year, and neither does a code in a shape we do not recognise. The code itself
// is stored verbatim either way, so nothing is lost — only the sort key.
func parseYearCode(code string) *int32 {
	digits, _, _ := strings.Cut(strings.TrimSpace(code), "-")

	year, err := strconv.Atoi(digits)
	if err != nil || year == zeroKmYearCode {
		return nil
	}
	// The column's CHECK is the same range. Returning something it would reject would
	// turn a provider oddity into a failed sync for the whole model.
	if year < 1900 || year > 2100 {
		return nil
	}

	parsed := int32(year)
	return &parsed
}

// parseYearLabel splits "2017 Híbrido" into the fuel word the provider displays.
//
// The year half is ignored here — parseYearCode is the authority on that, because the code
// is what the provider keys on and the label is what it prints.
func parseYearLabel(name string) (fuelLabel string) {
	_, fuel, found := strings.Cut(strings.TrimSpace(name), " ")
	if !found {
		return ""
	}
	return strings.TrimSpace(fuel)
}

// fuelTypeFor maps a provider fuel word to ours, or "" when there is no equivalent.
func fuelTypeFor(fuelLabel string) string {
	return fuelTypeByLabel[foldAccents(fuelLabel)]
}

// parsePriceCents reads "R$ 70.470,00" as 7047000.
//
// Brazilian formatting: "." groups thousands and "," separates centavos, which is the
// reverse of what strconv.ParseFloat expects — and a float is the wrong destination
// anyway. Money is an integer of centavos everywhere in this schema, so the string is
// taken apart by hand and never passes through a float at all.
func parsePriceCents(raw string) (int64, error) {
	// Everything that is not a digit or a separator goes: the currency symbol, ordinary
	// spaces, and the non-breaking space the provider sometimes emits after "R$".
	var cleaned strings.Builder
	for _, r := range raw {
		if (r >= '0' && r <= '9') || r == ',' || r == '.' {
			cleaned.WriteRune(r)
		}
	}

	reais, centavos, hasCentavos := strings.Cut(cleaned.String(), ",")
	reais = strings.ReplaceAll(reais, ".", "")
	if reais == "" {
		return 0, errUnparseable
	}

	whole, err := strconv.ParseInt(reais, 10, 64)
	if err != nil {
		return 0, errUnparseable
	}

	fraction := int64(0)
	if hasCentavos {
		// "R$ 10,5" is half of a real, not five centavos; "10,505" is not a price the
		// provider emits, and truncating is the only reading that cannot invent money.
		centavos = (centavos + "00")[:2]
		if fraction, err = strconv.ParseInt(centavos, 10, 64); err != nil {
			return 0, errUnparseable
		}
	}

	return whole*100 + fraction, nil
}

// parseReferenceMonth reads "agosto de 2026" — or "agosto/2026", which the same provider
// uses on a different endpoint — as the first day of that month.
//
// The first day, not the day it was published: the value is a month, and anchoring it to
// day 1 makes it a civil date that sorts, groups and subtracts correctly.
func parseReferenceMonth(raw string) (time.Time, error) {
	normalised := foldAccents(raw)
	normalised = strings.NewReplacer("/", " ", " de ", " ").Replace(normalised)

	parts := strings.Fields(normalised)
	if len(parts) != 2 {
		return time.Time{}, errUnparseable
	}

	month, ok := portugueseMonths[parts[0]]
	if !ok {
		return time.Time{}, errUnparseable
	}

	year, err := strconv.Atoi(parts[1])
	if err != nil || year < 1900 || year > 2100 {
		return time.Time{}, errUnparseable
	}

	// Midnight UTC, like every other civil date in this codebase — it round trips through
	// a Postgres `date` column unchanged (internal/platform/civil).
	return time.Date(year, month, 1, 0, 0, 0, 0, time.UTC), nil
}
