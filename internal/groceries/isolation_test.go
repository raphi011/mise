package groceries

import (
	"errors"
	"testing"
	"time"

	"golang.org/x/text/language"

	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

// TestHouseholdIsolation walks every Scoped method of this package against
// two households and checks that neither sees or changes the other's rows
// (docs/plan.md, Multi-tenancy).
func TestHouseholdIsolation(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()

		// Both households have the same names, so a leak shows up as an
		// extra row rather than hiding behind a name clash.
		type fixture struct {
			env
			list, spare           List
			cat                   Category
			open, inCart, removed Entry
			cleared               Cleared
		}
		setup := func(name string) fixture {
			e := newEnv(t, db, name)
			f := fixture{env: e}
			var err error
			if f.list, err = e.s.Start(ctx, e.sc, testDefaults); err != nil {
				t.Fatal(err)
			}
			if f.spare, err = e.s.CreateList(ctx, e.sc, "Spare"); err != nil {
				t.Fatal(err)
			}
			if err := e.s.DeleteList(ctx, e.sc, f.spare.ID); err != nil {
				t.Fatal(err)
			}
			cats, _ := e.s.Categories(ctx, e.sc)
			f.cat = cats[0]
			f.open = e.add(t, f.list.ID, "2 lemons").Entry
			f.inCart = e.add(t, f.list.ID, "milk").Entry
			cleared := e.add(t, f.list.ID, "bread").Entry
			if _, err := e.s.Check(ctx, e.sc, cleared.ID); err != nil {
				t.Fatal(err)
			}
			if f.cleared, err = e.s.ClearCart(ctx, e.sc, f.list.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := e.s.Check(ctx, e.sc, f.inCart.ID); err != nil {
				t.Fatal(err)
			}
			f.removed = e.add(t, f.list.ID, "eggs").Entry
			if _, err := e.s.Remove(ctx, e.sc, f.removed.ID); err != nil {
				t.Fatal(err)
			}
			return f
		}
		a, b := setup("a"), setup("b")
		// Both cleared their carts at the same clock time; move b's clock
		// so RestoreCart's timestamp cannot match by accident.
		b.clk.Add(time.Hour)

		notFound := func(what string, err error) {
			t.Helper()
			if !errors.Is(err, ErrNotFound) {
				t.Errorf("%s: %v, want ErrNotFound", what, err)
			}
		}

		for _, pair := range []struct{ own, other fixture }{{a, b}, {b, a}} {
			own, other := pair.own, pair.other
			s, sc := own.s, own.sc

			// Reads return only own rows.
			if lists, err := s.Lists(ctx, sc); err != nil || len(lists) != 1 || lists[0].ID != own.list.ID {
				t.Errorf("Lists: %v, %v", lists, err)
			}
			_, err := s.List(ctx, sc, other.list.ID)
			notFound("List(other)", err)
			if l, err := s.Start(ctx, sc, testDefaults); err != nil || l.ID != own.list.ID {
				t.Errorf("Start: %v, %v", l, err)
			}
			if cats, err := s.Categories(ctx, sc); err != nil || len(cats) != 3 || cats[0].ID != own.cat.ID {
				t.Errorf("Categories: %v, %v", cats, err)
			}
			if n, err := s.UncategorisedItems(ctx, sc); err != nil || n != 4 {
				t.Errorf("UncategorisedItems: %d, %v", n, err)
			}
			_, err = s.Entry(ctx, sc, other.open.ID)
			notFound("Entry(other)", err)
			_, err = s.ListView(ctx, sc, other.list.ID)
			notFound("ListView(other)", err)
			v, err := s.ListView(ctx, sc, own.list.ID)
			if err != nil || len(v.Groups) != 1 || len(v.Groups[0].Entries) != 1 || len(v.Cart) != 1 || len(v.Usual) != 1 {
				t.Errorf("ListView(own): %+v, %v", v, err)
			}
			if sug, err := s.Suggest(ctx, sc, own.list.ID, "e", 50); err != nil || len(sug) != 3 {
				t.Errorf("Suggest: %d suggestions, %v", len(sug), err)
			} else {
				for _, x := range sug {
					if x.Item.ID == other.open.ItemID || x.Item.ID == other.inCart.ItemID {
						t.Errorf("Suggest returned the other household's %s", x.Item.Name)
					}
				}
			}

			// Writes aimed at the other household fail.
			notFound("RenameList(other)", s.RenameList(ctx, sc, other.list.ID, "x"))
			notFound("MoveList(other)", s.MoveList(ctx, sc, other.list.ID, 0))
			notFound("DeleteList(other)", s.DeleteList(ctx, sc, other.list.ID))
			notFound("RestoreList(other)", s.RestoreList(ctx, sc, other.spare.ID))
			notFound("UpdateCategory(other)", s.UpdateCategory(ctx, sc, other.cat.ID, NewCategory{Name: "x", Color: "herb"}))
			notFound("MoveCategory(other)", s.MoveCategory(ctx, sc, other.cat.ID, 2))
			notFound("DeleteCategory(other)", s.DeleteCategory(ctx, sc, other.cat.ID))
			_, err = s.Add(ctx, sc, AddInput{ListID: other.list.ID, Name: "x", AddedBy: own.hh.Admin, Lang: language.English})
			notFound("Add(other list)", err)
			otherItem := other.open.ItemID
			_, err = s.Add(ctx, sc, AddInput{ListID: own.list.ID, ItemID: &otherItem, AddedBy: own.hh.Admin})
			notFound("Add(other item)", err)
			_, err = s.Check(ctx, sc, other.open.ID)
			notFound("Check(other)", err)
			_, err = s.Uncheck(ctx, sc, other.inCart.ID)
			notFound("Uncheck(other)", err)
			_, err = s.Remove(ctx, sc, other.open.ID)
			notFound("Remove(other)", err)
			_, err = s.Restore(ctx, sc, other.removed.ID)
			notFound("Restore(other)", err)
			_, err = s.ClearCart(ctx, sc, other.list.ID)
			notFound("ClearCart(other)", err)
			_, err = s.RestoreCart(ctx, sc, other.list.ID, other.cleared.At)
			notFound("RestoreCart(other)", err)
			notFound("UpdateEntry(other)", s.UpdateEntry(ctx, sc, other.open.ID, EntryUpdate{Name: "x", ListID: other.list.ID}))
			notFound("UpdateEntry(to other list)", s.UpdateEntry(ctx, sc, own.open.ID, EntryUpdate{Name: "Lemons", ListID: other.list.ID}))
			otherCat := other.cat.ID
			if err := s.UpdateEntry(ctx, sc, own.open.ID, EntryUpdate{Name: "Lemons", ListID: own.list.ID, CategoryID: &otherCat}); !isField(err, "category") {
				t.Errorf("UpdateEntry(other category): %v", err)
			}
			// Own categories and lists are unaffected by the other's
			// names: creating one that only the other has works.
			if _, err := s.CreateCategory(ctx, sc, NewCategory{Name: "Only " + own.hh.ID.String(), Color: "herb"}); err != nil {
				t.Errorf("CreateCategory: %v", err)
			}
			if _, err := s.CreateList(ctx, sc, "Extra"); err != nil {
				t.Errorf("CreateList: %v", err)
			}
		}

		// Nothing of either household changed through the other.
		for _, f := range []fixture{a, b} {
			v, err := f.s.ListView(ctx, f.sc, f.list.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(v.Groups) != 1 || v.Groups[0].Entries[0].ID != f.open.ID || v.Groups[0].Entries[0].ItemName != "Lemons" ||
				len(v.Cart) != 1 || v.Cart[0].ID != f.inCart.ID {
				t.Errorf("household %s changed: %+v", f.hh.ID, v)
			}
			if l, err := f.s.List(ctx, f.sc, f.list.ID); err != nil || l.Name != "Shopping list" {
				t.Errorf("list changed: %+v, %v", l, err)
			}
			if cats, _ := f.s.Categories(ctx, f.sc); len(cats) != 4 || cats[0].Name != "Produce" {
				t.Errorf("categories changed: %+v", cats)
			}
			if _, err := f.s.Entry(ctx, f.sc, f.removed.ID); !errors.Is(err, ErrNotFound) {
				t.Errorf("removed entry restored by the other household: %v", err)
			}
		}
	})
}
