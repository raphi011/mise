package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/raphi011/mise/internal/catalog"
)

const fixture = "testdata"

func generate(t *testing.T, mapPath string) ([]byte, []string, error) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "nutrition.json")
	unmapped, err := run(filepath.Join(fixture, "fdc"), mapPath, filepath.Join(fixture, "entries.json"), out)
	if err != nil {
		return nil, nil, err
	}
	b, err := os.ReadFile(out)
	return b, unmapped, err
}

func TestRunFixture(t *testing.T) {
	b, unmapped, err := generate(t, filepath.Join(fixture, "map.csv"))
	if err != nil {
		t.Fatal(err)
	}
	if len(unmapped) != 1 || unmapped[0] != "soap" {
		t.Errorf("unmapped = %v", unmapped)
	}
	var got map[string]catalog.Nutrition
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	f := got["flour"]
	if f.FDCID != 1 || f.Kcal != 366 || *f.Protein != 10.9 || *f.Fibre != 2.7 || *f.Carbs != 74.6 { // carbs exclude fibre
		t.Errorf("flour = %+v", f)
	}
	if want := 0.528; *f.Density != want { // 125 g per 236.588 ml
		t.Errorf("density %v, want %v", *f.Density, want)
	}
	if f.Weights["pack"] != 1000 {
		t.Errorf("weights %v", f.Weights)
	}
	e := got["egg"]
	if e.Kcal != 143.12 || e.Weights["piece"] != 52 { // Atwater energy, second "large" row
		t.Errorf("egg = %+v", e)
	}
}

func TestDeterministic(t *testing.T) {
	a, _, err := generate(t, filepath.Join(fixture, "map.csv"))
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := generate(t, filepath.Join(fixture, "map.csv"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Error("two runs differ")
	}
}

func TestRefusals(t *testing.T) {
	head := "key,fdc_id,portion_fdc_id,density,weights,hand_edited,note\n"
	tail := "soap,,,,,,Not a food\n"
	for name, c := range map[string]struct{ rows, want string }{
		"cooked food":       {"flour,4,,,,,\negg,2,,,,,\n" + tail, "not a raw food"},
		"other data type":   {"flour,3,,,,,\negg,2,,,,,\n" + tail, "not Foundation or SR Legacy"},
		"ambiguous portion": {"flour,1,,,,,\negg,2,,,piece=large,,\n" + tail, "pick one"},
		"unknown portion":   {"flour,1,,pint,,,\negg,2,,,,,\n" + tail, "no portion"},
		"unflagged hand":    {"flour,1,,,pack=500,,\negg,2,,,,,\n" + tail, "hand_edited"},
		"flag without edit": {"flour,1,,cup,,yes,x\negg,2,,,,,\n" + tail, "hand_edited"},
		"missing key":       {"flour,1,,,,,\n" + tail, "has no row"},
		"unknown key":       {"flour,1,,,,,\negg,2,,,,,\nsoap,,,,,,x\nnope,,,,,,x\n", "not a catalogue key"},
		"unmapped no note":  {"flour,1,,,,,\negg,2,,,,,\nsoap,,,,,,\n", "needs a note"},
	} {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "map.csv")
			if err := os.WriteFile(p, []byte(head+c.rows), 0o644); err != nil {
				t.Fatal(err)
			}
			_, _, err := generate(t, p)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %v, want %q", err, c.want)
			}
		})
	}
}

// TestCommittedMap checks the real map against the real catalogue without
// the FDC downloads: every key has a row, hand edits are flagged and
// explained, and nothing maps twice to a different row.
func TestCommittedMap(t *testing.T) {
	keys, err := entryKeys("../../data/entries.json")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := readMap("nutrition_map.csv", keys)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(keys) {
		t.Errorf("%d rows for %d keys", len(rows), len(keys))
	}
}
