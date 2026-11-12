package nutrition

import (
	"math"
	"strings"
	"unicode"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/groceries"
)

// pinchGrams is what a pinch weighs.
const pinchGrams = 0.4

// MinCoverage is the share of ingredients that must be counted before the
// numbers are shown (docs/plan.md, D3).
const MinCoverage = 0.5

// Reason says why an ingredient line could not be turned into grams.
type Reason string

const (
	// ReasonNoData: the item has no nutrition values.
	ReasonNoData Reason = "no_data"
	// ReasonFreeText: the amount has no quantity ("1 thumb", "ca. 200 g").
	ReasonFreeText Reason = "free_text"
	// ReasonNoDensity: a volume, and the item has no density.
	ReasonNoDensity Reason = "no_density"
	// ReasonNoWeight: a count, and the item has no weight for that unit.
	ReasonNoWeight Reason = "no_weight"
)

// Grams converts an ingredient amount to grams with the item's weights. The
// reason is empty on success. Nothing is guessed: a volume without a density
// does not count as water.
func Grams(a groceries.Amount, f Facts) (float64, Reason) {
	v, kind, ok := groceries.BaseAmount(a)
	if !ok {
		return 0, ReasonFreeText
	}
	switch kind {
	case groceries.KindMass:
		return v, ""
	case groceries.KindVolume:
		if f.Density == nil {
			return 0, ReasonNoDensity
		}
		return v * *f.Density, ""
	}
	if a.Unit == "pinch" {
		return v * pinchGrams, ""
	}
	unit := a.Unit
	if unit == "" {
		unit = "piece"
	}
	if g, ok := f.Weights[unit]; ok {
		return v * g, ""
	}
	return 0, ReasonNoWeight
}

// Nutrients are amounts of the tracked nutrients.
type Nutrients struct {
	Kcal, Protein, Fat, SatFat, Carbs, Sugars, Fibre, SodiumMg float64
}

// Salt is the salt equivalent of the sodium, in grams.
func (n Nutrients) Salt() float64 { return n.SodiumMg * 2.5 / 1000 }

// Known says which nutrients every counted ingredient had a value for;
// the others would be sums with holes, and are not shown.
type Known struct {
	Protein, Fat, SatFat, Carbs, Sugars, Fibre, SodiumMg bool
}

// Line is one ingredient of a recipe.
type Line struct {
	ItemID   uuid.UUID
	Name     string
	Amount   groceries.Amount
	Optional bool
}

// Missing is a line that is not in the estimate.
type Missing struct {
	// Index is the position of the line in the input.
	Index  int
	ItemID uuid.UUID
	Name   string
	// Amount is the amount as written.
	Amount string
	Reason Reason
}

// LineStatus is what happened to one line, in the order of the input.
type LineStatus struct {
	// Counted lines are in the estimate.
	Counted bool
	// Uncounted lines have an amount and are not optional, but could not be
	// weighed.
	Uncounted bool
}

// Result is the estimate for a recipe.
type Result struct {
	// PerServing is the estimate for one serving.
	PerServing Nutrients
	Known      Known
	Servings   int
	// Counted of Of ingredients are in the estimate. Optional ingredients
	// and lines without an amount are in neither.
	Counted, Of int
	Missing     []Missing
	Lines       []LineStatus
	// OptionalLeftOut is the number of optional ingredients left out.
	OptionalLeftOut int
	// OpenAmounts is the number of ingredients whose amount names no
	// number ("to taste", "for frying"). They cannot be weighed, so they
	// are in neither count.
	OpenAmounts int
}

func hasDigit(s string) bool {
	return strings.ContainsFunc(s, unicode.IsDigit)
}

// Show reports whether enough is counted to show the numbers.
func (r Result) Show() bool {
	return r.Servings > 0 && r.Counted > 0 && float64(r.Counted) >= MinCoverage*float64(r.Of)
}

// Partial reports whether some ingredients are not counted.
func (r Result) Partial() bool { return r.Counted < r.Of }

// Calculate estimates the nutrition per serving of lines. facts holds what is
// known about each item; items missing from it have no data.
func Calculate(lines []Line, facts map[uuid.UUID]Facts, servings int) Result {
	r := Result{Servings: servings, Lines: make([]LineStatus, len(lines))}
	known := Known{true, true, true, true, true, true, true}
	var total Nutrients
	for i, l := range lines {
		switch {
		case l.Optional:
			r.OptionalLeftOut++
			continue
		case l.Amount.IsZero():
			continue
		case l.Amount.Quantity == nil && !hasDigit(l.Amount.Text):
			r.OpenAmounts++
			continue
		}
		r.Of++
		f, have := facts[l.ItemID]
		g, reason := Grams(l.Amount, f)
		if !have && reason != ReasonFreeText {
			reason = ReasonNoData
		}
		if reason != "" {
			r.Lines[i].Uncounted = true
			r.Missing = append(r.Missing, Missing{Index: i, ItemID: l.ItemID, Name: l.Name, Amount: l.Amount.Text, Reason: reason})
			continue
		}
		r.Counted++
		r.Lines[i].Counted = true
		k := g / 100
		total.Kcal += f.Kcal * k
		add := func(sum *float64, v *float64, ok *bool) {
			if v == nil {
				*ok = false
				return
			}
			*sum += *v * k
		}
		add(&total.Protein, f.Protein, &known.Protein)
		add(&total.Fat, f.Fat, &known.Fat)
		add(&total.SatFat, f.SatFat, &known.SatFat)
		add(&total.Carbs, f.Carbs, &known.Carbs)
		add(&total.Sugars, f.Sugars, &known.Sugars)
		add(&total.Fibre, f.Fibre, &known.Fibre)
		add(&total.SodiumMg, f.SodiumMg, &known.SodiumMg)
	}
	if r.Counted == 0 || servings < 1 {
		return r
	}
	n := float64(servings)
	r.PerServing = Nutrients{
		Kcal: total.Kcal / n, Protein: total.Protein / n, Fat: total.Fat / n, SatFat: total.SatFat / n,
		Carbs: total.Carbs / n, Sugars: total.Sugars / n, Fibre: total.Fibre / n, SodiumMg: total.SodiumMg / n,
	}
	r.Known = known
	return r
}

// RoundKcal rounds energy to five.
func RoundKcal(v float64) float64 { return math.Round(v/5) * 5 }

// RoundGrams rounds to a tenth of a gram below ten grams, else to a gram.
func RoundGrams(v float64) float64 {
	if v < 10 {
		return math.Round(v*10) / 10
	}
	return math.Round(v)
}

// RoundSodium rounds sodium to ten milligrams.
func RoundSodium(v float64) float64 { return math.Round(v/10) * 10 }
