package groceries

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/store"
)

// View is a list as the list page shows it.
type View struct {
	List List
	// Groups are the entries still to get, by category in aisle order;
	// uncategorised entries come last (Category nil).
	Groups []Group
	// Cart holds the checked entries, most recent first.
	Cart []Entry
	// Usual are frequently bought items that are not on the list.
	Usual []Item
}

// Group is one category's entries, by item name.
type Group struct {
	Category *Category
	Entries  []Entry
}

// Item is a catalogue item.
type Item struct {
	ID              uuid.UUID
	Name            string
	Category        *Category
	PurchaseCount   int
	LastPurchasedAt *time.Time
	// CatalogKey links the item to the default-ingredient catalogue ("" for
	// none).
	CatalogKey string
	// IconOverride is the icon the household picked: "" inherits the
	// catalogue's, IconNone shows no icon.
	IconOverride string
	// Icon is the icon to show: the override, else the catalogue's, else ""
	// for the initial tile.
	Icon string
}

// usualLimit caps the "Usually bought" chips.
const usualLimit = 8

// ListView loads everything the list page shows.
func (s *Service) ListView(ctx context.Context, sc store.Scoped, listID uuid.UUID) (View, error) {
	l, err := s.List(ctx, sc, listID)
	if err != nil {
		return View{}, err
	}
	v := View{List: l}
	var rows []entryRow
	if err := sc.Read().Select(ctx, &rows, entrySelect+` AND e.list_id = ? AND e.deleted_at IS NULL`,
		sc.HouseholdID(), listID); err != nil {
		return View{}, err
	}
	var open []entryRow
	var cart []entryRow
	for _, r := range rows {
		if r.CheckedAt != nil {
			cart = append(cart, r)
		} else {
			open = append(open, r)
		}
	}
	slices.SortStableFunc(open, func(a, b entryRow) int {
		return cmp.Or(compareCategory(a, b), strings.Compare(a.NameKey, b.NameKey), strings.Compare(a.ID.String(), b.ID.String()))
	})
	for _, r := range open {
		e := r.entry()
		n := len(v.Groups)
		if n == 0 || !sameCategory(v.Groups[n-1].Category, e.Category) {
			v.Groups = append(v.Groups, Group{Category: e.Category})
			n++
		}
		v.Groups[n-1].Entries = append(v.Groups[n-1].Entries, e)
	}
	slices.SortStableFunc(cart, func(a, b entryRow) int { return b.CheckedAt.Compare(a.CheckedAt.Time) })
	for _, r := range cart {
		v.Cart = append(v.Cart, r.entry())
	}

	v.Usual, err = s.usual(ctx, sc, listID)
	return v, err
}

// compareCategory orders by aisle position, uncategorised last.
func compareCategory(a, b entryRow) int {
	switch {
	case a.CategoryID == nil && b.CategoryID == nil:
		return 0
	case a.CategoryID == nil:
		return 1
	case b.CategoryID == nil:
		return -1
	}
	return cmp.Or(cmp.Compare(*a.CategoryPosition, *b.CategoryPosition), strings.Compare(a.CategoryID.String(), b.CategoryID.String()))
}

func sameCategory(a, b *Category) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.ID == b.ID
}

type itemRow struct {
	ID              uuid.UUID
	Name            string
	NameKey         string
	CategoryID      *uuid.UUID
	CategoryName    *string
	CategoryColor   *string
	PurchaseCount   int
	LastPurchasedAt *store.Time
	CatalogKey      *string
	Icon            *string
}

func (r itemRow) item() Item {
	it := Item{ID: r.ID, Name: r.Name, PurchaseCount: r.PurchaseCount, LastPurchasedAt: timePtr(r.LastPurchasedAt),
		CatalogKey: deref(r.CatalogKey), IconOverride: deref(r.Icon), Icon: ShowIcon(r.Icon, r.CatalogKey)}
	if r.CategoryID != nil {
		it.Category = &Category{ID: *r.CategoryID, Name: deref(r.CategoryName), Color: deref(r.CategoryColor)}
	}
	return it
}

const itemSelect = `SELECT i.id, i.name, i.name_key, i.category_id, c.name AS category_name, c.color AS category_color,
		i.purchase_count, i.last_purchased_at, i.catalog_key, i.icon
	FROM items i LEFT JOIN categories c ON c.household_id = i.household_id AND c.id = i.category_id
	WHERE i.household_id = ?`

// usual returns the most bought items that are not on the list, checked or
// not. Cleared items come back here.
func (s *Service) usual(ctx context.Context, sc store.Scoped, listID uuid.UUID) ([]Item, error) {
	var rows []itemRow
	err := sc.Read().Select(ctx, &rows, itemSelect+` AND i.purchase_count > 0
		AND NOT EXISTS (SELECT 1 FROM list_entries e
		                WHERE e.household_id = i.household_id AND e.item_id = i.id AND e.list_id = ? AND e.deleted_at IS NULL)
		ORDER BY i.purchase_count DESC, i.last_purchased_at DESC NULLS LAST, i.name_key LIMIT ?`,
		sc.HouseholdID(), listID, usualLimit)
	if err != nil {
		return nil, err
	}
	out := make([]Item, len(rows))
	for i, r := range rows {
		out[i] = r.item()
	}
	return out, nil
}

// Match says how a suggestion matches what was typed, best first.
type Match int

// Matches.
const (
	MatchExact Match = iota
	MatchPrefix
	MatchWord
	MatchContains
)

// Suggestion is a catalogue item matching what is being typed.
type Suggestion struct {
	Item  Item
	Match Match
	// Catalog is set instead of Item for a catalogue entry the household
	// has no item for yet; picking it creates the item.
	Catalog *CatalogHit
	// OnList is the amount of the item's open entry on the list, if any.
	OnList *Amount
}

// suggestCandidates bounds the rows ranked per keystroke.
const suggestCandidates = 200

// Suggest ranks catalogue items whose name contains the name typed (see
// SearchItems) and adds each one's open entry on the list.
func (s *Service) Suggest(ctx context.Context, sc store.Scoped, listID uuid.UUID, name string, limit int) ([]Suggestion, error) {
	out, err := s.SearchItems(ctx, sc, name, limit)
	if err != nil {
		return nil, err
	}
	// The household's own items come first; catalogue entries fill what is
	// left of the limit.
	cat, err := s.suggestCatalog(ctx, sc, name, limit-len(out))
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return cat, nil
	}

	var open []struct {
		ItemID     uuid.UUID
		Quantity   *float64
		Unit       *string
		AmountText string
	}
	if err := sc.Read().Select(ctx, &open, `SELECT item_id, quantity, unit, amount_text FROM list_entries
		WHERE household_id = ? AND list_id = ? AND checked_at IS NULL AND deleted_at IS NULL`,
		sc.HouseholdID(), listID); err != nil {
		return nil, err
	}
	for i := range out {
		for _, o := range open {
			if o.ItemID == out[i].Item.ID {
				out[i].OnList = &Amount{Quantity: o.Quantity, Unit: deref(o.Unit), Text: o.AmountText}
				break
			}
		}
	}
	return append(out, cat...), nil
}

// SearchItems ranks catalogue items whose name contains name: exact
// matches first, then names starting with it, then names with a word
// starting with it, then the rest; within each, the most bought and most
// recently bought first. OnList is not set.
func (s *Service) SearchItems(ctx context.Context, sc store.Scoped, name string, limit int) ([]Suggestion, error) {
	key := NameKey(name)
	if key == "" {
		return nil, nil
	}
	var rows []itemRow
	err := sc.Read().Select(ctx, &rows, itemSelect+` AND i.name_key LIKE ? ESCAPE '\'
		ORDER BY i.purchase_count DESC, i.name_key LIMIT ?`,
		sc.HouseholdID(), "%"+escapeLike(key)+"%", suggestCandidates)
	if err != nil {
		return nil, err
	}
	out := make([]Suggestion, len(rows))
	for i, r := range rows {
		m := MatchContains
		switch {
		case r.NameKey == key:
			m = MatchExact
		case strings.HasPrefix(r.NameKey, key):
			m = MatchPrefix
		case strings.Contains(r.NameKey, " "+key):
			m = MatchWord
		}
		out[i] = Suggestion{Item: r.item(), Match: m}
	}
	slices.SortStableFunc(out, func(a, b Suggestion) int {
		return cmp.Or(
			cmp.Compare(a.Match, b.Match),
			cmp.Compare(b.Item.PurchaseCount, a.Item.PurchaseCount),
			compareTimes(b.Item.LastPurchasedAt, a.Item.LastPurchasedAt),
			strings.Compare(NameKey(a.Item.Name), NameKey(b.Item.Name)),
		)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ItemsByID returns the household's items among ids, by ID. IDs of other
// households' items are left out.
func (s *Service) ItemsByID(ctx context.Context, sc store.Scoped, ids []uuid.UUID) (map[uuid.UUID]Item, error) {
	out := map[uuid.UUID]Item{}
	if len(ids) == 0 {
		return out, nil
	}
	args := []any{sc.HouseholdID()}
	for _, id := range ids {
		args = append(args, id)
	}
	var rows []itemRow
	err := sc.Read().Select(ctx, &rows, itemSelect+` AND i.id IN (?`+strings.Repeat(", ?", len(ids)-1)+`)`, args...)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ID] = r.item()
	}
	return out, nil
}

// compareTimes orders nil before any time.
func compareTimes(a, b *time.Time) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return -1
	case b == nil:
		return 1
	}
	return a.Compare(*b)
}

// escapeLike escapes LIKE wildcards for use with ESCAPE '\'.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// ItemsWithin returns the catalogue items whose name appears as whole
// words in text ("Tofu" in "extra-firm tofu"), longest name first, for
// matching an imported ingredient to an item. Hyphens count as word
// breaks.
func (s *Service) ItemsWithin(ctx context.Context, sc store.Scoped, text string, limit int) ([]Item, error) {
	key := wordKey(NameKey(text))
	if key == "" {
		return nil, nil
	}
	var rows []itemRow
	// The concatenation is portable; LIKE wildcards in names only widen
	// the candidates, which the word check below narrows again.
	err := sc.Read().Select(ctx, &rows, itemSelect+` AND ? LIKE '%' || i.name_key || '%' LIMIT ?`,
		sc.HouseholdID(), NameKey(text), suggestCandidates)
	if err != nil {
		return nil, err
	}
	var out []Item
	padded := " " + key + " "
	for _, r := range rows {
		if k := wordKey(r.NameKey); k != "" && strings.Contains(padded, " "+k+" ") {
			out = append(out, r.item())
		}
	}
	slices.SortStableFunc(out, func(a, b Item) int {
		return cmp.Or(
			cmp.Compare(len(NameKey(b.Name)), len(NameKey(a.Name))),
			cmp.Compare(b.PurchaseCount, a.PurchaseCount),
			strings.Compare(NameKey(a.Name), NameKey(b.Name)),
		)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// wordKey turns punctuation into spaces so words can be compared.
func wordKey(key string) string {
	return strings.Join(strings.FieldsFunc(key, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}), " ")
}
