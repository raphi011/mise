package groceries

import (
	"math"
	"testing"

	"golang.org/x/text/language"
)

func TestBaseAmount(t *testing.T) {
	q := func(v float64) *float64 { return &v }
	for _, c := range []struct {
		a    Amount
		want float64
		kind Kind
		ok   bool
	}{
		{Amount{Quantity: q(2), Unit: "kg"}, 2000, KindMass, true},
		{Amount{Quantity: q(1), Unit: "lb"}, gramsPerLb, KindMass, true},
		{Amount{Quantity: q(2), Unit: "tbsp"}, 2 * mlPerTbsp, KindVolume, true},
		{Amount{Quantity: q(1.5), Unit: "dl"}, 150, KindVolume, true},
		{Amount{Quantity: q(3), Unit: "clove"}, 3, KindCount, true},
		{Amount{Quantity: q(4)}, 4, KindCount, true},
		{Amount{Text: "1 thumb"}, 0, 0, false},
		{Amount{}, 0, 0, false},
	} {
		got, kind, ok := BaseAmount(c.a)
		if ok != c.ok || kind != c.kind || math.Abs(got-c.want) > 1e-9 {
			t.Errorf("BaseAmount(%+v) = %v, %v, %v; want %v, %v, %v", c.a, got, kind, ok, c.want, c.kind, c.ok)
		}
	}
}

func TestConvert(t *testing.T) {
	en, de := language.English, language.German
	cases := []struct {
		amount string
		lang   language.Tag
		sys    System
		want   string
	}{
		// Metric to US: weights in oz, lb from a pound.
		{"400 g", en, US, "14 oz"},
		{"20 g", en, US, "0.75 oz"},
		{"125 g", en, US, "4.5 oz"},
		{"1 kg", en, US, "2.25 lb"},
		{"500 mg", en, US, "500 mg"},
		// Volumes: tsp, tbsp, fl oz below a cup, cups from there.
		{"5 ml", en, US, "1 tsp"},
		{"30 ml", en, US, "2 tbsp"},
		{"100 ml", en, US, "3.5 fl oz"},
		{"250 ml", en, US, "1 cup"},
		{"1,5 l", de, US, "6,25 Tassen"},
		{"2 dl", en, US, "7 fl oz"},
		// US to metric.
		{"4 oz", en, Metric, "115 g"},
		{"1 lb", en, Metric, "455 g"},
		{"2.5 lb", en, Metric, "1.15 kg"},
		{"1 cup", en, Metric, "235 ml"},
		{"5 cups", en, Metric, "1.2 l"},
		{"8 fl oz", en, Metric, "235 ml"},
		{"1 fl. oz", en, Metric, "30 ml"},
		// Already in the system, shared units and free text stay as typed.
		{"500 g", en, Metric, "500 g"},
		{"1,5 kg", de, Metric, "1,5 kg"},
		{"2 cups", en, US, "2 cups"},
		{"2 tbsp", en, Metric, "2 tbsp"},
		{"1 TL", de, US, "1 TL"},
		{"1 pinch", en, US, "1 pinch"},
		{"3", en, US, "3"},
		{"1 thumb", en, US, "1 thumb"},
	}
	for _, c := range cases {
		got := Convert(ParseAmount(c.amount, c.lang), c.sys, c.lang)
		if got.Text != c.want {
			t.Errorf("Convert(%q, %s) = %q, want %q", c.amount, c.sys, got.Text, c.want)
		}
	}
}

func TestParseFluidOunces(t *testing.T) {
	for _, in := range []string{"8 fl oz milk", "8 fl. oz milk", "8 floz milk", "8 fl.oz milk"} {
		a, name := ParseEntry(in, language.English)
		if a.Quantity == nil || *a.Quantity != 8 || a.Unit != "floz" || name != "milk" {
			t.Errorf("ParseEntry(%q) = %+v, %q", in, a, name)
		}
	}
	// "fl" alone is not a unit.
	if a, name := ParseEntry("2 fl owers", language.English); a.Unit != "" || name != "fl owers" {
		t.Errorf("2 fl owers = %+v, %q", a, name)
	}
}
