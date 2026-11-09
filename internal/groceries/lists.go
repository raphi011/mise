package groceries

import (
	"context"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/store"
)

// List is a shopping list. The first one is the default.
type List struct {
	ID       uuid.UUID
	Name     string
	Position int
	// Open counts entries still to get.
	Open int
}

const listColumns = `l.id, l.name, l.position,
	(SELECT count(*) FROM list_entries e
	  WHERE e.household_id = l.household_id AND e.list_id = l.id AND e.checked_at IS NULL AND e.deleted_at IS NULL) AS open`

// Lists returns the household's lists in order.
func (s *Service) Lists(ctx context.Context, sc store.Scoped) ([]List, error) {
	var rows []List
	err := sc.Read().Select(ctx, &rows, `SELECT `+listColumns+` FROM shopping_lists l
		WHERE l.household_id = ? AND l.deleted_at IS NULL ORDER BY l.position, l.created_at, l.id`, sc.HouseholdID())
	return rows, err
}

// List returns one list, or ErrNotFound when it does not exist or was
// deleted.
func (s *Service) List(ctx context.Context, sc store.Scoped, id uuid.UUID) (List, error) {
	var l List
	err := sc.Read().Get(ctx, &l, `SELECT `+listColumns+` FROM shopping_lists l
		WHERE l.household_id = ? AND l.id = ? AND l.deleted_at IS NULL`, sc.HouseholdID(), id)
	return l, notFound(err)
}

// Defaults is what a household starts with: its first list and categories,
// in the household's default language. Setup creates them (SeedTx); Start
// backfills households created before Groceries existed.
type Defaults struct {
	ListName   string
	Categories []NewCategory
}

// Start makes sure the household has its defaults and returns its first
// list. It creates the list from d when the household has never had one, and
// the default categories if it has none. It is idempotent and safe to run
// concurrently; requests never call it (every GET is read-only), the server
// runs it at startup for households created before Groceries existed.
func (s *Service) Start(ctx context.Context, sc store.Scoped, d Defaults) (List, error) {
	created := false
	err := sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		var err error
		created, err = s.seed(ctx, q, sc, d)
		return err
	})
	if err != nil {
		return List{}, err
	}
	if created {
		s.publish(sc, listsChanged, catalogChanged)
	}
	return s.FirstList(ctx, sc)
}

// SeedTx creates the household's defaults inside the caller's transaction,
// e.g. the one creating the household during first-run setup. Like Start it
// does nothing for a household that already has lists.
func (s *Service) SeedTx(ctx context.Context, q store.Querier, sc store.Scoped, d Defaults) error {
	_, err := s.seed(ctx, q, sc, d)
	return err
}

// seed creates the defaults under the household lock and reports whether it
// did.
func (s *Service) seed(ctx context.Context, q store.Querier, sc store.Scoped, d Defaults) (bool, error) {
	if err := sc.LockHousehold(ctx, q); err != nil {
		return false, err
	}
	hh := sc.HouseholdID()
	// Deleted lists count: the last list cannot be deleted, so a household
	// with only deleted lists is never new.
	var n int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM shopping_lists WHERE household_id = ?`, hh).Scan(&n); err != nil {
		return false, err
	}
	if n > 0 {
		return false, nil
	}
	if _, err := s.insertList(ctx, q, sc, d.ListName); err != nil {
		return false, err
	}
	if err := q.QueryRow(ctx, `SELECT count(*) FROM categories WHERE household_id = ?`, hh).Scan(&n); err != nil {
		return false, err
	}
	if n == 0 {
		for _, c := range d.Categories {
			c, err := checkCategory(c)
			if err != nil {
				return false, err
			}
			if _, err := s.insertCategory(ctx, q, sc, c); err != nil {
				return false, err
			}
		}
	}
	return true, nil
}

// FirstList returns the household's first list (the default list), or
// ErrNotFound if it has none.
func (s *Service) FirstList(ctx context.Context, sc store.Scoped) (List, error) {
	lists, err := s.Lists(ctx, sc)
	if err != nil {
		return List{}, err
	}
	if len(lists) == 0 {
		return List{}, ErrNotFound
	}
	return lists[0], nil
}

func checkListName(name string) (string, error) {
	name = normalizeSpace(name)
	errs := FieldErrors{}
	checkName(errs, "name", name)
	return name, errs.orNil()
}

// insertList adds a list at the end. The caller holds the household lock.
func (s *Service) insertList(ctx context.Context, q store.Querier, sc store.Scoped, name string) (List, error) {
	name, err := checkListName(name)
	if err != nil {
		return List{}, err
	}
	var pos int
	if err := q.QueryRow(ctx, `SELECT COALESCE(MAX(position), -1) + 1 FROM shopping_lists WHERE household_id = ? AND deleted_at IS NULL`,
		sc.HouseholdID()).Scan(&pos); err != nil {
		return List{}, err
	}
	l := List{ID: store.NewID(), Name: name, Position: pos}
	now := s.clock()
	_, err = q.Exec(ctx, `INSERT INTO shopping_lists (id, household_id, name, position, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`, l.ID, sc.HouseholdID(), l.Name, pos, now, now)
	return l, err
}

// CreateList adds a list at the end.
func (s *Service) CreateList(ctx context.Context, sc store.Scoped, name string) (List, error) {
	if _, err := checkListName(name); err != nil {
		return List{}, err
	}
	var l List
	err := sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		if err := sc.LockHousehold(ctx, q); err != nil {
			return err
		}
		var err error
		l, err = s.insertList(ctx, q, sc, name)
		return err
	})
	if err != nil {
		return List{}, err
	}
	s.publish(sc, listsChanged)
	return l, nil
}

// RenameList renames a list.
func (s *Service) RenameList(ctx context.Context, sc store.Scoped, id uuid.UUID, name string) error {
	name, err := checkListName(name)
	if err != nil {
		return err
	}
	err = sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		res, err := q.Exec(ctx, `UPDATE shopping_lists SET name = ?, updated_at = ?
			WHERE household_id = ? AND id = ? AND deleted_at IS NULL`, name, s.clock(), sc.HouseholdID(), id)
		if err != nil {
			return err
		}
		return mustAffect(res)
	})
	if err != nil {
		return err
	}
	s.publish(sc, listsChanged)
	return nil
}

// MoveList moves a list to index to (clamped); index 0 is the default list.
func (s *Service) MoveList(ctx context.Context, sc store.Scoped, id uuid.UUID, to int) error {
	err := sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		return s.reorder(ctx, q, sc, "shopping_lists", "deleted_at IS NULL", id, to)
	})
	if err != nil {
		return err
	}
	s.publish(sc, listsChanged)
	return nil
}

// DeleteList deletes a list with its entries. It can be restored with
// RestoreList within UndoWindow. The household's last list cannot be
// deleted (ErrLastList).
func (s *Service) DeleteList(ctx context.Context, sc store.Scoped, id uuid.UUID) error {
	err := sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		if err := sc.LockHousehold(ctx, q); err != nil {
			return err
		}
		var n int
		if err := q.QueryRow(ctx, `SELECT count(*) FROM shopping_lists WHERE household_id = ? AND deleted_at IS NULL`,
			sc.HouseholdID()).Scan(&n); err != nil {
			return err
		}
		now := s.clock()
		res, err := q.Exec(ctx, `UPDATE shopping_lists SET deleted_at = ?, updated_at = ?
			WHERE household_id = ? AND id = ? AND deleted_at IS NULL`, now, now, sc.HouseholdID(), id)
		if err != nil {
			return err
		}
		if err := mustAffect(res); err != nil {
			return err
		}
		if n <= 1 {
			return ErrLastList
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.publish(sc, listsChanged, listChanged(id))
	return nil
}

// RestoreList undoes DeleteList within UndoWindow. The list goes back to
// the end of the order.
func (s *Service) RestoreList(ctx context.Context, sc store.Scoped, id uuid.UUID) error {
	err := sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		if err := sc.LockHousehold(ctx, q); err != nil {
			return err
		}
		hh := sc.HouseholdID()
		var pos int
		if err := q.QueryRow(ctx, `SELECT COALESCE(MAX(position), -1) + 1 FROM shopping_lists WHERE household_id = ? AND deleted_at IS NULL`,
			hh).Scan(&pos); err != nil {
			return err
		}
		res, err := q.Exec(ctx, `UPDATE shopping_lists SET deleted_at = NULL, position = ?, updated_at = ?
			WHERE household_id = ? AND id = ? AND deleted_at >= ?`, pos, s.clock(), hh, id, s.undoCutoff())
		if err != nil {
			return err
		}
		return mustAffect(res)
	})
	if err != nil {
		return err
	}
	s.publish(sc, listsChanged, listChanged(id))
	return nil
}
