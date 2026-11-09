package recipes

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/groceries"
	"github.com/raphi011/mise/internal/store"
)

// Tag labels recipes. Pinned tags show as quick filters on Recipes.
type Tag struct {
	ID     uuid.UUID
	Name   string
	Pinned bool
	// Count is the number of recipes with the tag, where the query says so.
	Count int
}

// Tags returns the household's tags by name, with recipe counts.
func (s *Service) Tags(ctx context.Context, sc store.Scoped) ([]Tag, error) {
	var out []Tag
	err := sc.Read().Select(ctx, &out, `SELECT t.id, t.name, t.pinned,
			(SELECT count(*) FROM recipe_tags rt
			 JOIN recipes r ON r.household_id = rt.household_id AND r.id = rt.recipe_id
			 WHERE rt.household_id = t.household_id AND rt.tag_id = t.id AND r.deleted_at IS NULL AND r.draft = ?) AS count
		FROM tags t WHERE t.household_id = ? ORDER BY t.name_key`, false, sc.HouseholdID())
	return out, err
}

func checkTagName(name string) (string, error) {
	name = groceries.CleanName(name)
	errs := FieldErrors{}
	if name == "" {
		errs["name"] = "form.error.required"
	}
	checkLen(errs, "name", name, maxTagLen)
	return name, errs.orNil()
}

// ensureTag returns the ID of the tag named name, creating it if needed.
func ensureTag(ctx context.Context, q store.Querier, sc store.Scoped, name string, now store.Time) (uuid.UUID, error) {
	hh := sc.HouseholdID()
	key := groceries.NameKey(name)
	// On Postgres the row lock keeps a concurrent DeleteTag or MergeTag
	// from removing the tag before the recipe_tags row referencing it
	// commits. A tag deleted between the insert and the select is created
	// again.
	query := `SELECT id FROM tags WHERE household_id = ? AND name_key = ?`
	if q.Dialect() == store.Postgres {
		query += ` FOR SHARE`
	}
	for range 3 {
		if _, err := q.Exec(ctx, `INSERT INTO tags (id, household_id, name, name_key, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (household_id, name_key) DO NOTHING`,
			store.NewID(), hh, name, key, now, now); err != nil {
			return uuid.Nil, err
		}
		var id uuid.UUID
		err := q.Get(ctx, &id, query, hh, key)
		if !errors.Is(err, sql.ErrNoRows) {
			return id, err
		}
	}
	return uuid.Nil, errors.New("recipes: tag keeps being deleted")
}

func tagNameTaken(ctx context.Context, q store.Querier, sc store.Scoped, key string, except uuid.UUID) (bool, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM tags WHERE household_id = ? AND name_key = ? AND id <> ?`,
		sc.HouseholdID(), key, except).Scan(&n)
	return n > 0, err
}

// CreateTag adds a tag.
func (s *Service) CreateTag(ctx context.Context, sc store.Scoped, name string) (Tag, error) {
	name, err := checkTagName(name)
	if err != nil {
		return Tag{}, err
	}
	t := Tag{ID: store.NewID(), Name: name}
	err = sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		key := groceries.NameKey(name)
		if taken, err := tagNameTaken(ctx, q, sc, key, uuid.Nil); err != nil {
			return err
		} else if taken {
			return FieldErrors{"name": "recipes.error.tagExists"}
		}
		now := s.clock()
		_, err := q.Exec(ctx, `INSERT INTO tags (id, household_id, name, name_key, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)`, t.ID, sc.HouseholdID(), name, key, now, now)
		if store.IsUniqueViolation(err) {
			// Another request added the name after the check above.
			return FieldErrors{"name": "recipes.error.tagExists"}
		}
		return err
	})
	if err != nil {
		return Tag{}, err
	}
	s.publish(sc, recipesChanged, tagsChanged)
	return t, nil
}

// RenameTag renames a tag on every recipe that has it. To fold a tag into
// one that already has the name, use MergeTag.
func (s *Service) RenameTag(ctx context.Context, sc store.Scoped, id uuid.UUID, name string) error {
	name, err := checkTagName(name)
	if err != nil {
		return err
	}
	err = sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		key := groceries.NameKey(name)
		if taken, err := tagNameTaken(ctx, q, sc, key, id); err != nil {
			return err
		} else if taken {
			return FieldErrors{"name": "recipes.error.tagExists"}
		}
		res, err := q.Exec(ctx, `UPDATE tags SET name = ?, name_key = ?, updated_at = ? WHERE household_id = ? AND id = ?`,
			name, key, s.clock(), sc.HouseholdID(), id)
		if store.IsUniqueViolation(err) {
			return FieldErrors{"name": "recipes.error.tagExists"}
		}
		if err != nil {
			return err
		}
		return mustAffect(res)
	})
	if err != nil {
		return err
	}
	s.publish(sc, recipesChanged, tagsChanged)
	return nil
}

// MergeTag moves every recipe of tag from to tag into and deletes from.
func (s *Service) MergeTag(ctx context.Context, sc store.Scoped, from, into uuid.UUID) error {
	if from == into {
		return FieldErrors{"into": "form.error.choice"}
	}
	merge := func(ctx context.Context, q store.Querier) error {
		hh := sc.HouseholdID()
		var n int
		if err := q.QueryRow(ctx, `SELECT count(*) FROM tags WHERE household_id = ? AND id IN (?, ?)`, hh, from, into).
			Scan(&n); err != nil {
			return err
		}
		if n != 2 {
			return ErrNotFound
		}
		// Retag recipes that don't have into yet; the rest keep their
		// into row.
		if _, err := q.Exec(ctx, `UPDATE recipe_tags SET tag_id = ?, updated_at = ?
			WHERE household_id = ? AND tag_id = ?
			AND NOT EXISTS (SELECT 1 FROM recipe_tags x
			                WHERE x.household_id = recipe_tags.household_id AND x.recipe_id = recipe_tags.recipe_id
			                AND x.tag_id = ?)`, into, s.clock(), hh, from, into); err != nil {
			return err
		}
		// Remaining recipe_tags rows of from go with it (ON DELETE CASCADE).
		_, err := q.Exec(ctx, `DELETE FROM tags WHERE household_id = ? AND id = ?`, hh, from)
		return err
	}
	// A recipe edit that gives a recipe tag into while the merge runs makes
	// the retag hit the primary key (Postgres; SQLite has one writer).
	// Again, the merge sees that row and skips the recipe.
	var err error
	for range 3 {
		if err = sc.Tx(ctx, merge); !store.IsUniqueViolation(err) {
			break
		}
	}
	if err != nil {
		return err
	}
	s.publish(sc, recipesChanged, tagsChanged)
	return nil
}

// DeleteTag deletes a tag and removes it from every recipe.
func (s *Service) DeleteTag(ctx context.Context, sc store.Scoped, id uuid.UUID) error {
	err := sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		res, err := q.Exec(ctx, `DELETE FROM tags WHERE household_id = ? AND id = ?`, sc.HouseholdID(), id)
		if err != nil {
			return err
		}
		return mustAffect(res)
	})
	if err != nil {
		return err
	}
	s.publish(sc, recipesChanged, tagsChanged)
	return nil
}

// SetTagPinned shows or hides a tag as a quick filter on Recipes.
func (s *Service) SetTagPinned(ctx context.Context, sc store.Scoped, id uuid.UUID, pinned bool) error {
	err := sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		res, err := q.Exec(ctx, `UPDATE tags SET pinned = ?, updated_at = ? WHERE household_id = ? AND id = ?`,
			pinned, s.clock(), sc.HouseholdID(), id)
		if err != nil {
			return err
		}
		return mustAffect(res)
	})
	if err != nil {
		return err
	}
	s.publish(sc, recipesChanged, tagsChanged)
	return nil
}
