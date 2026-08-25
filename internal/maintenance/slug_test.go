package maintenance

import "testing"

func TestSlugify(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"Troca de óleo do motor":         "troca_de_oleo_do_motor",
		"Revisão da suspensão dianteira": "revisao_da_suspensao_dianteira",
		"Filtro de ar-condicionado":      "filtro_de_ar_condicionado",
		"  Correia   Dentada  ":          "correia_dentada",
		"Balanceamento 4 rodas":          "balanceamento_4_rodas",
		"Manutenção":                     "manutencao",
		"!!!":                            "",
	}

	for name, want := range cases {
		if got := slugify(name); got != want {
			t.Errorf("slugify(%q) = %q, want %q", name, got, want)
		}
	}
}
