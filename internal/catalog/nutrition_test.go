package catalog

import (
	"slices"
	"testing"
)

// TestNutritionData checks every row of data/nutrition.json: its key is an
// entry of the catalogue, it names its FoodData Central food, and the values
// are possible per 100 g. It passes while the file is empty.
func TestNutritionData(t *testing.T) {
	ensure()
	units := []string{"piece", "clove", "slice", "bunch", "can", "pack"}
	for key, n := range nutrition {
		if _, ok := For("en").Entry(key); !ok {
			t.Errorf("%s: not a catalogue entry", key)
		}
		if n.FDCID <= 0 {
			t.Errorf("%s: no FoodData Central ID", key)
		}
		if n.Kcal < 0 || n.Kcal > 900 {
			t.Errorf("%s: %v kcal per 100 g", key, n.Kcal)
		}
		var macros float64
		for name, v := range map[string]*float64{"protein": n.Protein, "fat": n.Fat, "saturated fat": n.SaturatedFat,
			"carbs": n.Carbs, "sugars": n.Sugars, "fibre": n.Fibre} {
			if v != nil && (*v < 0 || *v > 100) {
				t.Errorf("%s: %v g %s per 100 g", key, *v, name)
			}
		}
		for _, v := range []*float64{n.Protein, n.Fat, n.Carbs, n.Fibre} {
			if v != nil {
				macros += *v
			}
		}
		if macros > 100.5 {
			t.Errorf("%s: protein, fat, carbs and fibre add up to %v g", key, macros)
		}
		if n.Density != nil && (*n.Density < 0.05 || *n.Density > 25) {
			t.Errorf("%s: density %v", key, *n.Density)
		}
		for unit, g := range n.Weights {
			if !slices.Contains(units, unit) || g <= 0 {
				t.Errorf("%s: weight %v g for %q", key, g, unit)
			}
		}
	}
}

func TestNutritionFor(t *testing.T) {
	ensure()
	if _, ok := NutritionFor("no_such_ingredient"); ok {
		t.Error("nutrition for an unknown key")
	}
	if n := NutritionCount(); n < 0 {
		t.Errorf("count %d", n)
	}
}
