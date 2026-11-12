package nutrition

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/live"
	"github.com/raphi011/mise/internal/store"
)

type nutritionRow struct {
	ItemID        uuid.UUID
	Kcal          float64
	ProteinG      *float64
	FatG          *float64
	SaturatedFatG *float64
	CarbsG        *float64
	SugarsG       *float64
	FibreG        *float64
	SodiumMg      *float64
	DensityGMl    *float64
}

func (r nutritionRow) facts() Facts {
	return Facts{
		Kcal: r.Kcal, Protein: r.ProteinG, Fat: r.FatG, SatFat: r.SaturatedFatG, Carbs: r.CarbsG,
		Sugars: r.SugarsG, Fibre: r.FibreG, SodiumMg: r.SodiumMg, Density: r.DensityGMl,
	}
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

// facts reads what is known about items. Items without values are left out
// of the result; an item with only weights has none either.
func facts(ctx context.Context, q store.Reader, sc store.Scoped, ids []uuid.UUID) (map[uuid.UUID]Facts, error) {
	out := map[uuid.UUID]Facts{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(ids)+1)
	args = append(args, sc.HouseholdID())
	for _, id := range ids {
		args = append(args, id)
	}
	in := placeholders(len(ids))
	var rows []nutritionRow
	if err := q.Select(ctx, &rows, `SELECT item_id, kcal, protein_g, fat_g, saturated_fat_g, carbs_g, sugars_g, fibre_g, sodium_mg, density_g_ml
		FROM item_nutrition WHERE household_id = ? AND item_id IN (`+in+`)`, args...); err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ItemID] = r.facts()
	}
	var weights []struct {
		ItemID uuid.UUID
		Unit   string
		Grams  float64
	}
	if err := q.Select(ctx, &weights, `SELECT item_id, unit, grams FROM item_unit_weights
		WHERE household_id = ? AND item_id IN (`+in+`)`, args...); err != nil {
		return nil, err
	}
	for _, w := range weights {
		f, ok := out[w.ItemID]
		if !ok {
			continue
		}
		if f.Weights == nil {
			f.Weights = map[string]float64{}
		}
		f.Weights[w.Unit] = w.Grams
		out[w.ItemID] = f
	}
	return out, nil
}

// Recipe estimates the nutrition of a recipe's ingredient lines at the
// recipe's own servings.
func (s *Service) Recipe(ctx context.Context, sc store.Scoped, lines []Line, servings int) (Result, error) {
	ids := make([]uuid.UUID, 0, len(lines))
	for _, l := range lines {
		ids = append(ids, l.ItemID)
	}
	f, err := facts(ctx, sc.Read(), sc, ids)
	if err != nil {
		return Result{}, err
	}
	return Calculate(lines, f, servings), nil
}

// Item is an item with the household's nutrition values.
type Item struct {
	ID   uuid.UUID
	Name string
	// Set is true when the household has typed values for the item.
	Set   bool
	Facts Facts
	// Recipes counts the live recipes that use the item.
	Recipes int
}

// Get returns the item with its values. It returns ErrNotFound when the
// household has no such item.
func (s *Service) Get(ctx context.Context, sc store.Scoped, id uuid.UUID) (Item, error) {
	q := sc.Read()
	name, err := itemName(ctx, q, sc, id)
	if err != nil {
		return Item{}, err
	}
	out := Item{ID: id, Name: name}
	f, err := facts(ctx, q, sc, []uuid.UUID{id})
	if err != nil {
		return Item{}, err
	}
	out.Facts, out.Set = f[id]
	if !out.Set {
		// Weights can exist without values; show them too.
		var weights []struct {
			Unit  string
			Grams float64
		}
		if err := q.Select(ctx, &weights, `SELECT unit, grams FROM item_unit_weights WHERE household_id = ? AND item_id = ?`, sc.HouseholdID(), id); err != nil {
			return Item{}, err
		}
		for _, w := range weights {
			if out.Facts.Weights == nil {
				out.Facts.Weights = map[string]float64{}
			}
			out.Facts.Weights[w.Unit] = w.Grams
		}
	}
	err = q.Get(ctx, &out.Recipes, `SELECT count(DISTINCT ri.recipe_id) FROM recipe_ingredients ri
		JOIN recipes r ON r.household_id = ri.household_id AND r.id = ri.recipe_id
		WHERE ri.household_id = ? AND ri.item_id = ? AND r.deleted_at IS NULL AND r.draft = ?`, sc.HouseholdID(), id, false)
	return out, err
}

// Input is what the editor submits for an item. Values are per 100 g.
type Input struct {
	Kcal float64
	// Optional values are nil when left empty.
	Protein, Fat, SatFat, Carbs, Sugars, Fibre *float64
	// SaltG is the salt per 100 g as printed on EU packs; it is stored as
	// sodium.
	SaltG *float64
	// Density is grams per millilitre.
	Density *float64
	// Weights are grams per unit, keyed by one of WeightUnits.
	Weights map[string]float64
}

const (
	maxKcal   = 900
	maxPer100 = 100
	// sodiumPerSalt converts grams of salt to milligrams of sodium.
	sodiumPerSalt = 400.0
	minDensity    = 0.05
	maxDensity    = 25
	maxWeight     = 10000
	// macroSlack allows for rounding on the pack.
	macroSlack = 0.5
)

func inRange(v float64) bool { return v >= 0 && v <= maxPer100 }

// clean checks the input and returns what is wrong with it.
func (in Input) clean() error {
	errs := FieldErrors{}
	if in.Kcal < 0 || in.Kcal > maxKcal {
		errs["kcal"] = "nutrition.error.range"
	}
	for field, v := range map[string]*float64{
		"protein": in.Protein, "fat": in.Fat, "saturated_fat": in.SatFat, "carbs": in.Carbs,
		"sugars": in.Sugars, "fibre": in.Fibre, "salt": in.SaltG,
	} {
		if v != nil && !inRange(*v) {
			errs[field] = "nutrition.error.range"
		}
	}
	if len(errs) == 0 {
		var sum float64
		for _, v := range []*float64{in.Protein, in.Fat, in.Carbs, in.Fibre} {
			if v != nil {
				sum += *v
			}
		}
		if sum > maxPer100+macroSlack {
			errs["fibre"] = "nutrition.error.macros"
		}
		if in.SatFat != nil && in.Fat != nil && *in.SatFat > *in.Fat+macroSlack {
			errs["saturated_fat"] = "nutrition.error.part"
		}
		if in.Sugars != nil && in.Carbs != nil && *in.Sugars > *in.Carbs+macroSlack {
			errs["sugars"] = "nutrition.error.part"
		}
	}
	if d := in.Density; d != nil && (*d < minDensity || *d > maxDensity) {
		errs["density"] = "nutrition.error.range"
	}
	for unit, g := range in.Weights {
		field := "weight." + unit
		switch {
		case !validUnit(unit):
			errs[field] = "form.error.choice"
		case g <= 0 || g > maxWeight:
			errs[field] = "nutrition.error.range"
		}
	}
	return errs.orNil()
}

func validUnit(u string) bool {
	for _, w := range WeightUnits {
		if u == w {
			return true
		}
	}
	return false
}

func sodium(salt *float64) *float64 {
	if salt == nil {
		return nil
	}
	v := *salt * sodiumPerSalt
	return &v
}

// Save replaces the household's values and weights for an item. It returns
// ErrNotFound when the household has no such item.
func (s *Service) Save(ctx context.Context, sc store.Scoped, by, id uuid.UUID, in Input) error {
	if err := in.clean(); err != nil {
		return err
	}
	hh := sc.HouseholdID()
	err := sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		if _, err := itemName(ctx, q.Reader, sc, id); err != nil {
			return err
		}
		now := s.clock()
		if _, err := q.Exec(ctx, `INSERT INTO item_nutrition (household_id, item_id, kcal, protein_g, fat_g, saturated_fat_g,
				carbs_g, sugars_g, fibre_g, sodium_mg, density_g_ml, updated_by, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (household_id, item_id) DO UPDATE SET kcal = excluded.kcal, protein_g = excluded.protein_g,
				fat_g = excluded.fat_g, saturated_fat_g = excluded.saturated_fat_g, carbs_g = excluded.carbs_g,
				sugars_g = excluded.sugars_g, fibre_g = excluded.fibre_g, sodium_mg = excluded.sodium_mg,
				density_g_ml = excluded.density_g_ml, updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
			hh, id, in.Kcal, in.Protein, in.Fat, in.SatFat, in.Carbs, in.Sugars, in.Fibre, sodium(in.SaltG), in.Density,
			by, now, now); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, `DELETE FROM item_unit_weights WHERE household_id = ? AND item_id = ?`, hh, id); err != nil {
			return err
		}
		for _, unit := range WeightUnits {
			g, ok := in.Weights[unit]
			if !ok {
				continue
			}
			if _, err := q.Exec(ctx, `INSERT INTO item_unit_weights (household_id, item_id, unit, grams, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?)`, hh, id, unit, g, now, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return s.publishRecipes(ctx, sc, id)
}

// Clear removes the household's values and weights for an item. It returns
// ErrNotFound when the household has no such item.
func (s *Service) Clear(ctx context.Context, sc store.Scoped, id uuid.UUID) error {
	hh := sc.HouseholdID()
	err := sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		if _, err := itemName(ctx, q.Reader, sc, id); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, `DELETE FROM item_unit_weights WHERE household_id = ? AND item_id = ?`, hh, id); err != nil {
			return err
		}
		_, err := q.Exec(ctx, `DELETE FROM item_nutrition WHERE household_id = ? AND item_id = ?`, hh, id)
		return err
	})
	if err != nil {
		return err
	}
	return s.publishRecipes(ctx, sc, id)
}

// publishRecipes tells open recipe pages that use the item to recalculate.
func (s *Service) publishRecipes(ctx context.Context, sc store.Scoped, item uuid.UUID) error {
	if s.pub == nil {
		return nil
	}
	var ids []uuid.UUID
	if err := sc.Read().Select(ctx, &ids, `SELECT DISTINCT ri.recipe_id FROM recipe_ingredients ri
		JOIN recipes r ON r.household_id = ri.household_id AND r.id = ri.recipe_id
		WHERE ri.household_id = ? AND ri.item_id = ? AND r.deleted_at IS NULL`, sc.HouseholdID(), item); err != nil {
		return err
	}
	events := make([]live.Event, 0, len(ids))
	for _, id := range ids {
		events = append(events, live.Event{Kind: live.RecipeChanged, RecipeID: id})
	}
	if len(events) > 0 {
		s.pub.Publish(sc.HouseholdID(), events...)
	}
	return nil
}
