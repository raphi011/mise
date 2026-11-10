package recipes

import (
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/raphi011/mise/internal/groceries"
)

// temperature matches a temperature in step text: "180°C", "350 °F",
// "180-200 °C", "37,5º C". Group 1 is the number, group 2 the upper end of
// a range, group 3 the scale.
var temperature = regexp.MustCompile(`(\d+(?:[.,]\d+)?)(?:\s*[-–]\s*(\d+(?:[.,]\d+)?))?\s?[°º]\s?([CF])\b`)

// ConvertTemperatures rewrites the temperatures in text to the scale of
// sys: Celsius for metric, Fahrenheit for US. Temperatures already in that
// scale, and the rest of the text, stay as they are.
func ConvertTemperatures(text string, sys groceries.System) string {
	want := "C"
	if sys == groceries.US {
		want = "F"
	}
	return temperature.ReplaceAllStringFunc(text, func(m string) string {
		sub := temperature.FindStringSubmatchIndex(m)
		if m[sub[6]:sub[7]] == want {
			return m
		}
		var b strings.Builder
		last := 0
		for _, g := range []int{1, 2} {
			lo, hi := sub[2*g], sub[2*g+1]
			if lo < 0 {
				continue
			}
			v, err := strconv.ParseFloat(strings.Replace(m[lo:hi], ",", ".", 1), 64)
			if err != nil {
				return m
			}
			b.WriteString(m[last:lo])
			b.WriteString(strconv.Itoa(convertDegrees(v, want)))
			last = hi
		}
		b.WriteString(m[last:sub[6]])
		b.WriteString(want)
		b.WriteString(m[sub[7]:])
		return b.String()
	})
}

// convertDegrees converts v to the scale to ("C" or "F"), rounded to five
// degrees for oven temperatures and to whole degrees below 100.
func convertDegrees(v float64, to string) int {
	if to == "F" {
		v = v*9/5 + 32
	} else {
		v = (v - 32) * 5 / 9
	}
	if v >= 100 {
		return int(math.Round(v/5) * 5)
	}
	return int(math.Round(v))
}
