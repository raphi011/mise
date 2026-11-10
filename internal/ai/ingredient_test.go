package ai_test

import (
	"slices"
	"testing"

	"github.com/raphi011/mise/internal/ai"
)

func TestParseIngredientName(t *testing.T) {
	for _, c := range []struct {
		in, phrase, new string
		options         []string
	}{
		{"onion, finely chopped (optional)", "onion", "onion", []string{"onion"}},
		{"sunflower or vegetable oil plus a little extra for frying", "sunflower or vegetable oil", "vegetable oil",
			[]string{"sunflower oil", "sunflower", "vegetable oil"}},
		{"red or white wine vinegar", "red or white wine vinegar", "white wine vinegar",
			[]string{"red wine vinegar", "red", "white wine vinegar"}},
		{"butter or margarine", "butter or margarine", "butter", []string{"butter", "margarine"}},
		{"extra virgin olive oil, to drizzle", "extra virgin olive oil", "extra virgin olive oil", []string{"extra virgin olive oil"}},
		{"sea salt to taste", "sea salt", "sea salt", []string{"sea salt"}},
		{"a firm white fish such as cod", "a firm white fish", "a firm white fish", []string{"a firm white fish"}},
		{"Öl oder Butter zum Braten", "Öl oder Butter", "Öl", []string{"Öl", "Butter"}},
		{"Sonnenblumen- oder Rapsöl", "Sonnenblumen- oder Rapsöl", "Rapsöl", []string{"Rapsöl"}},
		{"Petersilie für die Deko", "Petersilie", "Petersilie", []string{"Petersilie"}},
		{"* chillies – optional", "chillies", "chillies", []string{"chillies"}},
		{"plus", "plus", "plus", []string{"plus"}},
		{"or", "", "", nil},
		{"(to serve)", "", "", nil},
	} {
		n := ai.ParseIngredientName(c.in)
		if n.Phrase != c.phrase || n.New != c.new || !slices.Equal(n.Options, c.options) {
			t.Errorf("%q: %+v, want phrase %q, new %q, options %q", c.in, n, c.phrase, c.new, c.options)
		}
	}
}
