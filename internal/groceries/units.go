package groceries

import (
	"math"

	"golang.org/x/text/language"
)

// System is a measurement system a user reads amounts in. Amounts are
// stored as written and converted when shown.
type System string

const (
	Metric System = "metric"
	US     System = "us"
)

// Conversion factors to grams and millilitres.
const (
	gramsPerOz = 28.349523125
	gramsPerLb = 453.59237
	mlPerFlOz  = 29.5735295625
	mlPerCup   = 236.5882365
	mlPerTbsp  = 14.78676478125
	mlPerTsp   = 4.92892159375
)

// toMetric maps the units a US user writes to grams or millilitres.
var toMetric = map[string]struct {
	factor float64
	volume bool
}{
	"oz": {gramsPerOz, false}, "lb": {gramsPerLb, false},
	"floz": {mlPerFlOz, true}, "cup": {mlPerCup, true},
}

// toUS maps metric units to grams or millilitres. mg stays as it is: the
// amounts are too small to weigh in ounces.
var toUS = map[string]struct {
	factor float64
	volume bool
}{
	"g": {1, false}, "kg": {1000, false},
	"ml": {1, true}, "cl": {10, true}, "dl": {100, true}, "l": {1000, true},
}

// Convert returns a as shown in sys. Only structured amounts in a unit of
// the other system change; teaspoons, pinches, pieces and free text stay as
// they are. A converted amount is rounded to what a cook would measure in
// the new unit and its Text is formatted in lang.
func Convert(a Amount, sys System, lang language.Tag) Amount {
	if a.Quantity == nil {
		return a
	}
	var q float64
	var unit string
	switch sys {
	case Metric:
		c, ok := toMetric[a.Unit]
		if !ok {
			return a
		}
		q, unit = metricAmount(*a.Quantity*c.factor, c.volume)
	case US:
		c, ok := toUS[a.Unit]
		if !ok {
			return a
		}
		if c.volume {
			q, unit = usVolume(*a.Quantity * c.factor)
		} else {
			q, unit = usWeight(*a.Quantity * c.factor)
		}
	default:
		return a
	}
	return Amount{Quantity: &q, Unit: unit, Text: FormatAmount(q, unit, lang)}
}

// metricAmount expresses grams or millilitres in g/kg or ml/l.
func metricAmount(base float64, volume bool) (float64, string) {
	small, large := "g", "kg"
	if volume {
		small, large = "ml", "l"
	}
	switch {
	case base >= 1000:
		return roundTo(base/1000, 0.05), large
	case base >= 100:
		return roundTo(base, 5), small
	}
	return roundTo(base, 1), small
}

// usWeight expresses grams in oz, or lb from a pound.
func usWeight(g float64) (float64, string) {
	oz := g / gramsPerOz
	if oz >= 16 {
		return roundTo(oz/16, 0.25), "lb"
	}
	return roundTo(oz, usStep(oz)), "oz"
}

// usVolume expresses millilitres in tsp or tbsp for small amounts, fl oz
// below a cup and cups from there.
func usVolume(ml float64) (float64, string) {
	switch {
	case ml < mlPerTbsp:
		return roundTo(ml/mlPerTsp, 0.25), "tsp"
	case ml < mlPerFlOz*2:
		return roundTo(ml/mlPerTbsp, 0.5), "tbsp"
	case ml < mlPerCup:
		fl := ml / mlPerFlOz
		return roundTo(fl, usStep(fl)), "floz"
	}
	return roundTo(ml/mlPerCup, 0.25), "cup"
}

// usStep is the rounding step for ounces: quarters below one, halves below
// ten, whole ounces from there.
func usStep(q float64) float64 {
	switch {
	case q < 1:
		return 0.25
	case q < 10:
		return 0.5
	}
	return 1
}

// roundTo rounds q to a multiple of step, but never to zero.
func roundTo(q, step float64) float64 {
	return math.Max(math.Round(q/step)*step, step)
}

// Kind says what a structured amount measures.
type Kind int

const (
	// KindMass is an amount of weight, in grams.
	KindMass Kind = iota + 1
	// KindVolume is an amount of volume, in millilitres.
	KindVolume
	// KindCount is a number of things: pieces, cloves, cans, pinches or a
	// bare number.
	KindCount
)

// toBase maps every measuring unit to grams or millilitres. Unlike toMetric
// and toUS it includes the units that never convert for display.
var toBase = map[string]struct {
	factor float64
	kind   Kind
}{
	"g": {1, KindMass}, "kg": {1000, KindMass}, "mg": {0.001, KindMass},
	"oz": {gramsPerOz, KindMass}, "lb": {gramsPerLb, KindMass},
	"ml": {1, KindVolume}, "cl": {10, KindVolume}, "dl": {100, KindVolume}, "l": {1000, KindVolume},
	"tsp": {mlPerTsp, KindVolume}, "tbsp": {mlPerTbsp, KindVolume},
	"cup": {mlPerCup, KindVolume}, "floz": {mlPerFlOz, KindVolume},
}

// BaseAmount returns a in grams (KindMass), millilitres (KindVolume) or as a
// plain count (KindCount, with the unit left in a.Unit for the caller). It
// reports false for free text, which has no quantity.
func BaseAmount(a Amount) (value float64, kind Kind, ok bool) {
	if a.Quantity == nil {
		return 0, 0, false
	}
	if b, found := toBase[a.Unit]; found {
		return *a.Quantity * b.factor, b.kind, true
	}
	return *a.Quantity, KindCount, true
}
