package groceries

import (
	"math"

	"golang.org/x/text/language"
)

// System is a measurement system a user reads amounts in. Amounts are
// stored as written and converted when shown (docs/plan.md, Units).
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
