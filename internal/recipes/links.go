package recipes

import (
	"context"
	"slices"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/groceries"
	"github.com/raphi011/mise/internal/store"
)

// maxLinks bounds a recipe's "Goes well with" list.
const maxLinks = 20

// Link is a recipe linked to another ("Goes well with"), as the detail
// page lists it.
type Link struct {
	ID       uuid.UUID
	Slug     string
	Name     string
	Photo    string
	TotalMin *int
}

// cleanLinks drops duplicate and self links, keeping the first order.
func cleanLinks(links []uuid.UUID, self uuid.UUID) []uuid.UUID {
	out := links[:0:0]
	for _, id := range links {
		if id == uuid.Nil || id == self || slices.Contains(out, id) {
			continue
		}
		out = append(out, id)
	}
	return out
}

// linkedIDs returns the recipes linked to id, live or in the bin.
func linkedIDs(ctx context.Context, q store.Querier, sc store.Scoped, id uuid.UUID) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := q.Select(ctx, &ids, `SELECT linked_id FROM recipe_links WHERE household_id = ? AND recipe_id = ?`,
		sc.HouseholdID(), id)
	return ids, err
}

// writeLinks replaces the links of recipe id with links, both ways. IDs
// that are not live, published recipes of the household are ignored. It
// returns every recipe whose links changed, for their pages to refresh.
func writeLinks(ctx context.Context, q store.Querier, sc store.Scoped, id uuid.UUID, links []uuid.UUID, now store.Time) ([]uuid.UUID, error) {
	hh := sc.HouseholdID()
	old, err := linkedIDs(ctx, q, sc, id)
	if err != nil {
		return nil, err
	}
	var keep []uuid.UUID
	if len(links) > 0 {
		args := []any{hh, false}
		for _, l := range links {
			args = append(args, l)
		}
		if err := q.Select(ctx, &keep, `SELECT id FROM recipes WHERE household_id = ? AND deleted_at IS NULL AND draft = ?
			AND id IN (`+placeholders(len(links))+`)`, args...); err != nil {
			return nil, err
		}
	}
	// Links to recipes in the bin stay, so a restore brings them back.
	if len(old) > 0 {
		args := []any{hh}
		for _, l := range old {
			args = append(args, l)
		}
		var binned []uuid.UUID
		if err := q.Select(ctx, &binned, `SELECT id FROM recipes WHERE household_id = ? AND deleted_at IS NOT NULL
			AND id IN (`+placeholders(len(old))+`)`, args...); err != nil {
			return nil, err
		}
		keep = append(keep, binned...)
	}
	if _, err := q.Exec(ctx, `DELETE FROM recipe_links WHERE household_id = ? AND (recipe_id = ? OR linked_id = ?)`,
		hh, id, id); err != nil {
		return nil, err
	}
	for _, l := range keep {
		for _, pair := range [][2]uuid.UUID{{id, l}, {l, id}} {
			if _, err := q.Exec(ctx, `INSERT INTO recipe_links (household_id, recipe_id, linked_id, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?)`, hh, pair[0], pair[1], now, now); err != nil {
				return nil, err
			}
		}
	}
	changed := slices.Clone(old)
	for _, l := range keep {
		if !slices.Contains(changed, l) {
			changed = append(changed, l)
		}
	}
	return changed, nil
}

// links returns the live, published recipes linked to id, by name.
func links(ctx context.Context, q store.Reader, sc store.Scoped, id uuid.UUID) ([]Link, error) {
	var rows []struct {
		ID      uuid.UUID
		Slug    string
		Name    string
		NameKey string
		Photo   *string
		PrepMin *int
		CookMin *int
	}
	if err := q.Select(ctx, &rows, `SELECT r.id, r.slug, r.name, r.name_key, r.photo, r.prep_min, r.cook_min
		FROM recipe_links l JOIN recipes r ON r.household_id = l.household_id AND r.id = l.linked_id
		WHERE l.household_id = ? AND l.recipe_id = ? AND r.deleted_at IS NULL AND r.draft = ?
		ORDER BY r.name_key, r.id`, sc.HouseholdID(), id, false); err != nil {
		return nil, err
	}
	out := make([]Link, len(rows))
	for i, r := range rows {
		out[i] = Link{ID: r.ID, Slug: r.Slug, Name: r.Name, Photo: deref(r.Photo), TotalMin: totalMin(r.PrepMin, r.CookMin)}
	}
	return out, nil
}

// RecipeName is a recipe's ID and name, for pickers.
type RecipeName struct {
	ID   uuid.UUID
	Name string
}

// Names returns the household's live, published recipes by name, for the
// editor's "Goes well with" picker.
func (s *Service) Names(ctx context.Context, sc store.Scoped) ([]RecipeName, error) {
	var out []RecipeName
	err := sc.Read().Select(ctx, &out, `SELECT id, name FROM recipes WHERE household_id = ? AND deleted_at IS NULL AND draft = ?
		ORDER BY name_key, created_at, id`, sc.HouseholdID(), false)
	return out, err
}

// FindByName returns the live, published recipe whose name matches name
// case-insensitively; with several, the oldest. ok is false for none.
func (s *Service) FindByName(ctx context.Context, sc store.Scoped, name string) (RecipeName, bool, error) {
	key := groceries.NameKey(name)
	if key == "" {
		return RecipeName{}, false, nil
	}
	var out []RecipeName
	if err := sc.Read().Select(ctx, &out, `SELECT id, name FROM recipes WHERE household_id = ? AND name_key = ?
		AND deleted_at IS NULL AND draft = ? ORDER BY created_at, id`, sc.HouseholdID(), key, false); err != nil {
		return RecipeName{}, false, err
	}
	if len(out) == 0 {
		return RecipeName{}, false, nil
	}
	return out[0], true, nil
}

// NamesOf returns the names of the live, published recipes among ids, in
// the order of ids; others are left out.
func (s *Service) NamesOf(ctx context.Context, sc store.Scoped, ids []uuid.UUID) ([]RecipeName, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := []any{sc.HouseholdID(), false}
	for _, id := range ids {
		args = append(args, id)
	}
	var rows []RecipeName
	if err := sc.Read().Select(ctx, &rows, `SELECT id, name FROM recipes WHERE household_id = ? AND deleted_at IS NULL AND draft = ?
		AND id IN (`+placeholders(len(ids))+`)`, args...); err != nil {
		return nil, err
	}
	out := make([]RecipeName, 0, len(rows))
	for _, id := range ids {
		if i := slices.IndexFunc(rows, func(r RecipeName) bool { return r.ID == id }); i >= 0 && !slices.ContainsFunc(out, func(r RecipeName) bool { return r.ID == id }) {
			out = append(out, rows[i])
		}
	}
	return out, nil
}
