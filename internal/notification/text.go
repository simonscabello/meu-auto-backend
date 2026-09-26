package notification

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The words of a reminder.
//
// They are written here, on the server, because the phone draws a push itself — the app
// is not running to phrase it. Each function below mirrors one in the app
// (lib/core/domain/phrases.dart, Vehicle.shortName) so that the lock screen and the screen
// the tap opens say the same thing in the same words. Change one, change the other.

const (
	nbsp   = " "
	dotSep = nbsp + "· "

	// Past this many days a count stops meaning anything and becomes months — the app's
	// _phraseDaysLimit.
	phraseDaysLimit = 45
)

// lineFor is one item as a line of the notification: "IPVA 2026 · Vence hoje", the same
// shape Início gives the items that need attention.
func lineFor(item reminder) string {
	return item.alert.Title + dotSep + phraseFor(item)
}

func phraseFor(item reminder) string {
	a := item.alert
	if a.Kind == kindWarranty {
		return warrantyPhrase(a.RemainingKm, a.RemainingDays)
	}
	if item.stage == stageDueToday {
		return "Vence hoje"
	}
	if phrase := urgencyPhrase(a.RemainingKm, a.RemainingDays); phrase != "" {
		return phrase
	}
	if item.stage == stageOverdue {
		return "Vencido"
	}
	return "Vence em breve"
}

// warrantyPhrase: a warranty does not fall due, it runs out — and a reminder about one is
// only ever sent while there is still time to go back to the workshop.
func warrantyPhrase(remainingKm, remainingDays *int32) string {
	if remainingDays != nil {
		switch days := *remainingDays; {
		case days <= 0:
			return "Garantia acaba hoje"
		case days == 1:
			return "Garantia acaba amanhã"
		default:
			return fmt.Sprintf("Garantia acaba em %d dias", days)
		}
	}
	if remainingKm != nil && *remainingKm > 0 {
		return "Faltam " + formatKm(int(*remainingKm)) + " de garantia"
	}
	return "Garantia acabando"
}

// urgencyPhrase mirrors the app's urgencyPhrase: the dimension that decides leads, and the
// other follows only when it is late too.
func urgencyPhrase(remainingKm, remainingDays *int32) string {
	switch {
	case remainingDays == nil && remainingKm == nil:
		return ""
	case remainingKm == nil:
		return dueInDaysPhrase(int(*remainingDays))
	case remainingDays == nil:
		return capitalizeFirst(remainingKmPhrase(int(*remainingKm)))
	}
	days, km := int(*remainingDays), int(*remainingKm)
	daysLate, kmLate := days < 0, km < 0
	switch {
	case daysLate && kmLate:
		return dueInDaysPhrase(days) + dotSep + remainingKmPhrase(km)
	case kmLate:
		return capitalizeFirst(remainingKmPhrase(km))
	case daysLate:
		return dueInDaysPhrase(days)
	}
	dayWord := strconv.Itoa(days) + " dias"
	if days == 1 {
		dayWord = "1 dia"
	}
	return "Faltam " + formatKm(km) + " ou " + dayWord
}

func remainingKmPhrase(km int) string {
	switch {
	case km > 0:
		return "faltam " + formatKm(km)
	case km == 0:
		return "vence agora"
	default:
		return "passou " + formatKm(-km)
	}
}

func remainingDaysPhrase(days int) string {
	switch days {
	case 0:
		return "vence hoje"
	case 1:
		return "vence amanhã"
	case -1:
		return "venceu ontem"
	}

	distance := days
	if distance < 0 {
		distance = -distance
	}
	if distance <= phraseDaysLimit {
		if days > 0 {
			return fmt.Sprintf("faltam %d dias", distance)
		}
		return fmt.Sprintf("venceu há %d dias", distance)
	}

	months := approximateMonths(distance)
	if months >= 24 {
		years := months / 12
		if days > 0 {
			return fmt.Sprintf("faltam mais de %d anos", years)
		}
		return fmt.Sprintf("venceu há mais de %d anos", years)
	}
	unit := "meses"
	if months == 1 {
		unit = "mês"
	}
	if days > 0 {
		return fmt.Sprintf("faltam cerca de %d %s", months, unit)
	}
	return fmt.Sprintf("venceu há cerca de %d %s", months, unit)
}

// dueInDaysPhrase mirrors the app's: "Venceu há 13 dias", "Vence hoje", "Vence em 21 dias".
func dueInDaysPhrase(days int) string {
	if days > 1 && days <= phraseDaysLimit {
		return fmt.Sprintf("Vence em %d dias", days)
	}
	if days > phraseDaysLimit {
		months := approximateMonths(days)
		switch {
		case months >= 24:
			return fmt.Sprintf("Vence em cerca de %d anos", months/12)
		case months == 1:
			return "Vence em cerca de 1 mês"
		default:
			return fmt.Sprintf("Vence em cerca de %d meses", months)
		}
	}
	return capitalizeFirst(remainingDaysPhrase(days))
}

// approximateMonths turns a day count into a coarse month figure for copy. Not calendar
// arithmetic, exactly like the app's _approximateMonths.
func approximateMonths(days int) int {
	months := (days + 15) / 30
	if months < 1 {
		return 1
	}
	return months
}

// formatKm writes a distance the Brazilian way, "1.200 km", with the unit held to the
// number.
func formatKm(km int) string {
	digits := strconv.Itoa(km)
	var grouped strings.Builder
	for i, r := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			grouped.WriteByte('.')
		}
		grouped.WriteRune(r)
	}
	return grouped.String() + nbsp + "km"
}

func capitalizeFirst(text string) string {
	r, size := utf8.DecodeRuneInString(text)
	if r == utf8.RuneError {
		return text
	}
	return string(unicode.ToUpper(r)) + text[size:]
}

// moreLine closes a notification that names only the first few items.
func moreLine(rest int) string {
	if rest == 1 {
		return "E mais 1 item."
	}
	return fmt.Sprintf("E mais %d itens.", rest)
}

var (
	spaces = regexp.MustCompile(`\s+`)
	// A word that starts the catalogue's specification: "1.8", "(Híbrido)", "16V".
	specStart = regexp.MustCompile(`^[0-9(]`)
	digit     = regexp.MustCompile(`[0-9]`)
)

// vehicleName is the car as the switcher names it — the app's Vehicle.shortName: the
// nickname, or the brand and the model without the FIPE specification. "Toyota Prius",
// not "Toyota PRIUS 1.8 16V 5p Aut. (Híbrido)".
func vehicleName(brand, model string, nickname *string) string {
	if nickname != nil {
		if nick := strings.TrimSpace(*nickname); nick != "" {
			return nick
		}
	}
	short := shortModelName(model)
	if short == "" {
		return brand
	}
	return brand + " " + short
}

func shortModelName(model string) string {
	var words []string
	for _, raw := range spaces.Split(strings.TrimSpace(model), -1) {
		if raw == "" {
			continue
		}
		if specStart.MatchString(raw) || strings.Contains(raw, ".") {
			break
		}
		words = append(words, asName(raw))
		if len(words) == 2 {
			break
		}
	}
	return strings.Join(words, " ")
}

// asName lowers a catalogue word written in capitals — "PRIUS" to "Prius" — and leaves a
// word that is not shouting, or that carries a digit ("HB20"), as it is.
func asName(word string) string {
	shouting := utf8.RuneCountInString(word) > 2 &&
		word == strings.ToUpper(word) &&
		!digit.MatchString(word)
	if !shouting {
		return word
	}
	parts := strings.Split(word, "-")
	for i, part := range parts {
		if part == "" {
			continue
		}
		r, size := utf8.DecodeRuneInString(part)
		parts[i] = string(unicode.ToUpper(r)) + strings.ToLower(part[size:])
	}
	return strings.Join(parts, "-")
}
