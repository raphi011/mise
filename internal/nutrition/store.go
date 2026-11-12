package nutrition

import (
	"context"
	"maps"
	"strings"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/catalog"
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

// Reference looks up the built-in values of a catalogue entry by key.
type Reference func(key string) (Facts, bool)

// CatalogueReference is the Reference backed by the embedded catalogue.
func CatalogueReference(key string) (Facts, bool) {
	n, ok := catalog.NutritionFor(key)
	if !ok {
		return Facts{}, false
	}
	f := Facts{
		Kcal: n.Kcal, Protein: n.Protein, Fat: n.Fat, SatFat: n.SaturatedFat, Carbs: n.Carbs,
		Sugars: n.Sugars, Fibre: n.Fibre, SodiumMg: n.SodiumMg, Density: n.Density,
	}
	if len(n.Weights) > 0 {
		f.Weights = maps.Clone(n.Weights)
	}
	return f, true
}

// known is what is known about a set of items.
type known struct {
	facts map[uuid.UUID]Facts
	// own marks items the household typed values for, catalogue marks items
	// whose catalogue entry has values. An item can be both: the
	// household's values win.
	own, catalogue map[uuid.UUID]bool
}

// facts reads what is known about items. The household's values for an item
// replace the catalogue's; the household's weights are added to the
// catalogue's, a unit at a time. Items without values from either are left
// out, and so are weights of an item without values.
func (s *Service) facts(ctx context.Context, q store.Reader, sc store.Scoped, ids []uuid.UUID) (known, error) {
	out := known{facts: map[uuid.UUID]Facts{}, own: map[uuid.UUID]bool{}, catalogue: map[uuid.UUID]bool{}}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(ids)+1)
	args = append(args, sc.HouseholdID())
	for _, id := range ids {
		args = append(args, id)
	}
	in := placeholders(len(ids))

	if s.reference != nil {
		var linked []struct {
			ID         uuid.UUID
			CatalogKey string
		}
		if err := q.Select(ctx, &linked, `SELECT id, catalog_key FROM items
			WHERE household_id = ? AND catalog_key IS NOT NULL AND id IN (`+in+`)`, args...); err != nil {
			return known{}, err
		}
		for _, l := range linked {
			if f, ok := s.reference(l.CatalogKey); ok {
				out.facts[l.ID] = f
				out.catalogue[l.ID] = true
			}
		}
		// A household has one item per catalogue key, so a second item named
		// like an alias ("Meersalz" next to "Salz") has no key. Its values
		// are the entry's all the same.
		var unlinked []struct {
			ID   uuid.UUID
			Name string
		}
		if err := q.Select(ctx, &unlinked, `SELECT id, name FROM items
			WHERE household_id = ? AND catalog_key IS NULL AND id IN (`+in+`)`, args...); err != nil {
			return known{}, err
		}
		for _, u := range unlinked {
			e, ok := catalog.LookupAny("", u.Name)
			if !ok {
				continue
			}
			if f, ok := s.reference(e.Key); ok {
				out.facts[u.ID] = f
				out.catalogue[u.ID] = true
			}
		}
	}

	var rows []nutritionRow
	if err := q.Select(ctx, &rows, `SELECT item_id, kcal, protein_g, fat_g, saturated_fat_g, carbs_g, sugars_g, fibre_g, sodium_mg, density_g_ml
		FROM item_nutrition WHERE household_id = ? AND item_id IN (`+in+`)`, args...); err != nil {
		return known{}, err
	}
	for _, r := range rows {
		f := r.facts()
		f.Weights = out.facts[r.ItemID].Weights
		out.facts[r.ItemID] = f
		out.own[r.ItemID] = true
	}

	var weights []struct {
		ItemID uuid.UUID
		Unit   string
		Grams  float64
	}
	if err := q.Select(ctx, &weights, `SELECT item_id, unit, grams FROM item_unit_weights
		WHERE household_id = ? AND item_id IN (`+in+`)`, args...); err != nil {
		return known{}, err
	}
	for _, w := range weights {
		f, ok := out.facts[w.ItemID]
		if !ok {
			continue
		}
		if f.Weights == nil {
			f.Weights = map[string]float64{}
		}
		f.Weights[w.Unit] = w.Grams
		out.facts[w.ItemID] = f
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
	k, err := s.facts(ctx, sc.Read(), sc, ids)
	if err != nil {
		return Result{}, err
	}
	return Calculate(lines, k.facts, servings), nil
}

// Item is an item with its nutrition values.
type Item struct {
	ID   uuid.UUID
	Name string
	// Set is true when the household has typed values for the item.
	Set bool
	// Catalogue is true when the item's catalogue entry has values. They
	// are Facts unless Set, and what Clear goes back to.
	Catalogue bool
	Facts     Facts
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
	k, err := s.facts(ctx, q, sc, []uuid.UUID{id})
	if err != nil {
		return Item{}, err
	}
	out.Facts, out.Set, out.Catalogue = k.facts[id], k.own[id], k.catalogue[id]
	if _, ok := k.facts[id]; !ok {
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
