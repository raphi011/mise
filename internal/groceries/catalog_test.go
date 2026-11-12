package groceries

import (
	"context"
	"errors"
	"testing"

	"golang.org/x/text/language"

	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

// catalogDefaults is testDefaults with the catalogue keys setup gives the
// categories.
var catalogDefaults = Defaults{
	ListName: "Shopping list",
	Categories: []NewCategory{
		{Name: "Produce", Color: "herb", CatalogKey: "produce"},
		{Name: "Fridge", Color: "sky", CatalogKey: "fridge"},
		{Name: "Pantry", Color: "amber", CatalogKey: "pantry"},
	},
}

func TestCatalogSuggestAndAdd(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		e := newEnv(t, db, "home")
		l, err := e.s.Start(ctx, e.sc, catalogDefaults)
		if err != nil {
			t.Fatal(err)
		}

		// A new household has no items, yet "lem" suggests catalogue entries
		// with their default category.
		got, err := e.s.Suggest(ctx, e.sc, l.ID, "lem", 5)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) < 1 || got[0].Catalog == nil || got[0].Catalog.Key != "lemon" || got[0].Catalog.Name != "Lemon" ||
			got[0].Catalog.Icon != "lemon" || got[0].Catalog.Category == nil || got[0].Catalog.Category.Name != "Produce" {
			t.Fatalf("suggestions %+v", got)
		}

		for _, g := range got {
			if g.Catalog != nil && g.Catalog.Key == "whole_wheat_flour" {
				t.Errorf("mid-word match suggested: %+v", g.Catalog)
			}
		}

		// Picking one creates the item: linked, filed, with the icon.
		added, err := e.s.Add(ctx, e.sc, AddInput{ListID: l.ID, CatalogKey: "lemon", AddedBy: e.hh.Admin, Lang: language.English})
		if err != nil {
			t.Fatal(err)
		}
		if added.Entry.ItemName != "Lemon" || added.Entry.ItemIcon != "lemon" || added.Entry.Category == nil || added.Entry.Category.Name != "Produce" {
			t.Errorf("entry %+v", added.Entry)
		}
		// Picking it again reuses the item.
		again, err := e.s.Add(ctx, e.sc, AddInput{ListID: l.ID, CatalogKey: "lemon", AddedBy: e.hh.Admin, Lang: language.English})
		if err != nil || again.Entry.ItemID != added.Entry.ItemID {
			t.Errorf("second add: %+v %v", again.Entry, err)
		}

		// The entry is no longer suggested from the catalogue; the item is.
		got, err = e.s.Suggest(ctx, e.sc, l.ID, "lemon", 5)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) == 0 || got[0].Catalog != nil || got[0].Item.Name != "Lemon" || got[0].Item.Icon != "lemon" {
			t.Fatalf("suggestions after add %+v", got)
		}
		for _, s := range got {
			if s.Catalog != nil && s.Catalog.Key == "lemon" {
				t.Error("catalogue entry suggested although the household has the item")
			}
		}

		if _, err := e.s.Add(ctx, e.sc, AddInput{ListID: l.ID, CatalogKey: "no_such_key", AddedBy: e.hh.Admin, Lang: language.English}); !errors.Is(err, ErrNotFound) {
			t.Errorf("unknown key: %v", err)
		}
	})
}

func TestTypedNameLinksToCatalogue(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		e := newEnv(t, db, "home")
		l, err := e.s.Start(ctx, e.sc, catalogDefaults)
		if err != nil {
			t.Fatal(err)
		}
		// An alias ("lemons") links to the entry and files the new item.
		a := e.add(t, l.ID, "2 lemons").Entry
		if a.ItemIcon != "lemon" || a.Category == nil || a.Category.Name != "Produce" {
			t.Errorf("typed alias: %+v", a)
		}
		// Unknown names stay free text: no icon, no category.
		b := e.add(t, l.ID, "kumquats").Entry
		if b.ItemIcon != "" || b.Category != nil {
			t.Errorf("free text: %+v", b)
		}
		// "Lemon" now resolves to the item that holds the key.
		c := e.add(t, l.ID, "lemon").Entry
		if c.ItemID == a.ItemID || c.ItemIcon != "" {
			t.Errorf("a second item must not take over a linked key: %+v", c)
		}
	})
}

func TestCatalogueItemRenameAndIconOverride(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		e := newEnv(t, db, "home")
		l, err := e.s.Start(ctx, e.sc, catalogDefaults)
		if err != nil {
			t.Fatal(err)
		}
		en := e.add(t, l.ID, "lemons").Entry
		update := func(name, icon string) error {
			return e.s.UpdateEntry(ctx, e.sc, en.ID, EntryUpdate{Name: name, ListID: l.ID, CategoryID: &en.Category.ID, Icon: icon})
		}

		// A name that still names the entry keeps the link, so the icon
		// stays; another name drops it (TestRenameRelinksCatalogue).
		for _, tc := range []struct{ name, want string }{{"Lemon", "lemon"}, {"Citrus", ""}, {"Lemons", "lemon"}} {
			if err := update(tc.name, ""); err != nil {
				t.Fatal(err)
			}
			got, _ := e.s.Entry(ctx, e.sc, en.ID)
			if got.ItemName != tc.name || got.ItemIcon != tc.want {
				t.Errorf("after rename to %q: icon %q, want %q", tc.name, got.ItemIcon, tc.want)
			}
		}
		// Own pick, then none, then back to the catalogue's.
		for _, tc := range []struct{ icon, want string }{{"onion", "onion"}, {IconNone, ""}, {"", "lemon"}} {
			if err := update("Lemons", tc.icon); err != nil {
				t.Fatal(err)
			}
			got, _ := e.s.Entry(ctx, e.sc, en.ID)
			if got.ItemIcon != tc.want {
				t.Errorf("icon %q shows %q, want %q", tc.icon, got.ItemIcon, tc.want)
			}
		}
		var fe FieldErrors
		if err := update("Lemons", "no-such-icon"); !errors.As(err, &fe) || fe["icon"] == "" {
			t.Errorf("unknown icon: %v", err)
		}
	})
}

func TestCatalogueItemWithoutCategory(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		e := newEnv(t, db, "home")
		l, err := e.s.Start(ctx, e.sc, catalogDefaults)
		if err != nil {
			t.Fatal(err)
		}
		cats, _ := e.s.Categories(ctx, e.sc)
		for _, c := range cats {
			if c.CatalogKey == "produce" {
				if err := e.s.DeleteCategory(ctx, e.sc, c.ID); err != nil {
					t.Fatal(err)
				}
			}
		}
		// The household deleted Produce: the item is uncategorised, not an
		// error and not a recreated category.
		added, err := e.s.Add(ctx, e.sc, AddInput{ListID: l.ID, CatalogKey: "lemon", AddedBy: e.hh.Admin, Lang: language.English})
		if err != nil || added.Entry.Category != nil {
			t.Errorf("%+v %v", added.Entry, err)
		}
		// Meat is a catalogue category this household never had.
		added, err = e.s.Add(ctx, e.sc, AddInput{ListID: l.ID, CatalogKey: "bacon", AddedBy: e.hh.Admin, Lang: language.English})
		if err != nil || added.Entry.Category != nil {
			t.Errorf("%+v %v", added.Entry, err)
		}
	})
}

func TestCatalogueLinksExistingItemWithSameName(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		e := newEnv(t, db, "home")
		l, err := e.s.Start(ctx, e.sc, testDefaults) // no catalogue keys: an older household
		if err != nil {
			t.Fatal(err)
		}
		typed := e.add(t, l.ID, "Lemon").Entry
		if typed.ItemIcon != "lemon" {
			t.Fatalf("typed name links even without categories: %+v", typed)
		}
		cat, err := e.s.Add(ctx, e.sc, AddInput{ListID: l.ID, CatalogKey: "lemon", AddedBy: e.hh.Admin, Lang: language.English})
		if err != nil || cat.Entry.ItemID != typed.ItemID {
			t.Errorf("catalogue pick duplicated the item: %+v %v", cat.Entry, err)
		}
	})
}

func TestLinkCatalog(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		e := newEnv(t, db, "home")
		l, err := e.s.Start(ctx, e.sc, testDefaults) // categories without keys, like before the catalogue
		if err != nil {
			t.Fatal(err)
		}
		onion := e.add(t, l.ID, "Onion").Entry
		// Clear the link typed names get now, to look like an older item.
		if err := e.sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
			_, err := q.Exec(ctx, `UPDATE items SET catalog_key = NULL WHERE household_id = ?`, e.sc.HouseholdID())
			return err
		}); err != nil {
			t.Fatal(err)
		}
		e.add(t, l.ID, "Kumquats")
		// Typed in German in an English household.
		garlic := e.add(t, l.ID, "Knoblauch").Entry
		if err := e.sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
			_, err := q.Exec(ctx, `UPDATE items SET catalog_key = NULL WHERE household_id = ?`, e.sc.HouseholdID())
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if err := e.s.LinkCatalog(ctx, e.sc, catalogDefaults); err != nil {
			t.Fatal(err)
		}
		cats, _ := e.s.Categories(ctx, e.sc)
		keys := map[string]string{}
		for _, c := range cats {
			keys[c.Name] = c.CatalogKey
		}
		if keys["Produce"] != "produce" || keys["Pantry"] != "pantry" {
			t.Errorf("category keys %v", keys)
		}
		got, _ := e.s.Entry(ctx, e.sc, onion.ID)
		if got.ItemIcon != "onion" {
			t.Errorf("onion not linked: %+v", got)
		}
		if got, _ := e.s.Entry(ctx, e.sc, garlic.ID); got.ItemIcon != "garlic" {
			t.Errorf("German name not linked in an English household: %+v", got)
		}
		e.rec.take()
		if err := e.s.LinkCatalog(ctx, e.sc, catalogDefaults); err != nil {
			t.Fatal(err)
		}
		if ev := e.rec.take(); len(ev) != 0 {
			t.Errorf("second run changed something: %v", ev)
		}
	})
}

// TestLinkCatalogPrefersEntryName: of two items that match one entry, the
// one named like it gets the key, not the older one with an alias name.
func TestLinkCatalogPrefersEntryName(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		e := newEnv(t, db, "home")
		l, err := e.s.Start(ctx, e.sc, testDefaults)
		if err != nil {
			t.Fatal(err)
		}
		sea := e.add(t, l.ID, "Meersalz").Entry
		plain := e.add(t, l.ID, "Salz").Entry
		if err := e.sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
			_, err := q.Exec(ctx, `UPDATE items SET catalog_key = NULL WHERE household_id = ?`, e.sc.HouseholdID())
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if err := e.s.LinkCatalog(ctx, e.sc, catalogDefaults); err != nil {
			t.Fatal(err)
		}
		if got, _ := e.s.Entry(ctx, e.sc, plain.ID); got.ItemIcon != "shaker" {
			t.Errorf("Salz not linked: %+v", got)
		}
		if got, _ := e.s.Entry(ctx, e.sc, sea.ID); got.ItemIcon == "shaker" {
			t.Errorf("Meersalz took the key: %+v", got)
		}
	})
}

func TestCatalogueIsolation(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		a := newEnv(t, db, "a")
		b := newEnv(t, db, "b")
		la, _ := a.s.Start(ctx, a.sc, catalogDefaults)
		lb, _ := b.s.Start(ctx, b.sc, catalogDefaults)
		if _, err := a.s.Add(ctx, a.sc, AddInput{ListID: la.ID, CatalogKey: "lemon", AddedBy: a.hh.Admin, Lang: language.English}); err != nil {
			t.Fatal(err)
		}
		// B still gets the catalogue entry, with B's own category.
		got, err := b.s.Suggest(ctx, b.sc, lb.ID, "lemon", 3)
		if err != nil || len(got) == 0 || got[0].Catalog == nil || got[0].Catalog.Category == nil {
			t.Fatalf("household B: %+v %v", got, err)
		}
		cats, _ := b.s.Categories(ctx, b.sc)
		found := false
		for _, c := range cats {
			found = found || c.ID == got[0].Catalog.Category.ID
		}
		if !found {
			t.Error("category of another household offered")
		}
	})
}

// TestRenameRelinksCatalogue: a renamed item keeps its catalogue entry only
// while the name still names it; otherwise it takes the entry the new name
// names, if no other item holds it, or none.
func TestRenameRelinksCatalogue(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		e := newEnv(t, db, "home")
		l, err := e.s.Start(ctx, e.sc, catalogDefaults)
		if err != nil {
			t.Fatal(err)
		}
		milk := e.add(t, l.ID, "Milk").Entry
		e.add(t, l.ID, "Onion")
		key := func() string {
			t.Helper()
			var k *string
			if err := e.sc.Read().Get(ctx, &k, `SELECT catalog_key FROM items WHERE household_id = ? AND id = ?`,
				e.sc.HouseholdID(), milk.ItemID); err != nil {
				t.Fatal(err)
			}
			return deref(k)
		}
		for _, c := range []struct{ name, want string }{
			{"milk", "milk"},         // same entry
			{"Oat milk", "oat_milk"}, // another entry, free
			{"Barista blend", ""},    // no entry
			{"Onions", ""},           // an entry another item holds
			{"Milk", "milk"},         // back again
		} {
			if err := e.s.UpdateEntry(ctx, e.sc, milk.ID, EntryUpdate{Name: c.name, ListID: l.ID}); err != nil {
				t.Fatalf("rename to %q: %v", c.name, err)
			}
			if got := key(); got != c.want {
				t.Errorf("renamed to %q: catalog key %q, want %q", c.name, got, c.want)
			}
		}
	})
}
