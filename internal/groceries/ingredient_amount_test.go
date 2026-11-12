package groceries

import (
	"testing"

	"golang.org/x/text/language"
)

func TestParseIngredientAmount(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	cases := []struct {
		in   string
		q    *float64
		unit string
		text string
		note string
	}{
		{"600 g gemischt", f(600), "g", "600 g", "gemischt"},
		{"500 g, mager", f(500), "g", "500 g", "mager"},
		{"1 TL, rot", f(1), "tsp", "1 TL", "rot"},
		{"2 TL getrocknet", f(2), "tsp", "2 TL", "getrocknet"},
		{"1 Dose (400 g)", f(400), "g", "400 g", ""},
		{"800 g (2 Dosen)", f(800), "g", "800 g", "(2 Dosen)"},
		{"1 (ca. 400 g)", f(400), "g", "ca. 400 g", ""},
		{"1 (ca. 1,5 kg)", f(1.5), "kg", "ca. 1,5 kg", ""},
		{"1 Dose (Bio)", f(1), "can", "1 Dose", "(Bio)"},
		{"1 groß", f(1), "", "1", "groß"},
		{"1/2 kleine, rot", f(0.5), "", "1/2", "kleine, rot"},
		{"3, orange", f(3), "", "3", "orange"},
		{"125 g Mini", f(125), "g", "125 g", "Mini"},
		{"ca. 80 g", f(80), "g", "ca. 80 g", ""},
		{"2–3 EL", f(2.5), "tbsp", "2–3 EL", ""},
		{"1-2 EL", f(1.5), "tbsp", "1-2 EL", ""},
		{"2–3 EL Saft", f(2.5), "tbsp", "2–3 EL", "Saft"},
		{"4–6 EL, vegan", f(5), "tbsp", "4–6 EL", "vegan"},
		{"1,5 kg", f(1.5), "kg", "1,5 kg", ""},
		{"1,5 kg, mehlig", f(1.5), "kg", "1,5 kg", "mehlig"},
		{"150 ml", f(150), "ml", "150 ml", ""},
		{"2 cm", nil, "", "2 cm", ""},
		{"2 Stangen", nil, "", "2 Stangen", ""},
		{"1 Msp.", nil, "", "1 Msp.", ""},
		{"nach Geschmack", nil, "", "nach Geschmack", ""},
		{"zum Braten", nil, "", "zum Braten", ""},
		{"frisch, gehackt", nil, "", "frisch, gehackt", ""},
		{"", nil, "", "", ""},
	}
	for _, c := range cases {
		a, note := ParseIngredientAmount(c.in, language.German)
		if (a.Quantity == nil) != (c.q == nil) || (c.q != nil && *a.Quantity != *c.q) || a.Unit != c.unit || a.Text != c.text || note != c.note {
			t.Errorf("%q = {%v %q %q} %q, want {%v %q %q} %q", c.in, a.Quantity, a.Unit, a.Text, note, c.q, c.unit, c.text, c.note)
		}
	}
}
