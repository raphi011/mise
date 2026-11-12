package groceries

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
	"golang.org/x/text/language"

	"github.com/raphi011/mise/internal/catalog"
	"github.com/raphi011/mise/internal/store"
)

// IconNone is the stored icon of an item that deliberately shows none,
// instead of inheriting its catalogue entry's. NULL inherits.
const IconNone = "none"

// ShowIcon resolves the icon to show for an item: its own pick, else the
// catalogue entry's, else "".
func ShowIcon(icon, catalogKey *string) string {
	switch {
	case icon != nil && *icon == IconNone:
		return ""
	case icon != nil && catalog.HasIcon(*icon):
		return *icon
	case catalogKey != nil:
		return catalog.Icon(*catalogKey)
	}
	return ""
}

// ItemIcon returns the icon an item overrides its catalogue's with: "" for
// none (inherits), IconNone, or an icon key.
func (s *Service) ItemIcon(ctx context.Context, sc store.Scoped, itemID uuid.UUID) (string, error) {
	var icon *string
	err := sc.Read().Get(ctx, &icon, `SELECT icon FROM items WHERE household_id = ? AND id = ?`, sc.HouseholdID(), itemID)
	return deref(icon), notFound(err)
}

// householdLanguage is the language the household's catalogue names are in.
func householdLanguage(ctx context.Context, q store.Reader, sc store.Scoped) (string, error) {
	var lang string
	err := q.Get(ctx, &lang, `SELECT default_language FROM households WHERE id = ?`, sc.HouseholdID())
	return lang, err
}

// catalogCategory returns the id of the household category that stands for
// the catalogue category key, or nil when it has none (deleted, or a
// household from before the catalogue).
func catalogCategory(ctx context.Context, q store.Reader, sc store.Scoped, key string) (*uuid.UUID, error) {
	var id uuid.UUID
	err := q.Get(ctx, &id, `SELECT id FROM categories WHERE household_id = ? AND catalog_key = ?`, sc.HouseholdID(), key)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, nil
	case err != nil:
		return nil, err
	}
	return &id, nil
}

// keyTaken reports whether an item of the household is linked to key.
func keyTaken(ctx context.Context, q store.Reader, sc store.Scoped, key string) (bool, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM items WHERE household_id = ? AND catalog_key = ?`, sc.HouseholdID(), key).Scan(&n)
	return n > 0, err
}

// EnsureCatalogItem returns the household's item for a catalogue entry,
// creating it if there is none: named in the household's language, filed
// under the household category that stands for the entry's default
// category. An item with the entry's name that is not linked yet is linked
// instead of duplicated. created reports a new item; the caller publishes
// live.CatalogChanged after commit. An unknown key is ErrNotFound.
func EnsureCatalogItem(ctx context.Context, q store.Querier, sc store.Scoped, key string, now store.Time) (id uuid.UUID, created bool, err error) {
	hh := sc.HouseholdID()
	err = q.Get(ctx, &id, `SELECT id FROM items WHERE household_id = ? AND catalog_key = ?`, hh, key)
	if err == nil {
		return id, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, false, err
	}
	lang, err := householdLanguage(ctx, q.Reader, sc)
	if err != nil {
		return uuid.Nil, false, err
	}
	e, ok := catalog.For(lang).Entry(key)
	if !ok {
		return uuid.Nil, false, ErrNotFound
	}
	nameKey := NameKey(e.Name)
	err = q.Get(ctx, &id, `SELECT id FROM items WHERE household_id = ? AND name_key = ?`, hh, nameKey)
	if err == nil {
		// Same name, not linked yet (typed by hand before): link it. The
		// partial unique index makes a second link to the key impossible.
		_, err = q.Exec(ctx, `UPDATE items SET catalog_key = ?, updated_at = ?
			WHERE household_id = ? AND id = ? AND catalog_key IS NULL`, e.Key, now, hh, id)
		return id, false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, false, err
	}
	category, err := catalogCategory(ctx, q.Reader, sc, e.Category)
	if err != nil {
		return uuid.Nil, false, err
	}
	id = store.NewID()
	_, err = q.Exec(ctx, `INSERT INTO items (id, household_id, name, name_key, category_id, catalog_key, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, id, hh, e.Name, nameKey, category, e.Key, now, now)
	return id, err == nil, err
}

// linkNew fills in the catalogue link and default category of an item that
// is about to be created from a typed or imported name: when the name is
// an entry's name or alias in the household's language and no item has the
// key yet. category is the category the caller already chose.
func linkNew(ctx context.Context, q store.Querier, sc store.Scoped, name string, category *uuid.UUID) (*string, *uuid.UUID, error) {
	lang, err := householdLanguage(ctx, q.Reader, sc)
	if err != nil {
		return nil, nil, err
	}
	e, ok := catalog.For(lang).Lookup(name)
	if !ok {
		return nil, category, nil
	}
	if taken, err := keyTaken(ctx, q.Reader, sc, e.Key); err != nil || taken {
		return nil, category, err
	}
	if category == nil {
		if category, err = catalogCategory(ctx, q.Reader, sc, e.Category); err != nil {
			return nil, nil, err
		}
	}
	return &e.Key, category, nil
}

// CatalogHit is a catalogue entry offered for an ingredient or a typed
// name, in the household's language.
type CatalogHit struct {
	Key  string
	Name string
	// Icon is the entry's icon key, "" for none.
	Icon string
	// Category is the household category the entry would be filed under,
	// nil when it has none.
	Category *Category
}

// hit builds a CatalogHit for e in the household's language.
func hit(lang string, e catalog.Entry, cats map[string]Category) CatalogHit {
	h := CatalogHit{Key: e.Key, Name: e.Name, Icon: e.Icon}
	if own, ok := catalog.For(lang).Entry(e.Key); ok {
		h.Name = own.Name
	}
	if c, ok := cats[e.Category]; ok {
		h.Category = &c
	}
	return h
}

// catalogCategories maps catalogue category keys to the household's
// categories that carry them.
func catalogCategories(ctx context.Context, q store.Reader, sc store.Scoped) (map[string]Category, error) {
	var rows []Category
	if err := q.Select(ctx, &rows, `SELECT id, name, color, position, catalog_key FROM categories
		WHERE household_id = ? AND catalog_key IS NOT NULL`, sc.HouseholdID()); err != nil {
		return nil, err
	}
	out := map[string]Category{}
	for _, c := range rows {
		out[c.CatalogKey] = c
	}
	return out, nil
}

// LookupCatalog finds the catalogue entry named name (exact name or alias),
// first in recipeLang's catalogue, then in the household's. The hit carries
// the household's name for it, so an English recipe in a German household
// proposes "Zitrone". ok is false when nothing matches. Only whole names
// match: "firm tofu" is not "tofu", which the importer leaves to the user.
func (s *Service) LookupCatalog(ctx context.Context, sc store.Scoped, name string, recipeLang language.Tag) (CatalogHit, bool, error) {
	lang, err := householdLanguage(ctx, sc.Read(), sc)
	if err != nil {
		return CatalogHit{}, false, err
	}
	for _, l := range []string{recipeLang.String(), lang} {
		found, ok := catalog.For(l).Lookup(name)
		if !ok {
			continue
		}
		cats, err := catalogCategories(ctx, sc.Read(), sc)
		if err != nil {
			return CatalogHit{}, false, err
		}
		return hit(lang, found, cats), true, nil
	}
	return CatalogHit{}, false, nil
}

// LinkCatalog links a household created before the catalogue to it:
// categories whose name is the translation of a seeded one (d lists them
// with their keys), and items named like an entry. It never changes
// names, categories or icons and skips keys that are taken. It is
// idempotent; the server runs it at startup.
func (s *Service) LinkCatalog(ctx context.Context, sc store.Scoped, d Defaults) error {
	changed := false
	err := sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		if err := sc.LockHousehold(ctx, q); err != nil {
			return err
		}
		hh := sc.HouseholdID()
		now := s.clock()
		for _, c := range d.Categories {
			if c.CatalogKey == "" {
				continue
			}
			res, err := q.Exec(ctx, `UPDATE categories SET catalog_key = ?, updated_at = ?
				WHERE household_id = ? AND name_key = ? AND catalog_key IS NULL
				AND NOT EXISTS (SELECT 1 FROM categories o WHERE o.household_id = ? AND o.catalog_key = ?)`,
				c.CatalogKey, now, hh, NameKey(c.Name), hh, c.CatalogKey)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				changed = true
			}
		}
		lang, err := householdLanguage(ctx, q.Reader, sc)
		if err != nil {
			return err
		}
		var unlinked []struct {
			ID      uuid.UUID
			NameKey string
		}
		if err := q.Select(ctx, &unlinked, `SELECT id, name_key FROM items WHERE household_id = ? AND catalog_key IS NULL`, hh); err != nil {
			return err
		}
		c := catalog.For(lang)
		for _, it := range unlinked {
			e, ok := c.Lookup(it.NameKey)
			if !ok {
				continue
			}
			res, err := q.Exec(ctx, `UPDATE items SET catalog_key = ?, updated_at = ?
				WHERE household_id = ? AND id = ? AND catalog_key IS NULL
				AND NOT EXISTS (SELECT 1 FROM items o WHERE o.household_id = ? AND o.catalog_key = ?)`,
				e.Key, now, hh, it.ID, hh, e.Key)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				changed = true
			}
		}
		return nil
	})
	if err == nil && changed {
		s.publish(sc, catalogChanged)
	}
	return err
}

// suggestCatalog returns catalogue entries matching what was typed that
// the household has no item for, as suggestions after its own items.
func (s *Service) suggestCatalog(ctx context.Context, sc store.Scoped, typed string, limit int) ([]Suggestion, error) {
	if limit <= 0 {
		return nil, nil
	}
	lang, err := householdLanguage(ctx, sc.Read(), sc)
	if err != nil {
		return nil, err
	}
	var keys []string
	if err := sc.Read().Select(ctx, &keys, `SELECT catalog_key FROM items WHERE household_id = ? AND catalog_key IS NOT NULL`, sc.HouseholdID()); err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, k := range keys {
		have[k] = true
	}
	var names []string
	if err := sc.Read().Select(ctx, &names, `SELECT name_key FROM items WHERE household_id = ? AND name_key LIKE ? ESCAPE '\'`,
		sc.HouseholdID(), "%"+escapeLike(NameKey(typed))+"%"); err != nil {
		return nil, err
	}
	haveName := map[string]bool{}
	for _, n := range names {
		haveName[n] = true
	}
	cats, err := catalogCategories(ctx, sc.Read(), sc)
	if err != nil {
		return nil, err
	}
	// Mid-word matches ("lem" in "wholemeal") are noise for entries the
	// household has never used; its own items still match anywhere.
	all := catalog.For(lang).Search(typed, 50, func(key string) bool {
		if have[key] {
			return true
		}
		e, _ := catalog.For(lang).Entry(key)
		return haveName[NameKey(e.Name)]
	})
	out := make([]Suggestion, 0, limit)
	for _, h := range all {
		if h.Match > catalog.MatchWord || len(out) == limit {
			continue
		}
		ch := hit(lang, h.Entry, cats)
		out = append(out, Suggestion{Match: Match(h.Match), Catalog: &ch})
	}
	return out, nil
}
