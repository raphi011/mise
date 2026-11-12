package ui

// NutritionView is the nutrition block of a recipe page: the estimate per
// serving, or the reasons there is none yet. Every string is translated.
type NutritionView struct {
	// Show is true when enough ingredients are counted to show numbers.
	Show bool
	// Kcal, Protein, Fat and Carbs are formatted with their units; a
	// macro is "" when some counted ingredient lacks it.
	Kcal, Protein, Fat, Carbs string
	// Split is the share of energy from protein, fat and carbs in percent,
	// or all zero when a macro is unknown.
	Split [3]int
	// More are the secondary nutrients that are known.
	More []NutrientRow
	// PerServing is "per serving · 2 servings".
	PerServing string
	// Subtitle is "Per serving · 2 servings", the popup's subtitle; unlike
	// PerServing it is set whether or not numbers are shown.
	Subtitle string
	// Coverage is "Based on 7 of 8 ingredients"; Counted and Of drive the
	// dots. It is empty when every ingredient is counted.
	Coverage    string
	Counted, Of int
	// NotEnough is the explanation shown instead of numbers.
	NotEnough string
	// LeftOut names the optional ingredients left out, or "".
	LeftOut string
	Missing []NutritionMissing
	// Public is the block as the public page shows it, without the list of
	// what is missing.
	Public bool
	// Credit is the footnote on what the estimate rests on.
	Credit string
}

// NutritionSheetView is the nutrition popup of a recipe.
type NutritionSheetView struct {
	// ID is the recipe's.
	ID, Name  string
	Nutrition NutritionView
}

// NutrientRow is one label and value.
type NutrientRow struct{ Label, Value string }

// NutritionMissing is an ingredient that is not in the estimate.
type NutritionMissing struct {
	Name, Amount, Why string
	// Href and Action lead to where it can be fixed.
	Href, Action string
}

// NutritionEditView is the page that edits an item's nutrition.
type NutritionEditView struct {
	ID, Name string
	// Next is where Save returns to, a local path.
	Next string
	// Has is true when values are stored, which Clear removes.
	Has bool
	// Used is "Used in 3 recipes".
	Used string
	// Note says where the shown values come from, or is empty.
	Note string
	// ClearLabel is the button that removes the household's values.
	ClearLabel string
	Form       Form
	Units      []WeightField
}

// WeightField is the input for the weight of one unit.
type WeightField struct {
	// Name is the form field, "weight_clove".
	Name  string
	Label string
}

// EnergySplit returns the shares of energy from protein, fat and carbs in
// percent, rounded so that they add up to 100.
func EnergySplit(protein, fat, carbs float64) [3]int {
	p, f, c := protein*4, fat*9, carbs*4
	total := p + f + c
	if total <= 0 {
		return [3]int{}
	}
	out := [3]int{int(p / total * 100), int(f / total * 100), int(c / total * 100)}
	out[0] += 100 - out[0] - out[1] - out[2]
	return out
}
