package groceries

import (
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/text/language"

	"github.com/raphi011/mise/internal/live"
	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

// recorder is a live.Publisher that keeps what was published.
type recorder struct {
	mu     sync.Mutex
	events []live.Event
}

func (r *recorder) Publish(_ uuid.UUID, events ...live.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, events...)
}

func (r *recorder) take() []live.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.events
	r.events = nil
	return e
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

var testDefaults = Defaults{
	ListName: "Shopping list",
	Categories: []NewCategory{
		{Name: "Produce", Color: "herb"},
		{Name: "Fridge", Color: "sky"},
		{Name: "Pantry", Color: "amber"},
	},
}

type env struct {
	s   *Service
	rec *recorder
	clk *clock
	sc  store.Scoped
	hh  storetest.Household
}

func newEnv(t *testing.T, db *store.DB, name string) env {
	t.Helper()
	rec := &recorder{}
	clk := &clock{t: time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)}
	hh := storetest.NewHousehold(t, db, name)
	return env{s: New(rec, clk.Now), rec: rec, clk: clk, sc: db.ScopeFor(hh.ID), hh: hh}
}

func (e env) add(t *testing.T, list uuid.UUID, text string) Added {
	t.Helper()
	a, name := ParseEntry(text, language.English)
	got, err := e.s.Add(t.Context(), e.sc, AddInput{ListID: list, Name: name, Amount: a, AddedBy: e.hh.Admin, Lang: language.English})
	if err != nil {
		t.Fatalf("add %q: %v", text, err)
	}
	return got
}

func (e env) view(t *testing.T, list uuid.UUID) View {
	t.Helper()
	v, err := e.s.ListView(t.Context(), e.sc, list)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func names(entries []Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.ItemName + " " + e.Amount.Text
	}
	return out
}

func TestStart(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		e := newEnv(t, db, "home")
		l, err := e.s.Start(ctx, e.sc, testDefaults)
		if err != nil {
			t.Fatal(err)
		}
		if l.Name != "Shopping list" {
			t.Errorf("default list %q", l.Name)
		}
		again, err := e.s.Start(ctx, e.sc, testDefaults)
		if err != nil || again.ID != l.ID {
			t.Errorf("second Start: %v, %v", again, err)
		}
		cats, err := e.s.Categories(ctx, e.sc)
		if err != nil || len(cats) != 3 || cats[0].Name != "Produce" || cats[2].Position != 2 {
			t.Errorf("categories %+v, %v", cats, err)
		}

		// Concurrent first visits create one list.
		f := newEnv(t, db, "other")
		var wg sync.WaitGroup
		ids := make([]uuid.UUID, 4)
		for i := range ids {
			wg.Go(func() {
				l, err := f.s.Start(ctx, f.sc, testDefaults)
				if err != nil {
					t.Error(err)
				}
				ids[i] = l.ID
			})
		}
		wg.Wait()
		lists, _ := f.s.Lists(ctx, f.sc)
		if len(lists) != 1 || slices.ContainsFunc(ids, func(id uuid.UUID) bool { return id != lists[0].ID }) {
			t.Errorf("concurrent Start: lists %v, ids %v", lists, ids)
		}
	})
}

func TestAddAndGroup(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		e := newEnv(t, db, "home")
		l, err := e.s.Start(ctx, e.sc, testDefaults)
		if err != nil {
			t.Fatal(err)
		}
		cats, _ := e.s.Categories(ctx, e.sc)
		produce, pantry := cats[0], cats[2]

		lemons := e.add(t, l.ID, "2 lemons")
		if lemons.Merged || lemons.Entry.ItemName != "Lemons" || lemons.Entry.Amount.Text != "2" {
			t.Errorf("first add: %+v", lemons)
		}
		if got := e.rec.take(); !slices.Contains(got, listChanged(l.ID)) {
			t.Errorf("events %v", got)
		}
		// Same item, same unit: amounts add up, whatever the case typed.
		more := e.add(t, l.ID, "LEMONS 3")
		if !more.Merged || more.Entry.ID != lemons.Entry.ID || more.Entry.Amount.Text != "5" || *more.Entry.Amount.Quantity != 5 {
			t.Errorf("merge: %+v", more.Entry)
		}
		// Incompatible units make a second entry.
		if other := e.add(t, l.ID, "500 g lemons"); other.Merged {
			t.Error("merged 500 g into 5")
		}
		e.add(t, l.ID, "rice")
		e.add(t, l.ID, "apples")
		e.add(t, l.ID, "zucchini")

		// Categorise: apples and lemons are produce, rice is pantry.
		for name, cat := range map[string]uuid.UUID{"Apples": produce.ID, "Lemons": produce.ID, "Rice": pantry.ID} {
			v := e.view(t, l.ID)
			for _, g := range v.Groups {
				for _, en := range g.Entries {
					if en.ItemName == name {
						if err := e.s.UpdateEntry(ctx, e.sc, en.ID, EntryUpdate{Name: name, Amount: en.Amount, CategoryID: &cat, ListID: l.ID}); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
		}
		v := e.view(t, l.ID)
		var got [][]string
		for _, g := range v.Groups {
			label := "-"
			if g.Category != nil {
				label = g.Category.Name
			}
			got = append(got, append([]string{label}, names(g.Entries)...))
		}
		want := [][]string{
			{"Produce", "Apples ", "Lemons 5", "Lemons 500 g"},
			{"Pantry", "Rice "},
			{"-", "Zucchini "},
		}
		if !slices.EqualFunc(got, want, slices.Equal) {
			t.Errorf("groups\n got %q\nwant %q", got, want)
		}
		if v.List.Open != 5 {
			t.Errorf("open %d", v.List.Open)
		}

		// Moving produce after pantry reorders the groups.
		if err := e.s.MoveCategory(ctx, e.sc, produce.ID, 2); err != nil {
			t.Fatal(err)
		}
		if v := e.view(t, l.ID); v.Groups[0].Category.Name != "Pantry" || v.Groups[1].Category.Name != "Produce" {
			t.Errorf("after move: %v, %v", v.Groups[0].Category, v.Groups[1].Category)
		}

		// Validation.
		if _, err := e.s.Add(ctx, e.sc, AddInput{ListID: l.ID, Name: " ", AddedBy: e.hh.Admin}); !isField(err, "text") {
			t.Errorf("empty name: %v", err)
		}
		if _, err := e.s.Add(ctx, e.sc, AddInput{ListID: store.NewID(), Name: "x", AddedBy: e.hh.Admin}); !errors.Is(err, ErrNotFound) {
			t.Errorf("unknown list: %v", err)
		}
	})
}

func isField(err error, field string) bool {
	var fe FieldErrors
	return errors.As(err, &fe) && fe[field] != ""
}

func TestCheckUndoAndCart(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		e := newEnv(t, db, "home")
		l, _ := e.s.Start(ctx, e.sc, testDefaults)
		milk := e.add(t, l.ID, "milk").Entry
		bread := e.add(t, l.ID, "bread").Entry

		if _, err := e.s.Check(ctx, e.sc, milk.ID); err != nil {
			t.Fatal(err)
		}
		// Checking twice counts one purchase.
		if _, err := e.s.Check(ctx, e.sc, milk.ID); err != nil {
			t.Fatal(err)
		}
		v := e.view(t, l.ID)
		if len(v.Cart) != 1 || v.Cart[0].ID != milk.ID || v.List.Open != 1 {
			t.Fatalf("cart %v open %d", names(v.Cart), v.List.Open)
		}
		item := suggestOne(t, e, l.ID, "milk")
		if item.PurchaseCount != 1 || item.LastPurchasedAt == nil {
			t.Errorf("after check: %+v", item)
		}

		// Undo takes the purchase back.
		if _, err := e.s.Uncheck(ctx, e.sc, milk.ID); err != nil {
			t.Fatal(err)
		}
		item = suggestOne(t, e, l.ID, "milk")
		if item.PurchaseCount != 0 || item.LastPurchasedAt != nil {
			t.Errorf("after uncheck: %+v", item)
		}

		// Buy both; clear the cart; undo; clear again.
		e.clk.Add(time.Minute)
		for _, id := range []uuid.UUID{milk.ID, bread.ID} {
			if _, err := e.s.Check(ctx, e.sc, id); err != nil {
				t.Fatal(err)
			}
		}
		cleared, err := e.s.ClearCart(ctx, e.sc, l.ID)
		if err != nil || cleared.Count != 2 {
			t.Fatalf("clear: %+v, %v", cleared, err)
		}
		if v := e.view(t, l.ID); len(v.Cart) != 0 || len(v.Groups) != 0 {
			t.Errorf("after clear: %+v", v)
		}
		// Cleared items come back under Usually bought.
		if v := e.view(t, l.ID); len(v.Usual) != 2 {
			t.Errorf("usual after clear: %+v", v.Usual)
		}
		if _, err := e.s.Check(ctx, e.sc, milk.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("check cleared entry: %v", err)
		}
		n, err := e.s.RestoreCart(ctx, e.sc, l.ID, cleared.At)
		if err != nil || n != 2 {
			t.Fatalf("restore cart: %d, %v", n, err)
		}
		if v := e.view(t, l.ID); len(v.Cart) != 2 || len(v.Usual) != 0 {
			t.Errorf("after restore: cart %v usual %v", names(v.Cart), v.Usual)
		}
		if n, _ := e.s.RestoreCart(ctx, e.sc, l.ID, cleared.At.Add(time.Second)); n != 0 {
			t.Errorf("restore with wrong time restored %d", n)
		}
		if c, err := e.s.ClearCart(ctx, e.sc, l.ID); err != nil || c.Count != 2 {
			t.Errorf("clear again: %+v, %v", c, err)
		}
		if c, err := e.s.ClearCart(ctx, e.sc, l.ID); err != nil || c.Count != 0 {
			t.Errorf("clear empty cart: %+v, %v", c, err)
		}
	})
}

func suggestOne(t *testing.T, e env, list uuid.UUID, name string) Item {
	t.Helper()
	s, err := e.s.Suggest(t.Context(), e.sc, list, name, 5)
	if err != nil || len(s) == 0 {
		t.Fatalf("suggest %q: %v, %v", name, s, err)
	}
	return s[0].Item
}

func TestRemoveRestore(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		e := newEnv(t, db, "home")
		l, _ := e.s.Start(ctx, e.sc, testDefaults)
		en := e.add(t, l.ID, "2 eggs").Entry
		if _, err := e.s.Remove(ctx, e.sc, en.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := e.s.Entry(ctx, e.sc, en.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("removed entry still found: %v", err)
		}
		if _, err := e.s.Remove(ctx, e.sc, en.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("remove twice: %v", err)
		}
		got, err := e.s.Restore(ctx, e.sc, en.ID)
		if err != nil || got.Amount.Text != "2" {
			t.Fatalf("restore: %+v, %v", got, err)
		}
		if _, err := e.s.Restore(ctx, e.sc, en.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("restore twice: %v", err)
		}
	})
}

func TestUpdateEntry(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		e := newEnv(t, db, "home")
		l, _ := e.s.Start(ctx, e.sc, testDefaults)
		other, err := e.s.CreateList(ctx, e.sc, "Drugstore")
		if err != nil {
			t.Fatal(err)
		}
		tofu := e.add(t, l.ID, "tofu").Entry
		e.add(t, l.ID, "soap")
		e.rec.take()

		err = e.s.UpdateEntry(ctx, e.sc, tofu.ID, EntryUpdate{
			Name: "Firm tofu", Amount: ParseAmount("400 g", language.English), Note: "the smoked one",
			NewCategory: NewCategory{Name: "Fridge", Color: "plum"}, ListID: other.ID,
		})
		if err != nil {
			t.Fatal(err)
		}
		got, err := e.s.Entry(ctx, e.sc, tofu.ID)
		if err != nil {
			t.Fatal(err)
		}
		// "Fridge" exists already, so it is reused rather than duplicated.
		if got.ItemName != "Firm tofu" || got.Amount.Text != "400 g" || got.Note != "the smoked one" ||
			got.ListID != other.ID || got.Category == nil || got.Category.Name != "Fridge" || got.Category.Color != "sky" {
			t.Errorf("updated entry %+v (category %+v)", got, got.Category)
		}
		events := e.rec.take()
		for _, want := range []live.Event{listChanged(l.ID), listChanged(other.ID), catalogChanged} {
			if !slices.Contains(events, want) {
				t.Errorf("missing event %v in %v", want, events)
			}
		}
		// The category is remembered for the item.
		if again := e.add(t, l.ID, "firm tofu"); again.Entry.Category == nil || again.Entry.Category.Name != "Fridge" {
			t.Errorf("category not remembered: %+v", again.Entry)
		}

		// Renaming onto another item's name is refused.
		err = e.s.UpdateEntry(ctx, e.sc, tofu.ID, EntryUpdate{Name: "SOAP", ListID: other.ID})
		if !isField(err, "name") {
			t.Errorf("rename onto existing: %v", err)
		}
		// A new category is created when the name is new.
		err = e.s.UpdateEntry(ctx, e.sc, tofu.ID, EntryUpdate{Name: "Firm tofu", ListID: other.ID, NewCategory: NewCategory{Name: "Asian", Color: "coral"}})
		if err != nil {
			t.Fatal(err)
		}
		cats, _ := e.s.Categories(ctx, e.sc)
		if len(cats) != 4 || cats[3].Name != "Asian" || cats[3].Items != 1 {
			t.Errorf("categories %+v", cats)
		}
		// Uncategorise.
		if err := e.s.UpdateEntry(ctx, e.sc, tofu.ID, EntryUpdate{Name: "Firm tofu", ListID: other.ID}); err != nil {
			t.Fatal(err)
		}
		if got, _ := e.s.Entry(ctx, e.sc, tofu.ID); got.Category != nil {
			t.Errorf("not uncategorised: %+v", got.Category)
		}
		if err := e.s.UpdateEntry(ctx, e.sc, tofu.ID, EntryUpdate{Name: "Firm tofu", ListID: store.NewID()}); !errors.Is(err, ErrNotFound) {
			t.Errorf("move to unknown list: %v", err)
		}
		bogus := store.NewID()
		if err := e.s.UpdateEntry(ctx, e.sc, tofu.ID, EntryUpdate{Name: "Firm tofu", ListID: other.ID, CategoryID: &bogus}); !isField(err, "category") {
			t.Errorf("unknown category: %v", err)
		}
	})
}

func TestListsAndCategories(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		e := newEnv(t, db, "home")
		first, _ := e.s.Start(ctx, e.sc, testDefaults)
		b, _ := e.s.CreateList(ctx, e.sc, "B")
		c, _ := e.s.CreateList(ctx, e.sc, "C")
		order := func() []string {
			lists, err := e.s.Lists(ctx, e.sc)
			if err != nil {
				t.Fatal(err)
			}
			var out []string
			for _, l := range lists {
				out = append(out, l.Name)
			}
			return out
		}
		if err := e.s.MoveList(ctx, e.sc, c.ID, 0); err != nil {
			t.Fatal(err)
		}
		if got := order(); !slices.Equal(got, []string{"C", "Shopping list", "B"}) {
			t.Errorf("order %v", got)
		}
		if err := e.s.MoveList(ctx, e.sc, c.ID, 99); err != nil {
			t.Fatal(err)
		}
		if err := e.s.RenameList(ctx, e.sc, b.ID, "  Bee  "); err != nil {
			t.Fatal(err)
		}
		if got := order(); !slices.Equal(got, []string{"Shopping list", "Bee", "C"}) {
			t.Errorf("order %v", got)
		}
		if err := e.s.RenameList(ctx, e.sc, b.ID, ""); !isField(err, "name") {
			t.Errorf("empty name: %v", err)
		}

		e.add(t, b.ID, "glue")
		if err := e.s.DeleteList(ctx, e.sc, b.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := e.s.List(ctx, e.sc, b.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("deleted list found: %v", err)
		}
		if _, err := e.s.Add(ctx, e.sc, AddInput{ListID: b.ID, Name: "x", AddedBy: e.hh.Admin}); !errors.Is(err, ErrNotFound) {
			t.Errorf("add to deleted list: %v", err)
		}
		if err := e.s.RestoreList(ctx, e.sc, b.ID); err != nil {
			t.Fatal(err)
		}
		if got := order(); !slices.Equal(got, []string{"Shopping list", "C", "Bee"}) {
			t.Errorf("after restore %v", got)
		}
		if v := e.view(t, b.ID); len(v.Groups) != 1 {
			t.Errorf("entries lost on restore: %+v", v)
		}
		for _, id := range []uuid.UUID{b.ID, c.ID} {
			if err := e.s.DeleteList(ctx, e.sc, id); err != nil {
				t.Fatal(err)
			}
		}
		if err := e.s.DeleteList(ctx, e.sc, first.ID); !errors.Is(err, ErrLastList) {
			t.Errorf("delete last list: %v", err)
		}

		// Categories.
		if _, err := e.s.CreateCategory(ctx, e.sc, NewCategory{Name: "produce", Color: "herb"}); !isField(err, "name") {
			t.Errorf("duplicate category: %v", err)
		}
		if _, err := e.s.CreateCategory(ctx, e.sc, NewCategory{Name: "Bakery", Color: "teal"}); !isField(err, "color") {
			t.Errorf("bad colour: %v", err)
		}
		bakery, err := e.s.CreateCategory(ctx, e.sc, NewCategory{Name: "Bakery", Color: "sand"})
		if err != nil || bakery.Position != 3 {
			t.Fatalf("create: %+v, %v", bakery, err)
		}
		if err := e.s.UpdateCategory(ctx, e.sc, bakery.ID, NewCategory{Name: "Fridge", Color: "sand"}); !isField(err, "name") {
			t.Errorf("rename onto existing: %v", err)
		}
		if err := e.s.UpdateCategory(ctx, e.sc, bakery.ID, NewCategory{Name: "Bread", Color: "coral"}); err != nil {
			t.Fatal(err)
		}
		bread := e.add(t, first.ID, "bread").Entry
		if err := e.s.UpdateEntry(ctx, e.sc, bread.ID, EntryUpdate{Name: "Bread", CategoryID: &bakery.ID, ListID: first.ID}); err != nil {
			t.Fatal(err)
		}
		if err := e.s.DeleteCategory(ctx, e.sc, bakery.ID); err != nil {
			t.Fatal(err)
		}
		if got, _ := e.s.Entry(ctx, e.sc, bread.ID); got.Category != nil {
			t.Errorf("item kept deleted category: %+v", got.Category)
		}
		if n, err := e.s.UncategorisedItems(ctx, e.sc); err != nil || n != 2 {
			t.Errorf("uncategorised: %d, %v", n, err)
		}
		if err := e.s.DeleteCategory(ctx, e.sc, bakery.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("delete twice: %v", err)
		}
	})
}

func TestSuggest(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		e := newEnv(t, db, "home")
		l, _ := e.s.Start(ctx, e.sc, testDefaults)
		other, _ := e.s.CreateList(ctx, e.sc, "Other")
		for _, n := range []string{"Cherry tomatoes", "Tomato paste", "Tomatoes", "Tom", "Bottom round", "100% juice"} {
			e.add(t, other.ID, n)
		}
		// Tomatoes are bought often.
		for range 3 {
			en := e.add(t, l.ID, "tomatoes").Entry
			if _, err := e.s.Check(ctx, e.sc, en.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := e.s.ClearCart(ctx, e.sc, l.ID); err != nil {
				t.Fatal(err)
			}
		}
		e.add(t, l.ID, "500 g cherry tomatoes")

		got, err := e.s.Suggest(ctx, e.sc, l.ID, "TOM", 10)
		if err != nil {
			t.Fatal(err)
		}
		var order []string
		for _, s := range got {
			order = append(order, s.Item.Name)
		}
		want := []string{"Tom", "Tomatoes", "Tomato paste", "Cherry tomatoes", "Bottom round"}
		if !slices.Equal(order, want) {
			t.Errorf("order %v, want %v", order, want)
		}
		if got[0].Match != MatchExact || got[1].Match != MatchPrefix || got[3].Match != MatchWord || got[4].Match != MatchContains {
			t.Errorf("matches %v", got)
		}
		if got[3].OnList == nil || got[3].OnList.Text != "500 g" || got[1].OnList != nil {
			t.Errorf("on list: %+v / %+v", got[3].OnList, got[1].OnList)
		}
		if got, _ := e.s.Suggest(ctx, e.sc, l.ID, "%", 10); len(got) != 1 || got[0].Item.Name != "100% juice" {
			t.Errorf("LIKE wildcard not escaped: %v", got)
		}
		if got, _ := e.s.Suggest(ctx, e.sc, l.ID, "  ", 10); len(got) != 0 {
			t.Errorf("blank query: %v", got)
		}
		if got, _ := e.s.Suggest(ctx, e.sc, l.ID, "to", 2); len(got) != 2 {
			t.Errorf("limit: %v", got)
		}
	})
}
