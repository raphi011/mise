package catalog

// Nutrition is what the catalogue knows about the nutrition of an entry,
// per 100 g of the raw ingredient, with the density and weights that turn
// other amounts into grams. Values come from USDA FoodData Central and keep
// the ID of the food they were taken from (docs/credits.md).
type Nutrition struct {
	// FDCID is the FoodData Central ID of the source food.
	FDCID int     `json:"fdc_id"`
	Kcal  float64 `json:"kcal"`
	// The other values are nil when the source has none.
	Protein      *float64 `json:"protein_g"`
	Fat          *float64 `json:"fat_g"`
	SaturatedFat *float64 `json:"saturated_fat_g"`
	Carbs        *float64 `json:"carbs_g"`
	Sugars       *float64 `json:"sugars_g"`
	Fibre        *float64 `json:"fibre_g"`
	SodiumMg     *float64 `json:"sodium_mg"`
	// Density is grams per millilitre.
	Density *float64 `json:"density_g_ml"`
	// Weights are grams per piece, clove, slice, bunch, can or pack.
	Weights map[string]float64 `json:"weights"`
}

// NutritionFor returns the nutrition of the entry with key, following
// retired keys to their replacement.
func NutritionFor(key string) (Nutrition, bool) {
	ensure()
	e, ok := For(Languages[0]).Entry(key)
	if !ok {
		return Nutrition{}, false
	}
	n, ok := nutrition[e.Key]
	return n, ok
}

// NutritionCount is the number of entries that have nutrition data.
func NutritionCount() int {
	ensure()
	return len(nutrition)
}
