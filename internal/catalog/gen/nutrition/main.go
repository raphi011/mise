// Command nutrition builds internal/catalog/data/nutrition.json from USDA
// FoodData Central CSV downloads and the hand-kept nutrition_map.csv.
//
//	go run ./internal/catalog/gen/nutrition <fdc-csv-dir>
//
// The directory holds the unzipped Foundation Foods and SR Legacy downloads
// (any depth; every folder with a food_nutrient.csv is read). They are never
// committed. Columns of nutrition_map.csv:
//
//	key            catalogue key (internal/catalog/data/entries.json)
//	fdc_id         FoodData Central ID of the raw food; empty = unmapped
//	portion_fdc_id food whose portion rows give density and weights; default fdc_id
//	density        portion label such as "cup" (grams per ml from that row), or
//	               "=0.91" for a hand-set g/ml
//	weights        unit=label pairs separated by ";" (piece, clove, slice,
//	               bunch, can, pack); a label is the portion row's unit,
//	               description and modifier joined by spaces ("medium (3" dia)"),
//	               "label#2" takes the second row with that label; a number
//	               is a hand-set gram weight
//	hand_edited    "yes" exactly when density or weights hold a hand-set value
//	note           why, for the reviewer
//
// Carbohydrate is the FDC value minus fibre (EU label convention).
package main

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/raphi011/mise/internal/catalog"
)

func main() {
	mapPath := flag.String("map", "internal/catalog/gen/nutrition/nutrition_map.csv", "hand-kept map")
	entries := flag.String("entries", "internal/catalog/data/entries.json", "catalogue entries")
	out := flag.String("out", "internal/catalog/data/nutrition.json", "file to write")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: go run ./internal/catalog/gen/nutrition <fdc-csv-dir>")
		os.Exit(2)
	}
	unmapped, err := run(flag.Arg(0), *mapPath, *entries, *out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nutrition:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "unmapped (%d): %s\n", len(unmapped), strings.Join(unmapped, " "))
}

// Nutrient IDs in food_nutrient.csv, first one present wins. Foundation
// Foods carry Atwater energy (2047, 2048) where SR Legacy has 1008.
var (
	idKcal    = []string{"1008", "2047", "2048"}
	idProtein = []string{"1003"}
	idFat     = []string{"1004"}
	idSat     = []string{"1258"}
	idCarbs   = []string{"1005", "1050"}
	idSugars  = []string{"2000", "1063"}
	idFibre   = []string{"1079", "2033"}
	idSodium  = []string{"1093"}
)

var weightUnits = []string{"piece", "clove", "slice", "bunch", "can", "pack"}

// notRaw marks descriptions the build refuses: recipes list raw amounts.
var notRaw = []string{"cooked", "boiled", "fried", "roasted", "baked", "braised", "broiled", "grilled", "toasted", "stewed", "steamed", "canned, drained"}

type row struct {
	key, portionFDC string
	fdcID           int
	density         string
	weights         []weightSpec
	hand            bool
	note            string
}

type weightSpec struct{ unit, label string }

type portion struct {
	label string
	grams float64 // per one unit of the portion
}

func run(dir, mapPath, entriesPath, outPath string) (unmapped []string, err error) {
	keys, err := entryKeys(entriesPath)
	if err != nil {
		return nil, err
	}
	rows, err := readMap(mapPath, keys)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, r := range rows {
		if r.fdcID == 0 {
			unmapped = append(unmapped, r.key)
			continue
		}
		want[strconv.Itoa(r.fdcID)] = true
		want[r.portionFDC] = true
	}
	src, err := load(dir, want)
	if err != nil {
		return nil, err
	}
	res := map[string]catalog.Nutrition{}
	var errs []error
	for _, r := range rows {
		if r.fdcID == 0 {
			continue
		}
		n, err := build(r, src)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", r.key, err))
			continue
		}
		res[r.key] = n
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(res, "", " ")
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	return unmapped, os.WriteFile(outPath, data, 0o644)
}

func entryKeys(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var es []struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(b, &es); err != nil {
		return nil, err
	}
	var keys []string
	for _, e := range es {
		keys = append(keys, e.Key)
	}
	return keys, nil
}

func readMap(path string, keys []string) ([]row, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	recs, err := csv.NewReader(f).ReadAll()
	if err != nil {
		return nil, err
	}
	want := []string{"key", "fdc_id", "portion_fdc_id", "density", "weights", "hand_edited", "note"}
	if len(recs) == 0 || !slices.Equal(recs[0], want) {
		return nil, fmt.Errorf("%s: header must be %s", path, strings.Join(want, ","))
	}
	seen := map[string]bool{}
	var rows []row
	for i, rec := range recs[1:] {
		line := i + 2
		r := row{key: rec[0], density: strings.TrimSpace(rec[3]), note: rec[6]}
		if !slices.Contains(keys, r.key) {
			return nil, fmt.Errorf("%s:%d: %q is not a catalogue key", path, line, r.key)
		}
		if seen[r.key] {
			return nil, fmt.Errorf("%s:%d: duplicate key %q", path, line, r.key)
		}
		seen[r.key] = true
		if rec[1] == "" {
			if r.note == "" {
				return nil, fmt.Errorf("%s:%d: unmapped %q needs a note", path, line, r.key)
			}
			rows = append(rows, r)
			continue
		}
		if r.fdcID, err = strconv.Atoi(rec[1]); err != nil {
			return nil, fmt.Errorf("%s:%d: fdc_id: %w", path, line, err)
		}
		r.portionFDC = rec[2]
		if r.portionFDC == "" {
			r.portionFDC = rec[1]
		}
		manual := strings.HasPrefix(r.density, "=")
		for _, w := range strings.Split(rec[4], ";") {
			if w = strings.TrimSpace(w); w == "" {
				continue
			}
			unit, label, ok := strings.Cut(w, "=")
			if !ok || !slices.Contains(weightUnits, unit) {
				return nil, fmt.Errorf("%s:%d: weight %q is not unit=label", path, line, w)
			}
			if _, err := strconv.ParseFloat(label, 64); err == nil {
				manual = true
			}
			r.weights = append(r.weights, weightSpec{unit, label})
		}
		switch rec[5] {
		case "yes":
			r.hand = true
		case "":
		default:
			return nil, fmt.Errorf("%s:%d: hand_edited must be yes or empty", path, line)
		}
		if r.hand != manual {
			return nil, fmt.Errorf("%s:%d: %s: hand_edited must be \"yes\" exactly when density or weights hold a hand-set value", path, line, r.key)
		}
		if r.hand && r.note == "" {
			return nil, fmt.Errorf("%s:%d: %s: a hand-edited row needs a note", path, line, r.key)
		}
		rows = append(rows, r)
	}
	for _, k := range keys {
		if !seen[k] {
			return nil, fmt.Errorf("%s: catalogue key %q has no row (leave fdc_id empty to leave it unmapped)", path, k)
		}
	}
	return rows, nil
}

type source struct {
	desc     map[string]string
	dataType map[string]string
	nutrient map[string]map[string]float64 // fdc_id → nutrient_id → amount per 100 g
	portions map[string][]portion
}

var wantedNutrients = func() map[string]bool {
	m := map[string]bool{}
	for _, ids := range [][]string{idKcal, idProtein, idFat, idSat, idCarbs, idSugars, idFibre, idSodium} {
		for _, id := range ids {
			m[id] = true
		}
	}
	return m
}()

func load(dir string, want map[string]bool) (*source, error) {
	s := &source{desc: map[string]string{}, dataType: map[string]string{},
		nutrient: map[string]map[string]float64{}, portions: map[string][]portion{}}
	var dirs []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == "food_nutrient.csv" {
			dirs = append(dirs, filepath.Dir(p))
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	if len(dirs) == 0 {
		return nil, fmt.Errorf("%s: no food_nutrient.csv found", dir)
	}
	sort.Strings(dirs)
	for _, d := range dirs {
		units, err := readUnits(filepath.Join(d, "measure_unit.csv"))
		if err != nil {
			return nil, err
		}
		if err := each(filepath.Join(d, "food.csv"), func(r map[string]string) {
			if want[r["fdc_id"]] {
				s.desc[r["fdc_id"]] = r["description"]
				s.dataType[r["fdc_id"]] = r["data_type"]
			}
		}); err != nil {
			return nil, err
		}
		if err := each(filepath.Join(d, "food_nutrient.csv"), func(r map[string]string) {
			if !want[r["fdc_id"]] || !wantedNutrients[r["nutrient_id"]] {
				return
			}
			v, err := strconv.ParseFloat(r["amount"], 64)
			if err != nil {
				return
			}
			if s.nutrient[r["fdc_id"]] == nil {
				s.nutrient[r["fdc_id"]] = map[string]float64{}
			}
			s.nutrient[r["fdc_id"]][r["nutrient_id"]] = v
		}); err != nil {
			return nil, err
		}
		if err := each(filepath.Join(d, "food_portion.csv"), func(r map[string]string) {
			if !want[r["fdc_id"]] {
				return
			}
			g, err1 := strconv.ParseFloat(r["gram_weight"], 64)
			a, err2 := strconv.ParseFloat(r["amount"], 64)
			if err1 != nil || err2 != nil || a <= 0 {
				return
			}
			var parts []string
			if u := units[r["measure_unit_id"]]; u != "" && u != "undetermined" {
				parts = append(parts, u)
			}
			parts = append(parts, r["portion_description"], r["modifier"])
			s.portions[r["fdc_id"]] = append(s.portions[r["fdc_id"]], portion{label: normalise(parts...), grams: g / a})
		}); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func normalise(parts ...string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.Join(parts, " ")), " "))
}

func readUnits(path string) (map[string]string, error) {
	m := map[string]string{}
	err := each(path, func(r map[string]string) { m[r["id"]] = r["name"] })
	return m, err
}

// each streams a CSV file with a header row.
func each(path string, fn func(map[string]string)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	cr := csv.NewReader(bufio.NewReader(f))
	cr.FieldsPerRecord = -1
	head, err := cr.Read()
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	rec := make(map[string]string, len(head))
	for {
		fields, err := cr.Read()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		clear(rec)
		for i, h := range head {
			if i < len(fields) {
				rec[h] = fields[i]
			}
		}
		fn(rec)
	}
}

func round(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}

func pick(m map[string]float64, ids []string) *float64 {
	for _, id := range ids {
		if v, ok := m[id]; ok {
			v = round(v, 2)
			return &v
		}
	}
	return nil
}

func build(r row, s *source) (catalog.Nutrition, error) {
	id := strconv.Itoa(r.fdcID)
	switch s.dataType[id] {
	case "foundation_food", "sr_legacy_food":
	case "":
		return catalog.Nutrition{}, fmt.Errorf("FDC %d is not in the downloads", r.fdcID)
	default:
		return catalog.Nutrition{}, fmt.Errorf("FDC %d is %s, not Foundation or SR Legacy", r.fdcID, s.dataType[id])
	}
	desc := strings.ToLower(s.desc[id])
	for _, w := range notRaw {
		if strings.Contains(desc, w) {
			return catalog.Nutrition{}, fmt.Errorf("FDC %d %q is not a raw food", r.fdcID, s.desc[id])
		}
	}
	nu := s.nutrient[id]
	kcal := pick(nu, idKcal)
	if kcal == nil {
		return catalog.Nutrition{}, fmt.Errorf("FDC %d has no energy value", r.fdcID)
	}
	n := catalog.Nutrition{
		FDCID: r.fdcID, Kcal: *kcal,
		Protein: pick(nu, idProtein), Fat: pick(nu, idFat), SaturatedFat: pick(nu, idSat),
		Carbs: pick(nu, idCarbs), Sugars: pick(nu, idSugars), Fibre: pick(nu, idFibre),
		SodiumMg: pick(nu, idSodium),
	}
	// FDC carbohydrate (by difference) includes fibre; the app follows the EU
	// label, where carbohydrate excludes it and the macros add up to 100 g.
	if n.Carbs != nil && n.Fibre != nil {
		c := round(math.Max(0, *n.Carbs-*n.Fibre), 2)
		n.Carbs = &c
	}
	ports := s.portions[r.portionFDC]
	if r.density != "" {
		d, err := density(r.density, ports, r.portionFDC)
		if err != nil {
			return n, err
		}
		n.Density = &d
	}
	for _, w := range r.weights {
		g, err := weight(w.label, ports, r.portionFDC)
		if err != nil {
			return n, fmt.Errorf("%s: %w", w.unit, err)
		}
		if n.Weights == nil {
			n.Weights = map[string]float64{}
		}
		n.Weights[w.unit] = g
	}
	return n, nil
}

var mlPer = []struct {
	name string
	ml   float64
}{
	{"fl oz", 29.5735}, {"cup", 236.588}, {"tablespoon", 14.7868}, {"tbsp", 14.7868},
	{"teaspoon", 4.92892}, {"tsp", 4.92892}, {"milliliter", 1}, {"liter", 1000},
}

func density(spec string, ports []portion, id string) (float64, error) {
	if v, ok := strings.CutPrefix(spec, "="); ok {
		d, err := strconv.ParseFloat(v, 64)
		if err != nil || d <= 0 {
			return 0, fmt.Errorf("density %q", spec)
		}
		return d, nil
	}
	g, err := find(spec, ports, id)
	if err != nil {
		return 0, fmt.Errorf("density: %w", err)
	}
	label := normalise(spec)
	for _, u := range mlPer {
		rest, ok := strings.CutPrefix(label, u.name)
		if ok && (rest == "" || !isLetter(rest[0])) {
			return round(g/u.ml, 3), nil
		}
	}
	return 0, fmt.Errorf("density: %q does not start with a volume unit", spec)
}

func isLetter(b byte) bool { return b >= 'a' && b <= 'z' }

func weight(spec string, ports []portion, id string) (float64, error) {
	if g, err := strconv.ParseFloat(spec, 64); err == nil {
		if g <= 0 {
			return 0, fmt.Errorf("weight %q", spec)
		}
		return g, nil
	}
	g, err := find(spec, ports, id)
	if err != nil {
		return 0, err
	}
	return round(g, 1), nil
}

// find returns the grams of one unit of the portion row labelled spec.
func find(spec string, ports []portion, id string) (float64, error) {
	label, nth := normalise(spec), 0
	if i := strings.LastIndex(label, "#"); i >= 0 {
		n, err := strconv.Atoi(label[i+1:])
		if err == nil {
			label, nth = label[:i], n
		}
	}
	var hits []portion
	for _, p := range ports {
		if p.label == label {
			hits = append(hits, p)
		}
	}
	switch {
	case len(hits) == 0:
		var all []string
		for _, p := range ports {
			all = append(all, p.label)
		}
		return 0, fmt.Errorf("FDC %s has no portion %q (has: %s)", id, label, strings.Join(all, " | "))
	case nth > 0 && nth <= len(hits):
		return hits[nth-1].grams, nil
	case nth > 0 || len(hits) > 1:
		return 0, fmt.Errorf("FDC %s has %d portions %q: pick one with %q#N", id, len(hits), label, label)
	}
	return hits[0].grams, nil
}
