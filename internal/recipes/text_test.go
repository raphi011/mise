package recipes

import (
	"strings"
	"testing"

	"golang.org/x/text/language"

	"github.com/raphi011/mise/internal/groceries"
)

func TestScale(t *testing.T) {
	en, de := language.English, language.German
	for _, tc := range []struct {
		amount         string
		lang           language.Tag
		base, servings int
		want           string
	}{
		{"400 g", en, 2, 4, "800 g"},
		{"3 tbsp", en, 2, 3, "4.5 tbsp"},
		{"1 thumb", en, 2, 6, "1 thumb"},
		{"a pinch or two", en, 2, 6, "a pinch or two"},
		{"1,5 EL", de, 2, 3, "2,5 EL"}, // 2.25 rounds to a half
		{"250 g", en, 4, 3, "190 g"},   // 187.5 rounds to tens
		{"12", en, 4, 3, "9"},
		{"½ tsp", en, 4, 1, "0.13 tsp"},
		{"2", en, 2, 2, "2"},
	} {
		a := groceries.ParseAmount(tc.amount, tc.lang)
		got := Scale(a, tc.base, tc.servings, tc.lang)
		if txt := DisplayAmount(got, tc.lang); txt != tc.want {
			t.Errorf("Scale(%q, %d→%d) = %q, want %q", tc.amount, tc.base, tc.servings, txt, tc.want)
		}
	}
}

func render(parts []Part, ings []Ingredient) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Ingredient < 0 {
			b.WriteString(p.Text)
			continue
		}
		b.WriteString("[" + p.Text + "=" + ings[p.Ingredient].ItemName + "]")
	}
	return b.String()
}

func TestHighlight(t *testing.T) {
	ings := []Ingredient{
		{ItemName: "Firm tofu"}, {ItemName: "Soy sauce"}, {ItemName: "Ginger"}, {ItemName: "Spring onions"},
		{ItemName: "Garlic"}, {ItemName: "Bratensoße"}, {ItemName: "Tomate"}, {ItemName: "Chili sauce"},
	}
	seen := map[int]bool{}
	steps := []struct{ in, want string }{
		{"Press the tofu for 10 minutes.", "Press the [tofu=Firm tofu] for 10 minutes."},
		// First mention only; "sauce" alone is ambiguous between two items.
		{"Toss tofu in soy sauce and grated Ginger, then the sauce.", "Toss tofu in [soy sauce=Soy sauce] and grated [Ginger=Ginger], then the sauce."},
		{"Scatter with sliced spring onion.", "Scatter with sliced [spring onion=Spring onions]."},
		{"Garlicky oil, then the garlic.", "Garlicky oil, then the [garlic=Garlic]."},
		{"Die BRATENSOSSE und die Tomaten erhitzen.", "Die [BRATENSOSSE=Bratensoße] und die [Tomaten=Tomate] erhitzen."},
		{"More ginger.", "More ginger."},
	}
	for _, st := range steps {
		if got := render(Highlight(st.in, ings, seen), ings); got != st.want {
			t.Errorf("Highlight(%q)\n got  %s\n want %s", st.in, got, st.want)
		}
	}
}
