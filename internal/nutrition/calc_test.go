package nutrition

import (
	"math"
	"testing"

	"github.com/google/uuid"
	"golang.org/x/text/language"

	"github.com/raphi011/mise/internal/groceries"
)

func ptr[T any](v T) *T { return &v }

func amount(s string) groceries.Amount { return groceries.ParseAmount(s, language.English) }

func near(a, b float64) bool { return math.Abs(a-b) < 0.01 }

func TestGrams(t *testing.T) {
	oil := Facts{Kcal: 884, Density: ptr(0.91)}
	garlic := Facts{Kcal: 149, Weights: map[string]float64{"clove": 4, "piece": 40}}
	onion := Facts{Kcal: 40, Weights: map[string]float64{"piece": 110}}
	bunch := Facts{Kcal: 32, Weights: map[string]float64{"bunch": 100}}
	cases := []struct {
		name   string
		amount string
		facts  Facts
		grams  float64
		reason Reason
	}{
		{"grams", "200 g", Facts{}, 200, ""},
		{"kilograms", "1,5 kg", Facts{}, 1500, ""},
		{"ounces", "2 oz", Facts{}, 56.7, ""},
		{"tablespoons", "2 tbsp", oil, 26.91, ""},
		{"cups", "1 cup", oil, 215.3, ""},
		{"volume without density", "2 tbsp", garlic, 0, ReasonNoDensity},
		{"cloves", "3 cloves", garlic, 12, ""},
		{"bare number is a piece", "2", onion, 220, ""},
		{"pieces", "2 pcs", onion, 220, ""},
		{"bare number without piece weight", "3", bunch, 0, ReasonNoWeight},
		{"bunch", "½ bunch", bunch, 50, ""},
		{"unit without weight", "1 can", onion, 0, ReasonNoWeight},
		{"pinch", "1 pinch", Facts{}, 0.4, ""},
		{"free text", "1 thumb", onion, 0, ReasonFreeText},
		{"approximate", "ca. 200 g", Facts{}, 0, ReasonFreeText},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g, reason := Grams(amount(c.amount), c.facts)
			if reason != c.reason || !near(g, c.grams) {
				t.Errorf("Grams(%q) = %.2f, %q; want %.2f, %q", c.amount, g, reason, c.grams, c.reason)
			}
		})
	}
}

func TestCalculate(t *testing.T) {
	tofu, soy, ginger, garlic, salt := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	facts := map[uuid.UUID]Facts{
		tofu:   {Kcal: 144, Protein: ptr(17.3), Fat: ptr(8.7), Carbs: ptr(2.8), SodiumMg: ptr(14.0)},
		soy:    {Kcal: 53, Protein: ptr(8.1), Fat: ptr(0.6), Carbs: ptr(4.9), SodiumMg: ptr(5493.0), Density: ptr(1.15)},
		garlic: {Kcal: 149, Protein: ptr(6.4), Fat: ptr(0.5), Carbs: ptr(33.0), Weights: map[string]float64{"clove": 4}},
		salt:   {Kcal: 0, SodiumMg: ptr(38758.0)},
	}
	lines := []Line{
		{ItemID: tofu, Name: "Tofu", Amount: amount("400 g")},
		{ItemID: soy, Name: "Soy sauce", Amount: amount("3 tbsp")},
		{ItemID: ginger, Name: "Ginger", Amount: amount("20 g")}, // no data
		{ItemID: garlic, Name: "Garlic", Amount: amount("2 cloves")},
		{ItemID: salt, Name: "Salt"}, // no amount: in neither count
		{ItemID: tofu, Name: "Sesame", Amount: amount("1 tbsp"), Optional: true},
		{ItemID: ginger, Name: "Ginger", Amount: amount("1 thumb")},
		{ItemID: salt, Name: "Pepper", Amount: amount("to taste")}, // no number: in neither count
	}
	r := Calculate(lines, facts, 2)
	if r.OpenAmounts != 1 {
		t.Errorf("open amounts %d, want 1", r.OpenAmounts)
	}
	if r.Of != 5 || r.Counted != 3 || r.OptionalLeftOut != 1 {
		t.Fatalf("counted %d of %d, optional %d; want 3 of 5, 1", r.Counted, r.Of, r.OptionalLeftOut)
	}
	if len(r.Missing) != 2 || r.Missing[0].Reason != ReasonNoData || r.Missing[1].Reason != ReasonFreeText {
		t.Errorf("missing = %+v", r.Missing)
	}
	wantStatus := []LineStatus{{Counted: true}, {Counted: true}, {Uncounted: true}, {Counted: true}, {}, {}, {Uncounted: true}}
	for i, w := range wantStatus {
		if r.Lines[i] != w {
			t.Errorf("line %d = %+v, want %+v", i, r.Lines[i], w)
		}
	}
	// 400 g tofu + 3 tbsp (44.36 ml * 1.15 = 51.0 g) soy + 8 g garlic, for 2.
	soyGrams := 3 * 14.78676478125 * 1.15
	wantKcal := (144.0*4 + 53*soyGrams/100 + 149*0.08) / 2
	if math.Abs(r.PerServing.Kcal-wantKcal) > 0.01 {
		t.Errorf("kcal = %.2f, want %.2f", r.PerServing.Kcal, wantKcal)
	}
	// Garlic has no sodium value, so a sodium sum would have a hole.
	if !r.Known.Protein || !r.Known.Fat || !r.Known.Carbs || r.Known.SodiumMg || r.Known.SatFat {
		t.Errorf("known = %+v", r.Known)
	}
	if !r.Show() || !r.Partial() {
		t.Errorf("Show = %v, Partial = %v; want true, true", r.Show(), r.Partial())
	}
}

func TestCalculateThreshold(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	facts := map[uuid.UUID]Facts{a: {Kcal: 100}}
	one := func(n int) []Line {
		lines := []Line{{ItemID: a, Name: "A", Amount: amount("100 g")}}
		for range n {
			lines = append(lines, Line{ItemID: b, Name: "B", Amount: amount("100 g")})
		}
		return lines
	}
	if r := Calculate(one(1), facts, 1); !r.Show() {
		t.Error("1 of 2 counted should show")
	}
	if r := Calculate(one(2), facts, 1); r.Show() {
		t.Error("1 of 3 counted should not show")
	}
	if r := Calculate(one(0), facts, 0); r.Show() {
		t.Error("no servings should not show")
	}
	if r := Calculate(nil, facts, 2); r.Show() || r.Of != 0 {
		t.Error("no ingredients should not show")
	}
}

func TestRounding(t *testing.T) {
	for _, c := range []struct{ got, want float64 }{
		{RoundKcal(397), 395}, {RoundKcal(398), 400}, {RoundGrams(2.64), 2.6}, {RoundGrams(37.4), 37},
		{RoundGrams(9.96), 10}, {RoundSodium(1435), 1440},
	} {
		if !near(c.got, c.want) {
			t.Errorf("got %v, want %v", c.got, c.want)
		}
	}
	if s := (Nutrients{SodiumMg: 1000}).Salt(); !near(s, 2.5) {
		t.Errorf("salt of 1000 mg sodium = %v, want 2.5", s)
	}
}
