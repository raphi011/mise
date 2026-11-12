package groceries

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/store"
)

// Category groups items in aisle order. Items without one are
// "Uncategorised", which always sorts last.
type Category struct {
	ID       uuid.UUID
	Name     string
	Color    string
	Position int
	// CatalogKey is the catalogue category the household's category stands
	// for ("" for a category it made itself).
	CatalogKey string
	// Items is the number of catalogue items in the category (Categories
	// only).
	Items int
}

// NewCategory describes a category to create.
type NewCategory struct {
	Name  string
	Color string
	// CatalogKey links a seeded category to the catalogue; "" for none.
	CatalogKey string
}

// Categories lists the household's categories in aisle order, with item
// counts.
func (s *Service) Categories(ctx context.Context, sc store.Scoped) ([]Category, error) {
	var rows []Category
	err := sc.Read().Select(ctx, &rows, `SELECT c.id, c.name, c.color, c.position, COALESCE(c.catalog_key, '') AS catalog_key,
			(SELECT count(*) FROM items i WHERE i.household_id = c.household_id AND i.category_id = c.id) AS items
		FROM categories c WHERE c.household_id = ? ORDER BY c.position, c.name_key`, sc.HouseholdID())
	return rows, err
}

// UncategorisedItems counts catalogue items without a category.
func (s *Service) UncategorisedItems(ctx context.Context, sc store.Scoped) (int, error) {
	var n int
	err := sc.Read().QueryRow(ctx, `SELECT count(*) FROM items WHERE household_id = ? AND category_id IS NULL`,
		sc.HouseholdID()).Scan(&n)
	return n, err
}

func checkCategory(c NewCategory) (NewCategory, error) {
	c.Name = normalizeSpace(c.Name)
	errs := FieldErrors{}
	checkName(errs, "name", c.Name)
	checkColor(errs, "color", c.Color)
	return c, errs.orNil()
}

// CreateCategory adds a category at the end of the aisle order.
func (s *Service) CreateCategory(ctx context.Context, sc store.Scoped, c NewCategory) (Category, error) {
	c, err := checkCategory(c)
	if err != nil {
		return Category{}, err
	}
	var out Category
	err = sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		if err := sc.LockHousehold(ctx, q); err != nil {
			return err
		}
		var err error
		out, err = s.insertCategory(ctx, q, sc, c)
		return err
	})
	if err != nil {
		return Category{}, err
	}
	s.publish(sc, catalogChanged)
	return out, nil
}

// insertCategory adds a validated category. The caller holds the household
// lock, so the name check and position cannot race.
func (s *Service) insertCategory(ctx context.Context, q store.Querier, sc store.Scoped, c NewCategory) (Category, error) {
	key := NameKey(c.Name)
	if taken, err := categoryNameTaken(ctx, q, sc, key, uuid.Nil); err != nil {
		return Category{}, err
	} else if taken {
		return Category{}, FieldErrors{"name": "groceries.error.categoryExists"}
	}
	var pos int
	if err := q.QueryRow(ctx, `SELECT COALESCE(MAX(position), -1) + 1 FROM categories WHERE household_id = ?`,
		sc.HouseholdID()).Scan(&pos); err != nil {
		return Category{}, err
	}
	out := Category{ID: store.NewID(), Name: c.Name, Color: c.Color, Position: pos}
	now := s.clock()
	_, err := q.Exec(ctx, `INSERT INTO categories (id, household_id, name, name_key, color, position, catalog_key, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, out.ID, sc.HouseholdID(), out.Name, key, out.Color, pos, nullable(c.CatalogKey), now, now)
	out.CatalogKey = c.CatalogKey
	return out, err
}

func categoryNameTaken(ctx context.Context, q store.Querier, sc store.Scoped, key string, except uuid.UUID) (bool, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM categories WHERE household_id = ? AND name_key = ? AND id <> ?`,
		sc.HouseholdID(), key, except).Scan(&n)
	return n > 0, err
}

// UpdateCategory renames a category and changes its colour.
func (s *Service) UpdateCategory(ctx context.Context, sc store.Scoped, id uuid.UUID, c NewCategory) error {
	c, err := checkCategory(c)
	if err != nil {
		return err
	}
	err = sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		if err := sc.LockHousehold(ctx, q); err != nil {
			return err
		}
		key := NameKey(c.Name)
		if taken, err := categoryNameTaken(ctx, q, sc, key, id); err != nil {
			return err
		} else if taken {
			return FieldErrors{"name": "groceries.error.categoryExists"}
		}
		res, err := q.Exec(ctx, `UPDATE categories SET name = ?, name_key = ?, color = ?, updated_at = ?
			WHERE household_id = ? AND id = ?`, c.Name, key, c.Color, s.clock(), sc.HouseholdID(), id)
		if err != nil {
			return err
		}
		return mustAffect(res)
	})
	if err != nil {
		return err
	}
	s.publish(sc, catalogChanged)
	return nil
}

// MoveCategory moves a category to index to in the aisle order (clamped).
func (s *Service) MoveCategory(ctx context.Context, sc store.Scoped, id uuid.UUID, to int) error {
	err := sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		return s.reorder(ctx, q, sc, "categories", "", id, to)
	})
	if err != nil {
		return err
	}
	s.publish(sc, catalogChanged)
	return nil
}

// DeleteCategory deletes a category; its items become uncategorised. The
// foreign key restricts deletes, so the items are updated first in the same
// transaction.
func (s *Service) DeleteCategory(ctx context.Context, sc store.Scoped, id uuid.UUID) error {
	err := sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		// Ordered against UpdateEntry, which checks the category exists
		// before pointing an item at it.
		if err := sc.LockHousehold(ctx, q); err != nil {
			return err
		}
		hh := sc.HouseholdID()
		if _, err := q.Exec(ctx, `UPDATE items SET category_id = NULL, updated_at = ? WHERE household_id = ? AND category_id = ?`,
			s.clock(), hh, id); err != nil {
			return err
		}
		res, err := q.Exec(ctx, `DELETE FROM categories WHERE household_id = ? AND id = ?`, hh, id)
		if err != nil {
			return err
		}
		return mustAffect(res)
	})
	if err != nil {
		return err
	}
	s.publish(sc, catalogChanged)
	return nil
}

// reorder moves id to index to among the household's rows of table (in
// position order, ties by creation) and rewrites the positions that
// changed. live is an extra condition selecting the rows that take part.
func (s *Service) reorder(ctx context.Context, q store.Querier, sc store.Scoped, table, live string, id uuid.UUID, to int) error {
	if err := sc.LockHousehold(ctx, q); err != nil {
		return err
	}
	where := `household_id = ?`
	if live != "" {
		where += ` AND ` + live
	}
	var ids []uuid.UUID
	if err := q.Select(ctx, &ids, `SELECT id FROM `+table+` WHERE `+where+` ORDER BY position, created_at, id`,
		sc.HouseholdID()); err != nil {
		return err
	}
	from := -1
	for i, v := range ids {
		if v == id {
			from = i
		}
	}
	if from < 0 {
		return ErrNotFound
	}
	to = max(0, min(to, len(ids)-1))
	moved := append([]uuid.UUID{}, ids[:from]...)
	moved = append(moved, ids[from+1:]...)
	moved = append(moved[:to], append([]uuid.UUID{id}, moved[to:]...)...)
	now := s.clock()
	for pos, v := range moved {
		if _, err := q.Exec(ctx, `UPDATE `+table+` SET position = ?, updated_at = ? WHERE household_id = ? AND id = ?`,
			pos, now, sc.HouseholdID(), v); err != nil {
			return err
		}
	}
	return nil
}

// mustAffect turns an update or delete that matched no row into
// ErrNotFound.
func mustAffect(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// notFound maps sql.ErrNoRows to ErrNotFound.
func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
