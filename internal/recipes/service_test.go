package recipes

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/text/language"

	"github.com/raphi011/mise/internal/groceries"
	"github.com/raphi011/mise/internal/live"
	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

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

type env struct {
	db   *store.DB
	s    *Service
	g    *groceries.Service
	rec  *recorder
	clk  *clock
	sc   store.Scoped
	hh   storetest.Household
	list groceries.List
}

func newEnv(t *testing.T, db *store.DB, name string) env {
	t.Helper()
	rec := &recorder{}
	clk := &clock{t: time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)}
	hh := storetest.NewHousehold(t, db, name)
	g := groceries.New(rec, clk.Now)
	e := env{db: db, s: New(rec, g, clk.Now), g: g, rec: rec, clk: clk, sc: db.ScopeFor(hh.ID), hh: hh}
	var err error
	if e.list, err = g.Start(t.Context(), e.sc, groceries.Defaults{ListName: "Weekly shop"}); err != nil {
		t.Fatal(err)
	}
	rec.take()
	return e
}

func ings(lines ...string) []IngredientInput {
	out := make([]IngredientInput, len(lines))
	for i, l := range lines {
		out[i] = ParseIngredient(l, language.English)
	}
	return out
}

func ptr[T any](v T) *T { return &v }

func (e env) create(t *testing.T, in Input) Recipe {
	t.Helper()
	if in.Servings == 0 {
		in.Servings = 2
	}
	id, err := e.s.Create(t.Context(), e.sc, e.hh.Admin, in)
	if err != nil {
		t.Fatalf("create %q: %v", in.Name, err)
	}
	r, err := e.s.Get(t.Context(), e.sc, id, e.hh.Admin)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (e env) cooked(t *testing.T, recipe uuid.UUID, at time.Time) {
	t.Helper()
	err := e.sc.Tx(t.Context(), func(ctx context.Context, q store.Querier) error {
		now := store.T(at)
		_, err := q.Exec(ctx, `INSERT INTO cook_events (id, household_id, recipe_id, user_id, cooked_at, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, store.NewID(), e.sc.HouseholdID(), recipe, e.hh.Admin, now, now, now)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func names(cards []Card) []string {
	out := make([]string, len(cards))
	for i, c := range cards {
		out[i] = c.Name
	}
	return out
}

func TestCreateGetUpdate(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		e := newEnv(t, db, "home")
		r := e.create(t, Input{
			Name: "  Sticky   tofu ", Description: "Glazed", PrepMin: ptr(10), CookMin: ptr(25), Servings: 2,
			SourceURL: "https://example.com/tofu", Notes: "Double the glaze",
			Tags:        []string{"vegan", "Vegan", "asian"},
			Ingredients: ings("400 g firm tofu", "3 tbsp soy sauce", "1 thumb ginger"),
			Steps:       []string{"Press the tofu.", "  ", "Fry it."},
		})
		if r.Name != "Sticky tofu" || *r.TotalMin() != 35 || r.SourceURL != "https://example.com/tofu" {
			t.Errorf("recipe %+v", r)
		}
		if got := []string{r.Tags[0].Name, r.Tags[1].Name}; len(r.Tags) != 2 || got[0] != "Asian" || got[1] != "Vegan" {
			t.Errorf("tags %+v", r.Tags)
		}
		if len(r.Ingredients) != 3 || r.Ingredients[0].ItemName != "Firm tofu" || *r.Ingredients[0].Amount.Quantity != 400 ||
			r.Ingredients[0].Amount.Unit != "g" || r.Ingredients[2].Amount.Quantity != nil || r.Ingredients[2].ItemName != "Ginger" {
			t.Errorf("ingredients %+v", r.Ingredients)
		}
		if len(r.Steps) != 2 || r.Steps[1].Text != "Fry it." {
			t.Errorf("steps %+v", r.Steps)
		}
		if r.NotesBy == nil || r.NotesBy.ID != e.hh.Admin || r.CreatedBy.ID != e.hh.Admin {
			t.Errorf("attribution %+v %+v", r.NotesBy, r.CreatedBy)
		}
		if ev := e.rec.take(); !slices.Contains(ev, recipesChanged) || !slices.Contains(ev, catalogChanged) {
			t.Errorf("create events %v", ev)
		}

		// Ingredients reuse catalogue items by name.
		items, err := e.g.Suggest(ctx, e.sc, e.list.ID, "soy sauce", 10)
		if err != nil || len(items) != 1 {
			t.Fatalf("items %v, %v", items, err)
		}

		// The member edits without touching the notes: the notes keep
		// their author, the recipe records the editor.
		e.clk.Add(time.Hour)
		in := Input{Name: "Sticky tofu", Servings: 4, Notes: "Double the glaze", Tags: []string{"Vegan"},
			Ingredients: []IngredientInput{{ItemID: &r.Ingredients[1].ItemID, Amount: groceries.ParseAmount("4 tbsp", language.English)}},
			Steps:       []string{"Just fry it."}}
		if err := e.s.Update(ctx, e.sc, r.ID, e.hh.Member, in); err != nil {
			t.Fatal(err)
		}
		u, err := e.s.Get(ctx, e.sc, r.ID, e.hh.Admin)
		if err != nil {
			t.Fatal(err)
		}
		if u.UpdatedBy.ID != e.hh.Member || u.NotesBy.ID != e.hh.Admin || !u.NotesAt.Equal(r.NotesAt.UTC()) {
			t.Errorf("after edit: updated by %v, notes by %v at %v", u.UpdatedBy.ID, u.NotesBy.ID, u.NotesAt)
		}
		if len(u.Ingredients) != 1 || u.Ingredients[0].ItemName != "Soy sauce" || len(u.Steps) != 1 || len(u.Tags) != 1 || u.PrepMin != nil {
			t.Errorf("after edit %+v", u)
		}
		if ev := e.rec.take(); !slices.Contains(ev, recipeChanged(r.ID)) || slices.Contains(ev, catalogChanged) {
			t.Errorf("update events %v", ev)
		}

		in.Notes = "Use less glaze"
		if err := e.s.Update(ctx, e.sc, r.ID, e.hh.Member, in); err != nil {
			t.Fatal(err)
		}
		if u, _ = e.s.Get(ctx, e.sc, r.ID, e.hh.Admin); u.NotesBy.ID != e.hh.Member {
			t.Errorf("notes by %v after note change", u.NotesBy.ID)
		}
		in.Notes = ""
		if err := e.s.Update(ctx, e.sc, r.ID, e.hh.Member, in); err != nil {
			t.Fatal(err)
		}
		if u, _ = e.s.Get(ctx, e.sc, r.ID, e.hh.Admin); u.NotesBy != nil || u.NotesAt != nil {
			t.Errorf("cleared notes keep an author: %+v at %v", u.NotesBy, u.NotesAt)
		}
		in.Notes = "Use less glaze"

		if err := e.s.Update(ctx, e.sc, store.NewID(), e.hh.Admin, in); !errors.Is(err, ErrNotFound) {
			t.Errorf("update missing: %v", err)
		}
	})
}

func TestValidation(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		e := newEnv(t, db, "home")
		long := string(make([]rune, maxNameLen+1))
		for _, tc := range []struct {
			in    Input
			field string
		}{
			{Input{Name: " ", Servings: 2}, "name"},
			{Input{Name: "x", Servings: 0}, "servings"},
			{Input{Name: "x", Servings: 2, PrepMin: ptr(-1)}, "prep_min"},
			{Input{Name: "x", Servings: 2, SourceURL: "javascript:alert(1)"}, "source_url"},
			{Input{Name: "x", Servings: 2, Photo: "../../etc/passwd"}, "photo"},
			{Input{Name: "x", Servings: 2, Ingredients: []IngredientInput{{Name: "ok"}, {Name: " "}}}, "ingredients.1"},
			{Input{Name: "x", Servings: 2, Ingredients: []IngredientInput{{ItemID: ptr(store.NewID())}}}, "ingredients.0"},
			{Input{Name: "x" + long, Servings: 2}, "name"},
		} {
			_, err := e.s.Create(t.Context(), e.sc, e.hh.Admin, tc.in)
			var fe FieldErrors
			if !errors.As(err, &fe) || fe[tc.field] == "" {
				t.Errorf("%+v: %v, want error on %s", tc.in, err, tc.field)
			}
		}
	})
}

func TestLibraryFiltersAndSorts(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		e := newEnv(t, db, "home")
		dal := e.create(t, Input{Name: "Dal", PrepMin: ptr(10), CookMin: ptr(20), Tags: []string{"Vegan"}})
		e.clk.Add(time.Minute)
		bread := e.create(t, Input{Name: "Banana bread", CookMin: ptr(70), Tags: []string{"Baking"}})
		e.clk.Add(time.Minute)
		id, err := e.s.Create(ctx, e.sc, e.hh.Member, Input{Name: "Arrabbiata", Servings: 2, Tags: []string{"Vegan", "Italian"}})
		if err != nil {
			t.Fatal(err)
		}
		e.cooked(t, dal.ID, e.clk.Now().Add(-40*24*time.Hour))
		e.cooked(t, dal.ID, e.clk.Now().Add(-35*24*time.Hour))
		e.cooked(t, bread.ID, e.clk.Now().Add(-2*24*time.Hour))
		if err := e.s.SetFavourite(ctx, e.sc, e.hh.Admin, id, true); err != nil {
			t.Fatal(err)
		}
		tags, err := e.s.Tags(ctx, e.sc)
		if err != nil {
			t.Fatal(err)
		}
		tagID := map[string]uuid.UUID{}
		for _, tg := range tags {
			tagID[tg.Name] = tg.ID
		}

		for _, tc := range []struct {
			name string
			f    Filter
			want []string
		}{
			{"recently cooked", Filter{}, []string{"Banana bread", "Dal", "Arrabbiata"}},
			{"recently added", Filter{Sort: SortRecentlyAdded}, []string{"Arrabbiata", "Banana bread", "Dal"}},
			{"most cooked", Filter{Sort: SortMostCooked}, []string{"Dal", "Banana bread", "Arrabbiata"}},
			{"name", Filter{Sort: SortName}, []string{"Arrabbiata", "Banana bread", "Dal"}},
			{"quickest", Filter{Sort: SortQuickest}, []string{"Dal", "Banana bread", "Arrabbiata"}},
			{"mine", Filter{AddedBy: []uuid.UUID{e.hh.Admin}, Sort: SortName}, []string{"Banana bread", "Dal"}},
			{"either tag", Filter{Tags: []uuid.UUID{tagID["Italian"], tagID["Baking"]}, Sort: SortName}, []string{"Arrabbiata", "Banana bread"}},
			{"under 30", Filter{MaxMinutes: 30}, []string{"Dal"}},
			{"favourites", Filter{Favourites: true}, []string{"Arrabbiata"}},
			{"not cooked in a month", Filter{NotCookedRecently: true, Sort: SortName}, []string{"Arrabbiata", "Dal"}},
			{"never cooked", Filter{NeverCooked: true}, []string{"Arrabbiata"}},
			{"vegan by member", Filter{Tags: []uuid.UUID{tagID["Vegan"]}, AddedBy: []uuid.UUID{e.hh.Member}}, []string{"Arrabbiata"}},
		} {
			cards, err := e.s.Library(ctx, e.sc, e.hh.Admin, tc.f)
			if err != nil {
				t.Fatal(err)
			}
			if got := names(cards); !slices.Equal(got, tc.want) {
				t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
			}
		}

		cards, _ := e.s.Library(ctx, e.sc, e.hh.Admin, Filter{Sort: SortName})
		if a := cards[0]; !a.Favourite || a.CreatedBy.ID != e.hh.Member || !slices.Equal(a.Tags, []string{"Italian", "Vegan"}) {
			t.Errorf("card %+v", a)
		}
		if d := cards[2]; d.TimesCooked != 2 || d.LastCookedAt == nil || *d.TotalMin != 30 {
			t.Errorf("card %+v", d)
		}
		// Favourites are per member.
		if cards, _ := e.s.Library(ctx, e.sc, e.hh.Member, Filter{Favourites: true}); len(cards) != 0 {
			t.Errorf("member favourites %v", names(cards))
		}
		if n, err := e.s.Count(ctx, e.sc); err != nil || n != 3 {
			t.Errorf("count %d, %v", n, err)
		}
		if tags, _ := e.s.Tags(ctx, e.sc); tags[len(tags)-1].Name != "Vegan" || tags[len(tags)-1].Count != 2 {
			t.Errorf("tag counts %+v", tags)
		}
	})
}

func TestSearch(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		e := newEnv(t, db, "home")
		e.create(t, Input{Name: "Gingerbread", Ingredients: ings("2 tsp ground ginger")})
		e.create(t, Input{Name: "Ginger & sesame noodles", Ingredients: ings("1 thumb ginger")})
		e.create(t, Input{Name: "Sticky tofu", Ingredients: ings("400 g firm tofu", "1 thumb ginger")})
		e.create(t, Input{Name: "Miso ramen", Ingredients: ings("2 cm fresh ginger")})
		e.create(t, Input{Name: "Spätzle mit Soße", Ingredients: ings("200 ml Bratensoße")})
		e.create(t, Input{Name: "50% less sugar"})

		res, err := e.s.Search(ctx, e.sc, e.hh.Admin, "GING")
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, m := range res.Names {
			got = append(got, m.Name+":"+m.Name[m.Span.Start:m.Span.End])
		}
		if want := []string{"Ginger & sesame noodles:Ging", "Gingerbread:Ging"}; !slices.Equal(got, want) {
			t.Errorf("name matches %v, want %v", got, want)
		}
		got = nil
		for _, m := range res.Ingredients {
			got = append(got, m.Name+":"+m.ItemName[m.Span.Start:m.Span.End]+":"+m.Amount.Text)
		}
		// Prefix matches on the item name rank before word matches.
		if want := []string{"Sticky tofu:Ging:1 thumb", "Miso ramen:ging:2 cm"}; !slices.Equal(got, want) {
			t.Errorf("ingredient matches %v, want %v", got, want)
		}

		// Folding: "sosse" finds "Soße" and highlights the original letters.
		res, _ = e.s.Search(ctx, e.sc, e.hh.Admin, "sosse")
		if len(res.Names) != 1 || res.Names[0].Name[res.Names[0].Span.Start:res.Names[0].Span.End] != "Soße" {
			t.Errorf("folded name match %+v", res.Names)
		}
		if len(res.Ingredients) != 0 {
			t.Errorf("recipe repeated as ingredient match: %+v", res.Ingredients)
		}
		// LIKE wildcards are literal.
		if res, _ = e.s.Search(ctx, e.sc, e.hh.Admin, "%"); len(res.Names) != 1 {
			t.Errorf("%% search %+v", res.Names)
		}
		if res, _ = e.s.Search(ctx, e.sc, e.hh.Admin, " "); len(res.Names)+len(res.Ingredients) != 0 {
			t.Errorf("blank search %+v", res)
		}
	})
}

func TestTags(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		e := newEnv(t, db, "home")
		r := e.create(t, Input{Name: "Dal", Tags: []string{"Veggie", "Vegan"}})
		e.create(t, Input{Name: "Soup", Tags: []string{"Veggie"}})
		tag := func(name string) Tag {
			t.Helper()
			tags, _ := e.s.Tags(ctx, e.sc)
			for _, tg := range tags {
				if tg.Name == name {
					return tg
				}
			}
			t.Fatalf("no tag %q in %+v", name, tags)
			return Tag{}
		}
		if _, err := e.s.CreateTag(ctx, e.sc, "vegan"); err == nil {
			t.Error("duplicate tag created")
		}
		if err := e.s.RenameTag(ctx, e.sc, tag("Veggie").ID, "VEGAN"); err == nil {
			t.Error("rename onto existing name allowed")
		}
		if err := e.s.SetTagPinned(ctx, e.sc, tag("Vegan").ID, true); err != nil || !tag("Vegan").Pinned {
			t.Errorf("pin: %v", err)
		}
		// Merge: Dal has both, Soup only the merged one.
		if err := e.s.MergeTag(ctx, e.sc, tag("Veggie").ID, tag("Vegan").ID); err != nil {
			t.Fatal(err)
		}
		if v := tag("Vegan"); v.Count != 2 {
			t.Errorf("merged count %d", v.Count)
		}
		if tags, _ := e.s.Tags(ctx, e.sc); len(tags) != 1 {
			t.Errorf("tags after merge %+v", tags)
		}
		if err := e.s.RenameTag(ctx, e.sc, tag("Vegan").ID, "Plant-based"); err != nil {
			t.Fatal(err)
		}
		if got, _ := e.s.Get(ctx, e.sc, r.ID, e.hh.Admin); len(got.Tags) != 1 || got.Tags[0].Name != "Plant-based" {
			t.Errorf("recipe tags %+v", got.Tags)
		}
		if err := e.s.DeleteTag(ctx, e.sc, tag("Plant-based").ID); err != nil {
			t.Fatal(err)
		}
		if got, _ := e.s.Get(ctx, e.sc, r.ID, e.hh.Admin); len(got.Tags) != 0 {
			t.Errorf("tags after delete %+v", got.Tags)
		}
		if err := e.s.DeleteTag(ctx, e.sc, store.NewID()); !errors.Is(err, ErrNotFound) {
			t.Errorf("delete missing tag: %v", err)
		}
	})
}

func TestToggleIngredient(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		e := newEnv(t, db, "home")
		r := e.create(t, Input{Name: "Dal", Servings: 2, Ingredients: ings("250 g red lentils", "1 thumb ginger", "salt")})
		r.Ingredients[2].Optional = true
		lentils, ginger := r.Ingredients[0], r.Ingredients[1]

		st, err := e.s.ListStatus(ctx, e.sc, r.Ingredients)
		if err != nil {
			t.Fatal(err)
		}
		if on, total := st.Count(r.Ingredients); on != 0 || total != 2 || st.List.ID != e.list.ID {
			t.Errorf("before: %d of %d on %v", on, total, st.List.Name)
		}

		// Scaled to 4 servings, tagged with the recipe.
		got, err := e.s.ToggleIngredient(ctx, e.sc, e.hh.Member, r.ID, lentils.ID, 4, true, language.English)
		if err != nil {
			t.Fatal(err)
		}
		if a := got.Added; a == nil || a.Amount.Text != "500 g" || a.RecipeID == nil || *a.RecipeID != r.ID ||
			a.RecipeName != "Dal" || a.AddedBy != e.hh.Member {
			t.Errorf("added %+v", got.Added)
		}
		// A repeated tap (a double click, or a page that has not caught up)
		// leaves the entry as it is.
		if got, _ = e.s.ToggleIngredient(ctx, e.sc, e.hh.Member, r.ID, lentils.ID, 2, true, language.English); got.Added.Amount.Text != "500 g" {
			t.Errorf("added twice %+v", got.Added)
		}
		if _, err := e.s.ToggleIngredient(ctx, e.sc, e.hh.Admin, r.ID, ginger.ID, 4, true, language.English); err != nil {
			t.Fatal(err)
		}
		st, _ = e.s.ListStatus(ctx, e.sc, r.Ingredients)
		if on, total := st.Count(r.Ingredients); on != 2 || total != 2 || !st.On[ginger.ItemID] {
			t.Errorf("after adds: %d of %d", on, total)
		}

		off, err := e.s.ToggleIngredient(ctx, e.sc, e.hh.Admin, r.ID, lentils.ID, 2, false, language.English)
		if err != nil || len(off.Removed) != 1 || off.Added != nil {
			t.Fatalf("off: %+v, %v", off, err)
		}
		if st, _ = e.s.ListStatus(ctx, e.sc, r.Ingredients); st.On[lentils.ItemID] {
			t.Error("lentils still on the list")
		}
		if _, err := e.g.Restore(ctx, e.sc, off.Removed[0]); err != nil {
			t.Errorf("undo: %v", err)
		}

		if _, err := e.s.ToggleIngredient(ctx, e.sc, e.hh.Admin, r.ID, store.NewID(), 2, true, language.English); !errors.Is(err, ErrNotFound) {
			t.Errorf("unknown ingredient: %v", err)
		}
		if err := e.s.Delete(ctx, e.sc, r.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := e.s.ToggleIngredient(ctx, e.sc, e.hh.Admin, r.ID, ginger.ID, 2, true, language.English); !errors.Is(err, ErrNotFound) {
			t.Errorf("deleted recipe: %v", err)
		}
		// Entries from a deleted recipe stay on the list, without the tag.
		v, _ := e.g.ListView(ctx, e.sc, e.list.ID)
		for _, gr := range v.Groups {
			for _, en := range gr.Entries {
				if en.RecipeID != nil {
					t.Errorf("entry %s still names deleted recipe", en.ItemName)
				}
			}
		}
		// Servings out of range fall back to the recipe's own.
		rice := e.create(t, Input{Name: "Rice", Servings: 2, Ingredients: ings("100 g rice")})
		if got, err := e.s.ToggleIngredient(ctx, e.sc, e.hh.Admin, rice.ID, rice.Ingredients[0].ID, 100000, true, language.English); err != nil || got.Added.Amount.Text != "100 g" {
			t.Errorf("servings not bounded: %+v, %v", got.Added, err)
		}
	})
}

func TestDeleteRestoreFavourite(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		e := newEnv(t, db, "home")
		r := e.create(t, Input{Name: "Dal"})
		e.cooked(t, r.ID, e.clk.Now())

		info, err := e.s.DeleteInfo(ctx, e.sc, r.ID, e.hh.Admin)
		if err != nil || info.TimesCooked != 1 || len(info.Others) != 1 || info.Others[0].ID != e.hh.Member {
			t.Errorf("delete info %+v, %v", info, err)
		}
		if err := e.s.SetFavourite(ctx, e.sc, e.hh.Member, r.ID, true); err != nil {
			t.Fatal(err)
		}
		if err := e.s.SetFavourite(ctx, e.sc, e.hh.Member, r.ID, true); err != nil {
			t.Errorf("favourite twice: %v", err)
		}
		if got, _ := e.s.Get(ctx, e.sc, r.ID, e.hh.Member); !got.Favourite {
			t.Error("not a favourite")
		}
		if got, _ := e.s.Get(ctx, e.sc, r.ID, e.hh.Admin); got.Favourite {
			t.Error("favourite leaked to another member")
		}

		if err := e.s.Delete(ctx, e.sc, r.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := e.s.Get(ctx, e.sc, r.ID, e.hh.Admin); !errors.Is(err, ErrNotFound) {
			t.Errorf("get deleted: %v", err)
		}
		if err := e.s.Delete(ctx, e.sc, r.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("delete twice: %v", err)
		}
		if err := e.s.SetFavourite(ctx, e.sc, e.hh.Admin, r.ID, true); !errors.Is(err, ErrNotFound) {
			t.Errorf("favourite deleted: %v", err)
		}
		if err := e.s.Restore(ctx, e.sc, r.ID); err != nil {
			t.Errorf("restore: %v", err)
		}
		if got, _ := e.s.Get(ctx, e.sc, r.ID, e.hh.Member); !got.Favourite {
			t.Error("favourite lost over delete and restore")
		}
		if err := e.s.Delete(ctx, e.sc, r.ID); err != nil {
			t.Fatal(err)
		}
		e.clk.Add(UndoWindow + time.Second)
		if err := e.s.Restore(ctx, e.sc, r.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("restore after window: %v", err)
		}
	})
}

// TestConcurrentTagCreate checks that racing adds of one name give one tag
// and the "already exists" error, not a server error.
func TestConcurrentTagCreate(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		e := newEnv(t, db, "home")
		const n = 8
		errs := make([]error, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Go(func() { _, errs[i] = e.s.CreateTag(t.Context(), e.sc, "Soup") })
		}
		wg.Wait()
		ok := 0
		for _, err := range errs {
			var fe FieldErrors
			switch {
			case err == nil:
				ok++
			case errors.As(err, &fe) && fe["name"] == "recipes.error.tagExists":
			default:
				t.Errorf("create: %v", err)
			}
		}
		if ok != 1 {
			t.Errorf("%d creates succeeded, want 1", ok)
		}
	})
}
