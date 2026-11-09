package groceries

import (
	"errors"
	"sync"
	"testing"

	"golang.org/x/text/language"

	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

// parallel runs f n times at once and fails on any error.
func parallel(t *testing.T, n int, f func() error) {
	t.Helper()
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			if err := f(); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
}

// TestConcurrentAdd checks that adds of the same item serialise on the list,
// so merged amounts add up and no duplicate entries appear.
func TestConcurrentAdd(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		e := newEnv(t, db, "home")
		l, err := e.s.Start(ctx, e.sc, testDefaults)
		if err != nil {
			t.Fatal(err)
		}
		const n = 20
		add := func(text string) func() error {
			return func() error {
				a, name := ParseEntry(text, language.English)
				_, err := e.s.Add(ctx, e.sc, AddInput{ListID: l.ID, Name: name, Amount: a, AddedBy: e.hh.Admin, Lang: language.English})
				return err
			}
		}

		// Onto an entry already on the list.
		e.add(t, l.ID, "1 lemons")
		parallel(t, n, add("1 lemons"))
		// And with no entry yet.
		parallel(t, n, add("1 limes"))

		got := names(e.view(t, l.ID).Groups[0].Entries)
		want := map[string]bool{"Lemons 21": true, "Limes 20": true}
		if len(got) != 2 || !want[got[0]] || !want[got[1]] {
			t.Errorf("entries after concurrent adds: %q, want Lemons 21 and Limes 20", got)
		}
	})
}

// TestConcurrentCheck checks that a double tap or two shoppers checking off
// (or putting back) the same entry count one purchase.
func TestConcurrentCheck(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		e := newEnv(t, db, "home")
		l, err := e.s.Start(ctx, e.sc, testDefaults)
		if err != nil {
			t.Fatal(err)
		}
		entry := e.add(t, l.ID, "milk").Entry
		purchases := func() int {
			t.Helper()
			var n int
			if err := e.sc.Read().QueryRow(ctx, `SELECT purchase_count FROM items WHERE household_id = ? AND id = ?`,
				e.hh.ID, entry.ItemID).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}

		const n = 20
		parallel(t, n, func() error {
			e, err := e.s.Check(ctx, e.sc, entry.ID)
			if err == nil && !e.Checked() {
				return errors.New("Check returned an unchecked entry")
			}
			return err
		})
		if got := purchases(); got != 1 {
			t.Errorf("after concurrent checks: %d purchases, want 1", got)
		}
		parallel(t, n, func() error {
			e, err := e.s.Uncheck(ctx, e.sc, entry.ID)
			if err == nil && e.Checked() {
				return errors.New("Uncheck returned a checked entry")
			}
			return err
		})
		if got := purchases(); got != 0 {
			t.Errorf("after concurrent unchecks: %d purchases, want 0", got)
		}
	})
}
