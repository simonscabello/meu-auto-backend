package notification

import (
	"testing"

	"github.com/google/uuid"
)

func ptr[T any](v T) *T { return &v }

// The same fact must read the same on the lock screen and on the screen the tap opens, so
// these mirror the app's phrases (lib/core/domain/phrases.dart) case for case.
func TestPhrasesMatchTheApp(t *testing.T) {
	cases := []struct {
		name string
		km   *int32
		days *int32
		want string
	}{
		{"today", nil, ptr[int32](0), "Vence hoje"},
		{"tomorrow", nil, ptr[int32](1), "Vence amanhã"},
		{"in days", nil, ptr[int32](21), "Vence em 21 dias"},
		{"yesterday", nil, ptr[int32](-1), "Venceu ontem"},
		{"days late", nil, ptr[int32](-13), "Venceu há 13 dias"},
		{"months late", nil, ptr[int32](-100), "Venceu há cerca de 3 meses"},
		{"years late", nil, ptr[int32](-800), "Venceu há mais de 2 anos"},
		{"km to go", ptr[int32](800), nil, "Faltam 800 km"},
		{"km late", ptr[int32](-1200), nil, "Passou 1.200 km"},
		{"both close", ptr[int32](800), ptr[int32](12), "Faltam 800 km ou 12 dias"},
		{"one day", ptr[int32](800), ptr[int32](1), "Faltam 800 km ou 1 dia"},
		{"km decides", ptr[int32](-50), ptr[int32](30), "Passou 50 km"},
		{"days decide", ptr[int32](3000), ptr[int32](-2), "Venceu há 2 dias"},
		{"both late", ptr[int32](-10), ptr[int32](-2), "Venceu há 2 dias · passou 10 km"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := urgencyPhrase(tc.km, tc.days); got != tc.want {
				t.Errorf("urgencyPhrase = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestKilometresAreWrittenTheBrazilianWay(t *testing.T) {
	for km, want := range map[int]string{
		0:       "0 km",
		950:     "950 km",
		1200:    "1.200 km",
		45000:   "45.000 km",
		1234567: "1.234.567 km",
	} {
		if got := formatKm(km); got != want {
			t.Errorf("formatKm(%d) = %q, want %q", km, got, want)
		}
	}
}

func TestAWarrantyRunsOutRatherThanFallsDue(t *testing.T) {
	cases := map[string]struct {
		km, days *int32
		want     string
	}{
		"days":     {nil, ptr[int32](12), "Garantia acaba em 12 dias"},
		"tomorrow": {nil, ptr[int32](1), "Garantia acaba amanhã"},
		"today":    {nil, ptr[int32](0), "Garantia acaba hoje"},
		"km only":  {ptr[int32](900), nil, "Faltam 900 km de garantia"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := warrantyPhrase(tc.km, tc.days); got != tc.want {
				t.Errorf("warrantyPhrase = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTheCarIsNamedAsTheSwitcherNamesIt(t *testing.T) {
	cases := []struct {
		brand, model string
		nickname     *string
		want         string
	}{
		{"Toyota", "PRIUS 1.8 16V 5p Aut. (Híbrido)", nil, "Toyota Prius"},
		{"Volkswagen", "Gol", nil, "Volkswagen Gol"},
		{"Hyundai", "HB20 1.0 Flex", nil, "Hyundai HB20"},
		{"Fiat", "STRADA ADVENTURE CD 1.8", nil, "Fiat Strada Adventure"},
		{"Chevrolet", "S-10 LTZ 2.8", nil, "Chevrolet S-10 Ltz"},
		{"Toyota", "Corolla", ptr("  Carrinho  "), "Carrinho"},
		{"Toyota", "Corolla", ptr("   "), "Toyota Corolla"},
		{"Honda", "1.5 (spec only)", nil, "Honda"},
	}
	for _, tc := range cases {
		if got := vehicleName(tc.brand, tc.model, tc.nickname); got != tc.want {
			t.Errorf("vehicleName(%q, %q) = %q, want %q", tc.brand, tc.model, got, tc.want)
		}
	}
}

func TestALineIsTheItemAndWhereItStands(t *testing.T) {
	due := func(kind string, s stage, title string, days *int32, km *int32) reminder {
		return reminder{stage: s, alert: Alert{
			Kind: kind, Title: title, RemainingDays: days, RemainingKm: km,
			ReferenceID: uuid.New(),
		}}
	}
	cases := []struct {
		item reminder
		want string
	}{
		{due("ipva", stageDueToday, "IPVA 2026", ptr[int32](0), nil), "IPVA 2026 · Vence hoje"},
		{due("seguro", stageDueSoon, "Seguro", ptr[int32](30), nil), "Seguro · Vence em 30 dias"},
		{due("manutencao", stageOverdue, "Troca de óleo", nil, ptr[int32](-1200)), "Troca de óleo · Passou 1.200 km"},
		{due("cuidado", stageOverdue, "Calibrar os pneus", ptr[int32](-2), nil), "Calibrar os pneus · Venceu há 2 dias"},
		{due("garantia", stageDueSoon, "Pastilhas de freio", ptr[int32](9), nil), "Pastilhas de freio · Garantia acaba em 9 dias"},
		{due("manutencao", stageOverdue, "Revisão", nil, nil), "Revisão · Vencido"},
	}
	for _, tc := range cases {
		if got := lineFor(tc.item); got != tc.want {
			t.Errorf("lineFor = %q, want %q", got, tc.want)
		}
	}
}
