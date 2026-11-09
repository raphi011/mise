package groceries

import (
	"testing"

	"golang.org/x/text/language"
)

func TestParseEntry(t *testing.T) {
	type want struct {
		q          float64 // -1: no quantity
		unit, text string
		name       string
	}
	cases := []struct {
		lang  language.Tag
		input string
		want  want
	}{
		{language.English, "2 lemons", want{2, "", "2", "lemons"}},
		{language.English, "500g cherry tomatoes", want{500, "g", "500g", "cherry tomatoes"}},
		{language.English, "500 g  Cherry   tomatoes ", want{500, "g", "500 g", "Cherry tomatoes"}},
		{language.English, "1.5 l milk", want{1.5, "l", "1.5 l", "milk"}},
		{language.English, "1,5 kg flour", want{1.5, "kg", "1,5 kg", "flour"}},
		{language.English, "1,500 g flour", want{1500, "g", "1,500 g", "flour"}},
		{language.English, "½ bunch parsley", want{0.5, "bunch", "½ bunch", "parsley"}},
		{language.English, "1 1/2 cups flour", want{1.5, "cup", "1 1/2 cups", "flour"}},
		{language.English, "1½ tbsp honey", want{1.5, "tbsp", "1½ tbsp", "honey"}},
		{language.English, "3/4 cup sugar", want{0.75, "cup", "3/4 cup", "sugar"}},
		{language.English, "2x milk", want{2, "", "2x", "milk"}},
		{language.English, "2 x milk", want{2, "", "2 x", "milk"}},
		{language.English, "2 cans", want{2, "", "2", "cans"}},
		{language.English, "Lemons 2", want{2, "", "2", "Lemons"}},
		{language.English, "Milk 1 l", want{1, "l", "1 l", "Milk"}},
		{language.English, "Tomatoes 500g", want{500, "g", "500g", "Tomatoes"}},
		{language.English, "7up", want{-1, "", "", "7up"}},
		{language.English, "milk", want{-1, "", "", "milk"}},
		{language.English, "1 thumb ginger", want{1, "", "1", "thumb ginger"}},
		{language.English, "500g", want{500, "g", "500g", ""}},
		{language.German, "1,5 kg Mehl", want{1.5, "kg", "1,5 kg", "Mehl"}},
		{language.German, "1.000 g Mehl", want{1000, "g", "1.000 g", "Mehl"}},
		{language.German, "1.5 l Milch", want{1.5, "l", "1.5 l", "Milch"}},
		{language.German, "2 EL Sojasoße", want{2, "tbsp", "2 EL", "Sojasoße"}},
		{language.German, "1 TL Zimt", want{1, "tsp", "1 TL", "Zimt"}},
		{language.German, "1 Prise Salz", want{1, "pinch", "1 Prise", "Salz"}},
		{language.German, "½ Bund Petersilie", want{0.5, "bunch", "½ Bund", "Petersilie"}},
		{language.German, "3 Stk. Eier", want{3, "piece", "3 Stk.", "Eier"}},
		{language.German, "2 Dosen Tomaten", want{2, "can", "2 Dosen", "Tomaten"}},
		{language.Make("de-AT"), "0,5 l Obers", want{0.5, "l", "0,5 l", "Obers"}},
		{language.German, "3 Flaschen", want{3, "", "3", "Flaschen"}},
		{language.German, "Mehl 405", want{-1, "", "", "Mehl 405"}},
		{language.German, "Weizenmehl Type 550", want{-1, "", "", "Weizenmehl Type 550"}},
		{language.German, "Eier 10", want{10, "", "10", "Eier"}},
		{language.German, "Mehl 1000 g", want{1000, "g", "1000 g", "Mehl"}},
		{language.German, "2 l", want{2, "l", "2 l", ""}},
		{language.German, "1 Stk.", want{1, "piece", "1 Stk.", ""}},
	}
	for _, c := range cases {
		a, name := ParseEntry(c.input, c.lang)
		got := want{-1, a.Unit, a.Text, name}
		if a.Quantity != nil {
			got.q = *a.Quantity
		}
		if got != c.want {
			t.Errorf("ParseEntry(%q, %s) = %+v, want %+v", c.input, c.lang, got, c.want)
		}
	}
}

func TestParseAmount(t *testing.T) {
	a := ParseAmount(" 400  g ", language.English)
	if a.Quantity == nil || *a.Quantity != 400 || a.Unit != "g" || a.Text != "400 g" {
		t.Errorf("400 g: %+v", a)
	}
	a = ParseAmount("1 thumb", language.English)
	if a.Quantity != nil || a.Text != "1 thumb" {
		t.Errorf("free text: %+v", a)
	}
	if a := ParseAmount("", language.English); !a.IsZero() {
		t.Errorf("empty: %+v", a)
	}
}

func TestFormatAmount(t *testing.T) {
	cases := []struct {
		q    float64
		unit string
		lang language.Tag
		want string
	}{
		{750, "g", language.English, "750 g"},
		{1.5, "tbsp", language.German, "1,5 EL"},
		{2, "cup", language.English, "2 cups"},
		{1, "cup", language.German, "1 Tasse"},
		{5, "", language.English, "5"},
		{1.0 / 3, "", language.English, "0.333"},
	}
	for _, c := range cases {
		if got := FormatAmount(c.q, c.unit, c.lang); got != c.want {
			t.Errorf("FormatAmount(%v, %q, %s) = %q, want %q", c.q, c.unit, c.lang, got, c.want)
		}
	}
}

func TestMerge(t *testing.T) {
	p := func(s string) Amount { return ParseAmount(s, language.English) }
	cases := []struct {
		a, b string
		want string
		ok   bool
	}{
		{"2", "3", "5", true},
		{"500 g", "250g", "750 g", true},
		{"1 l", "500 ml", "", false},
		{"2", "500 g", "", false},
		{"", "2", "2", true},
		{"2", "", "2", true},
		{"1 thumb", "1 thumb", "", false},
	}
	for _, c := range cases {
		got, ok := merge(p(c.a), p(c.b), language.English)
		if ok != c.ok || (ok && got.Text != c.want) {
			t.Errorf("merge(%q, %q) = %q, %v; want %q, %v", c.a, c.b, got.Text, ok, c.want, c.ok)
		}
	}
}

func TestNameKey(t *testing.T) {
	if NameKey(" Äpfel  grün ") != NameKey("äpfel GRÜN") {
		t.Error("case or space not folded")
	}
	if NameKey("Straße") != NameKey("STRASSE") {
		t.Error("ß not folded")
	}
	if got := CleanName("  lemons "); got != "Lemons" {
		t.Errorf("CleanName = %q", got)
	}
}
