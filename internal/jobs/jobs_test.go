package jobs

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"golang.org/x/text/language"

	"github.com/raphi011/mise/internal/groceries"
	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

func TestSweep(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
		clock := func() time.Time { return now }
		g := groceries.New(nil, clock)
		hh := storetest.NewHousehold(t, db, "home")
		sc := db.ScopeFor(hh.ID)

		list, err := g.Start(ctx, sc, groceries.Defaults{ListName: "List"})
		if err != nil {
			t.Fatal(err)
		}
		other, err := g.CreateList(ctx, sc, "Other")
		if err != nil {
			t.Fatal(err)
		}
		addEntry := func(name string, l groceries.List) groceries.Entry {
			a, err := g.Add(ctx, sc, groceries.AddInput{ListID: l.ID, Name: name, AddedBy: hh.Admin, Lang: language.English})
			if err != nil {
				t.Fatal(err)
			}
			return a.Entry
		}
		keep := addEntry("keep", list)
		removed := addEntry("removed", list)
		addEntry("on deleted list", other)
		if _, err := g.Remove(ctx, sc, removed.ID); err != nil {
			t.Fatal(err)
		}
		if err := g.DeleteList(ctx, sc, other.ID); err != nil {
			t.Fatal(err)
		}

		s := NewSweeper(db, slog.New(slog.DiscardHandler), func() time.Time { return now }, 0)
		// Within the Undo window nothing goes.
		now = now.Add(groceries.UndoWindow - time.Minute)
		if got, err := s.Sweep(ctx); err != nil || got != (Swept{}) {
			t.Fatalf("early sweep: %+v, %v", got, err)
		}
		if _, err := g.Restore(ctx, sc, removed.ID); err != nil {
			t.Fatalf("restore within window: %v", err)
		}
		if _, err := g.Remove(ctx, sc, removed.ID); err != nil {
			t.Fatal(err)
		}

		now = now.Add(groceries.UndoWindow + time.Minute)
		got, err := s.Sweep(ctx)
		if err != nil || got != (Swept{Entries: 1, Lists: 1}) {
			t.Fatalf("sweep: %+v, %v", got, err)
		}
		if _, err := g.Restore(ctx, sc, removed.ID); !errors.Is(err, groceries.ErrNotFound) {
			t.Errorf("swept entry restored: %v", err)
		}
		if err := g.RestoreList(ctx, sc, other.ID); !errors.Is(err, groceries.ErrNotFound) {
			t.Errorf("swept list restored: %v", err)
		}
		if _, err := g.Entry(ctx, sc, keep.ID); err != nil {
			t.Errorf("live entry swept: %v", err)
		}
		var n int
		if err := db.System().Read().QueryRow(ctx, `SELECT count(*) FROM list_entries`).Scan(&n); err != nil || n != 1 {
			t.Errorf("entries left: %d, %v", n, err)
		}
	})
}

func TestSeedGroceries(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		g := groceries.New(nil, nil)
		fresh := storetest.NewHousehold(t, db, "fresh")
		started := storetest.NewHousehold(t, db, "started")
		err := db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
			_, err := q.Exec(ctx, `UPDATE households SET default_language = 'de' WHERE id = ?`, fresh.ID)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := g.Start(ctx, db.ScopeFor(started.ID), groceries.Defaults{ListName: "Mine"}); err != nil {
			t.Fatal(err)
		}
		defaults := func(lang string) groceries.Defaults {
			return groceries.Defaults{ListName: "List " + lang, Categories: []groceries.NewCategory{{Name: "Obst", Color: "herb"}}}
		}
		for range 2 { // idempotent
			if err := SeedGroceries(ctx, db, g, defaults); err != nil {
				t.Fatal(err)
			}
		}
		lists, err := g.Lists(ctx, db.ScopeFor(fresh.ID))
		if err != nil || len(lists) != 1 || lists[0].Name != "List de" {
			t.Errorf("fresh household lists: %+v, %v", lists, err)
		}
		lists, err = g.Lists(ctx, db.ScopeFor(started.ID))
		if err != nil || len(lists) != 1 || lists[0].Name != "Mine" {
			t.Errorf("started household changed: %+v, %v", lists, err)
		}
	})
}
