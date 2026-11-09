package groceries

import (
	"context"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"golang.org/x/text/language"

	"github.com/raphi011/mise/internal/live"
	"github.com/raphi011/mise/internal/store"
)

// Entry is an item on a list.
type Entry struct {
	ID       uuid.UUID
	ListID   uuid.UUID
	ItemID   uuid.UUID
	ItemName string
	// Category is nil for uncategorised items.
	Category *Category
	Amount   Amount
	Note     string
	AddedBy  uuid.UUID
	// AddedByName and AddedByColor describe the member who added it.
	AddedByName  string
	AddedByColor string
	// CheckedAt is set while the entry is in the cart.
	CheckedAt *time.Time
}

// Checked reports whether the entry is in the cart.
func (e Entry) Checked() bool { return e.CheckedAt != nil }

type entryRow struct {
	ID               uuid.UUID
	ListID           uuid.UUID
	ItemID           uuid.UUID
	ItemName         string
	NameKey          string
	CategoryID       *uuid.UUID
	CategoryName     *string
	CategoryColor    *string
	CategoryPosition *int
	Quantity         *float64
	Unit             *string
	AmountText       string
	Note             *string
	AddedBy          uuid.UUID
	AddedByName      string
	AddedByColor     string
	CheckedAt        *store.Time
}

func (r entryRow) entry() Entry {
	e := Entry{
		ID: r.ID, ListID: r.ListID, ItemID: r.ItemID, ItemName: r.ItemName,
		Amount:  Amount{Quantity: r.Quantity, Unit: deref(r.Unit), Text: r.AmountText},
		Note:    deref(r.Note),
		AddedBy: r.AddedBy, AddedByName: r.AddedByName, AddedByColor: r.AddedByColor,
		CheckedAt: timePtr(r.CheckedAt),
	}
	if r.CategoryID != nil {
		e.Category = &Category{ID: *r.CategoryID, Name: deref(r.CategoryName), Color: deref(r.CategoryColor)}
		if r.CategoryPosition != nil {
			e.Category.Position = *r.CategoryPosition
		}
	}
	return e
}

// entrySelect selects entryRow columns; callers add WHERE conditions on e.
const entrySelect = `SELECT e.id, e.list_id, e.item_id, i.name AS item_name, i.name_key, i.category_id,
		c.name AS category_name, c.color AS category_color, c.position AS category_position,
		e.quantity, e.unit, e.amount_text, e.note, e.added_by,
		u.name AS added_by_name, u.avatar_color AS added_by_color, e.checked_at
	FROM list_entries e
	JOIN items i ON i.household_id = e.household_id AND i.id = e.item_id
	LEFT JOIN categories c ON c.household_id = i.household_id AND c.id = i.category_id
	JOIN users u ON u.id = e.added_by
	WHERE e.household_id = ?`

// Entry returns an entry that is on a list (checked or not), or
// ErrNotFound.
func (s *Service) Entry(ctx context.Context, sc store.Scoped, id uuid.UUID) (Entry, error) {
	return getEntry(ctx, sc.Read(), sc, id)
}

func getEntry(ctx context.Context, q store.Reader, sc store.Scoped, id uuid.UUID) (Entry, error) {
	var r entryRow
	err := q.Get(ctx, &r, entrySelect+` AND e.id = ? AND e.deleted_at IS NULL
		AND EXISTS (SELECT 1 FROM shopping_lists l WHERE l.household_id = e.household_id AND l.id = e.list_id AND l.deleted_at IS NULL)`,
		sc.HouseholdID(), id)
	if err != nil {
		return Entry{}, notFound(err)
	}
	return r.entry(), nil
}

// AddInput is an item to put on a list.
type AddInput struct {
	ListID uuid.UUID
	// ItemID picks a catalogue item. When nil, Name names the item: an
	// existing one with the same NameKey, or a new one.
	ItemID *uuid.UUID
	Name   string
	Amount Amount
	// AddedBy is the acting member.
	AddedBy uuid.UUID
	// Lang formats merged amounts.
	Lang language.Tag
}

// Added describes the result of Add.
type Added struct {
	Entry Entry
	// Merged is true when the amount was added to an entry already on the
	// list instead of creating a new one.
	Merged bool
}

func checkAmount(errs FieldErrors, field string, a Amount) {
	if utf8.RuneCountInString(a.Text) > maxAmountLen {
		errs[field] = "form.error.tooLong"
	}
}

// Add puts an item on a list. If the item is already on the list and not
// in the cart, amounts in the same unit are added up ("2" + "3" lemons is
// "5"); amounts that cannot be combined make a second entry.
func (s *Service) Add(ctx context.Context, sc store.Scoped, in AddInput) (Added, error) {
	errs := FieldErrors{}
	if in.ItemID == nil {
		in.Name = CleanName(in.Name)
		checkName(errs, "text", in.Name)
	}
	checkAmount(errs, "text", in.Amount)
	if err := errs.orNil(); err != nil {
		return Added{}, err
	}
	var out Added
	err := sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		hh := sc.HouseholdID()
		if err := liveList(ctx, q, sc, in.ListID); err != nil {
			return err
		}
		now := s.clock()
		var itemID uuid.UUID
		if in.ItemID != nil {
			if err := q.Get(ctx, &itemID, `SELECT id FROM items WHERE household_id = ? AND id = ?`, hh, *in.ItemID); err != nil {
				return notFound(err)
			}
		} else {
			key := NameKey(in.Name)
			if _, err := q.Exec(ctx, `INSERT INTO items (id, household_id, name, name_key, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (household_id, name_key) DO NOTHING`,
				store.NewID(), hh, in.Name, key, now, now); err != nil {
				return err
			}
			if err := q.Get(ctx, &itemID, `SELECT id FROM items WHERE household_id = ? AND name_key = ?`, hh, key); err != nil {
				return err
			}
		}

		// Merge into the newest open entry of the item, if the amounts
		// combine.
		var open []struct {
			ID         uuid.UUID
			Quantity   *float64
			Unit       *string
			AmountText string
		}
		if err := q.Select(ctx, &open, `SELECT id, quantity, unit, amount_text FROM list_entries
			WHERE household_id = ? AND list_id = ? AND item_id = ? AND checked_at IS NULL AND deleted_at IS NULL
			ORDER BY created_at DESC LIMIT 1`, hh, in.ListID, itemID); err != nil {
			return err
		}
		id := store.NewID()
		if len(open) == 1 {
			cur := Amount{Quantity: open[0].Quantity, Unit: deref(open[0].Unit), Text: open[0].AmountText}
			if sum, ok := merge(cur, in.Amount, in.Lang); ok {
				id, out.Merged = open[0].ID, true
				if _, err := q.Exec(ctx, `UPDATE list_entries SET quantity = ?, unit = ?, amount_text = ?, updated_at = ?
					WHERE household_id = ? AND id = ?`, sum.Quantity, nullable(sum.Unit), sum.Text, now, hh, id); err != nil {
					return err
				}
			}
		}
		if !out.Merged {
			if _, err := q.Exec(ctx, `INSERT INTO list_entries
				(id, household_id, list_id, item_id, quantity, unit, amount_text, added_by, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				id, hh, in.ListID, itemID, in.Amount.Quantity, nullable(in.Amount.Unit), in.Amount.Text, in.AddedBy, now, now); err != nil {
				return err
			}
		}
		e, err := getEntry(ctx, q.Reader, sc, id)
		out.Entry = e
		return err
	})
	if err != nil {
		return Added{}, err
	}
	s.publish(sc, listChanged(in.ListID))
	return out, nil
}

// liveList checks that a list exists and is not deleted.
func liveList(ctx context.Context, q store.Querier, sc store.Scoped, id uuid.UUID) error {
	var n int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM shopping_lists WHERE household_id = ? AND id = ? AND deleted_at IS NULL`,
		sc.HouseholdID(), id).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Check puts an entry in the cart and counts a purchase of its item. It is
// idempotent. Undo with Uncheck.
func (s *Service) Check(ctx context.Context, sc store.Scoped, id uuid.UUID) (Entry, error) {
	var e Entry
	err := sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		var err error
		if e, err = getEntry(ctx, q.Reader, sc, id); err != nil {
			return err
		}
		if e.Checked() {
			return nil
		}
		now := s.clock()
		hh := sc.HouseholdID()
		if _, err := q.Exec(ctx, `UPDATE list_entries SET checked_at = ?, updated_at = ? WHERE household_id = ? AND id = ?`,
			now, now, hh, id); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, `UPDATE items SET purchase_count = purchase_count + 1, last_purchased_at = ?, updated_at = ?
			WHERE household_id = ? AND id = ?`, now, now, hh, e.ItemID); err != nil {
			return err
		}
		e.CheckedAt = &now.Time
		return nil
	})
	if err != nil {
		return Entry{}, err
	}
	s.publish(sc, listChanged(e.ListID))
	return e, nil
}

// Uncheck takes an entry out of the cart and back onto the list, and takes
// back the purchase Check counted. It is idempotent.
func (s *Service) Uncheck(ctx context.Context, sc store.Scoped, id uuid.UUID) (Entry, error) {
	var e Entry
	err := sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		var err error
		if e, err = getEntry(ctx, q.Reader, sc, id); err != nil {
			return err
		}
		if !e.Checked() {
			return nil
		}
		checkedAt := store.T(*e.CheckedAt)
		now := s.clock()
		hh := sc.HouseholdID()
		if _, err := q.Exec(ctx, `UPDATE list_entries SET checked_at = NULL, updated_at = ? WHERE household_id = ? AND id = ?`,
			now, hh, id); err != nil {
			return err
		}
		// The item's last purchase falls back to its latest other checked
		// entry if this check was the last purchase.
		var last *store.Time
		if err := q.QueryRow(ctx, `SELECT last_purchased_at FROM items WHERE household_id = ? AND id = ?`, hh, e.ItemID).Scan(&last); err != nil {
			return err
		}
		if last != nil && last.Equal(checkedAt.Time) {
			last = nil
			var prev []store.Time
			if err := q.Select(ctx, &prev, `SELECT checked_at FROM list_entries
				WHERE household_id = ? AND item_id = ? AND checked_at IS NOT NULL ORDER BY checked_at DESC LIMIT 1`,
				hh, e.ItemID); err != nil {
				return err
			}
			if len(prev) == 1 {
				last = &prev[0]
			}
		}
		if _, err := q.Exec(ctx, `UPDATE items SET
				purchase_count = CASE WHEN purchase_count > 0 THEN purchase_count - 1 ELSE 0 END,
				last_purchased_at = ?, updated_at = ?
			WHERE household_id = ? AND id = ?`, last, now, hh, e.ItemID); err != nil {
			return err
		}
		e.CheckedAt = nil
		return nil
	})
	if err != nil {
		return Entry{}, err
	}
	s.publish(sc, listChanged(e.ListID))
	return e, nil
}

// Remove takes an entry off its list. Undo with Restore within UndoWindow.
func (s *Service) Remove(ctx context.Context, sc store.Scoped, id uuid.UUID) (Entry, error) {
	var e Entry
	err := sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		var err error
		if e, err = getEntry(ctx, q.Reader, sc, id); err != nil {
			return err
		}
		now := s.clock()
		_, err = q.Exec(ctx, `UPDATE list_entries SET deleted_at = ?, updated_at = ? WHERE household_id = ? AND id = ?`,
			now, now, sc.HouseholdID(), id)
		return err
	})
	if err != nil {
		return Entry{}, err
	}
	s.publish(sc, listChanged(e.ListID))
	return e, nil
}

// Restore undoes Remove. It returns ErrNotFound when the entry was not
// removed, was swept, or its list is deleted.
func (s *Service) Restore(ctx context.Context, sc store.Scoped, id uuid.UUID) (Entry, error) {
	var e Entry
	err := sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		res, err := q.Exec(ctx, `UPDATE list_entries SET deleted_at = NULL, updated_at = ?
			WHERE household_id = ? AND id = ? AND deleted_at IS NOT NULL
			AND EXISTS (SELECT 1 FROM shopping_lists l
			            WHERE l.household_id = list_entries.household_id AND l.id = list_entries.list_id AND l.deleted_at IS NULL)`,
			s.clock(), sc.HouseholdID(), id)
		if err != nil {
			return err
		}
		if err := mustAffect(res); err != nil {
			return err
		}
		e, err = getEntry(ctx, q.Reader, sc, id)
		return err
	})
	if err != nil {
		return Entry{}, err
	}
	s.publish(sc, listChanged(e.ListID))
	return e, nil
}

// Cleared describes a cleared cart, for Undo.
type Cleared struct {
	// At marks the entries cleared together; RestoreCart takes it back.
	At time.Time
	// Count is how many entries were cleared.
	Count int
}

// ClearCart removes every checked entry of a list. Undo with RestoreCart.
func (s *Service) ClearCart(ctx context.Context, sc store.Scoped, listID uuid.UUID) (Cleared, error) {
	var out Cleared
	err := sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		if err := liveList(ctx, q, sc, listID); err != nil {
			return err
		}
		now := s.clock()
		res, err := q.Exec(ctx, `UPDATE list_entries SET deleted_at = ?, updated_at = ?
			WHERE household_id = ? AND list_id = ? AND checked_at IS NOT NULL AND deleted_at IS NULL`,
			now, now, sc.HouseholdID(), listID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		out = Cleared{At: now.Time, Count: int(n)}
		return err
	})
	if err != nil {
		return Cleared{}, err
	}
	if out.Count > 0 {
		s.publish(sc, listChanged(listID))
	}
	return out, nil
}

// RestoreCart undoes ClearCart: the entries cleared at at go back into the
// cart. It returns how many came back.
func (s *Service) RestoreCart(ctx context.Context, sc store.Scoped, listID uuid.UUID, at time.Time) (int, error) {
	var n int64
	err := sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		if err := liveList(ctx, q, sc, listID); err != nil {
			return err
		}
		res, err := q.Exec(ctx, `UPDATE list_entries SET deleted_at = NULL, updated_at = ?
			WHERE household_id = ? AND list_id = ? AND checked_at IS NOT NULL AND deleted_at = ?`,
			s.clock(), sc.HouseholdID(), listID, store.T(at))
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return 0, err
	}
	if n > 0 {
		s.publish(sc, listChanged(listID))
	}
	return int(n), nil
}

// EntryUpdate is the edit sheet: the entry's amount, note and list, and
// its item's name and category, which apply wherever the item is used.
type EntryUpdate struct {
	Name   string
	Amount Amount
	Note   string
	// CategoryID is the item's category; nil means uncategorised.
	CategoryID *uuid.UUID
	// NewCategory, when its name is set, creates a category (or picks the
	// one with that name) and uses it instead of CategoryID. An empty colour
	// picks the next one from Colors.
	NewCategory NewCategory
	ListID      uuid.UUID
}

// UpdateEntry saves the edit sheet.
func (s *Service) UpdateEntry(ctx context.Context, sc store.Scoped, id uuid.UUID, u EntryUpdate) error {
	u.Name = normalizeSpace(u.Name)
	u.Note = normalizeSpace(u.Note)
	u.NewCategory.Name = normalizeSpace(u.NewCategory.Name)
	errs := FieldErrors{}
	checkName(errs, "name", u.Name)
	checkAmount(errs, "amount", u.Amount)
	if utf8.RuneCountInString(u.Note) > maxNoteLen {
		errs["note"] = "form.error.tooLong"
	}
	if u.NewCategory.Name != "" {
		if utf8.RuneCountInString(u.NewCategory.Name) > maxNameLen {
			errs["new_category"] = "form.error.tooLong"
		}
		if u.NewCategory.Color != "" {
			checkColor(errs, "new_category", u.NewCategory.Color)
		}
	}
	if err := errs.orNil(); err != nil {
		return err
	}

	var before Entry
	catalog := false
	err := sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		var err error
		if before, err = getEntry(ctx, q.Reader, sc, id); err != nil {
			return err
		}
		if u.ListID != before.ListID {
			if err := liveList(ctx, q, sc, u.ListID); err != nil {
				return err
			}
		}
		hh := sc.HouseholdID()
		now := s.clock()

		categoryID := u.CategoryID
		if u.NewCategory.Name != "" {
			if err := sc.LockHousehold(ctx, q); err != nil {
				return err
			}
			var existing []uuid.UUID
			if err := q.Select(ctx, &existing, `SELECT id FROM categories WHERE household_id = ? AND name_key = ?`,
				hh, NameKey(u.NewCategory.Name)); err != nil {
				return err
			}
			if len(existing) == 1 {
				categoryID = &existing[0]
			} else {
				if u.NewCategory.Color == "" {
					// Cycle through the palette so neighbours differ.
					var n int
					if err := q.QueryRow(ctx, `SELECT count(*) FROM categories WHERE household_id = ?`, hh).Scan(&n); err != nil {
						return err
					}
					u.NewCategory.Color = Colors[n%len(Colors)]
				}
				c, err := s.insertCategory(ctx, q, sc, u.NewCategory)
				if err != nil {
					return err
				}
				categoryID = &c.ID
			}
		} else if categoryID != nil {
			var n int
			if err := q.QueryRow(ctx, `SELECT count(*) FROM categories WHERE household_id = ? AND id = ?`, hh, *categoryID).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return FieldErrors{"category": "form.error.choice"}
			}
		}

		key := NameKey(u.Name)
		var clash int
		if err := q.QueryRow(ctx, `SELECT count(*) FROM items WHERE household_id = ? AND name_key = ? AND id <> ?`,
			hh, key, before.ItemID).Scan(&clash); err != nil {
			return err
		}
		if clash > 0 {
			return FieldErrors{"name": "groceries.error.itemExists"}
		}
		oldCategory := uuid.Nil
		if before.Category != nil {
			oldCategory = before.Category.ID
		}
		newCategory := uuid.Nil
		if categoryID != nil {
			newCategory = *categoryID
		}
		if u.Name != before.ItemName || newCategory != oldCategory {
			catalog = true
			if _, err := q.Exec(ctx, `UPDATE items SET name = ?, name_key = ?, category_id = ?, updated_at = ?
				WHERE household_id = ? AND id = ?`, u.Name, key, categoryID, now, hh, before.ItemID); err != nil {
				return err
			}
		}
		_, err = q.Exec(ctx, `UPDATE list_entries SET list_id = ?, quantity = ?, unit = ?, amount_text = ?, note = ?, updated_at = ?
			WHERE household_id = ? AND id = ?`,
			u.ListID, u.Amount.Quantity, nullable(u.Amount.Unit), u.Amount.Text, nullable(u.Note), now, hh, id)
		return err
	})
	if err != nil {
		return err
	}
	events := []live.Event{listChanged(before.ListID)}
	if u.ListID != before.ListID {
		events = append(events, listChanged(u.ListID), listsChanged)
	}
	if catalog {
		events = append(events, catalogChanged)
	}
	s.publish(sc, events...)
	return nil
}
