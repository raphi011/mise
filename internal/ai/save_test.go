package ai_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/ai"
	"github.com/raphi011/mise/internal/groceries"
	"github.com/raphi011/mise/internal/recipes"
	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

const tofuPage = `<!doctype html><html lang="en"><head>
<script type="application/ld+json">{"@type":"Recipe","name":"Crispy tofu","recipeYield":"3",
 "recipeIngredient":["400 g firm tofu","2 tbsp tamari or soy sauce","1 tbsp maple syrup"],
 "recipeInstructions":[{"@type":"HowToStep","text":"Press the tofu."},{"@type":"HowToStep","text":"Fry it."}]}</script>
</head><body></body></html>`

func TestSaveImport(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		base, f := pages(t, map[string]string{"/tofu": tofuPage})
		e := newImportEnv(t, db, "home", f)
		other := newImportEnv(t, db, "other", nil)
		other.item(t, "Agave syrup")
		e.item(t, "Firm tofu", "Soy sauce")
		runImports(t, e)
		anna := e.hh.Admin

		id, err := e.s.StartImport(ctx, e.sc, anna, ai.ImportInput{URL: base + "/tofu"})
		if err != nil {
			t.Fatal(err)
		}
		job := waitJob(t, e, id)
		if job.Status != ai.JobReady {
			t.Fatalf("job %+v", job)
		}
		matches := []ai.Match{}
		for _, ing := range job.Draft.Ingredients {
			matches = append(matches, ing.Match)
		}
		if want := []ai.Match{ai.MatchExact, ai.MatchCheck, ai.MatchCatalog}; !slices.Equal(matches, want) {
			t.Fatalf("matches %v, want %v", matches, want)
		}

		// Another household's item can't be picked; a name the household
		// has picks that item.
		agave := other.items["Agave syrup"]
		var fe ai.FieldErrors
		if err := e.s.PickItem(ctx, e.sc, anna, id, 2, ai.ItemPick{ItemID: &agave}); !errors.As(err, &fe) || fe["item"] == "" {
			t.Errorf("pick another household's item: %v", err)
		}
		if err := e.s.PickItem(ctx, e.sc, anna, id, 1, ai.ItemPick{Name: " soy SAUCE "}); err != nil {
			t.Fatal(err)
		}
		if err := e.s.PickItem(ctx, e.sc, anna, id, 2, ai.ItemPick{Name: ""}); !errors.As(err, &fe) {
			t.Errorf("empty name: %v", err)
		}
		if err := e.s.PickItem(ctx, e.sc, anna, id, 3, ai.ItemPick{Name: "Salt"}); !errors.Is(err, ai.ErrJobNotFound) {
			t.Errorf("row out of range: %v", err)
		}
		if err := e.s.PickItem(ctx, e.sc, e.hh.Member, id, 2, ai.ItemPick{Name: "Salt"}); !errors.Is(err, ai.ErrNotReady) {
			t.Errorf("another member picks: %v", err)
		}
		job, _ = e.s.Job(ctx, e.sc, anna, id)
		if ing := job.Draft.Ingredients[1]; ing.Match != ai.MatchExact || ing.ItemName != "Soy sauce" || *ing.ItemID != e.items["Soy sauce"] {
			t.Errorf("picked row %+v", ing)
		}
		if got := job.Draft.NewItems(); len(got) != 1 || got[0] != "Maple syrup" {
			t.Errorf("new items %v", got)
		}

		recipeID, err := e.s.SaveImport(ctx, e.sc, anna, id, "")
		if err != nil {
			t.Fatal(err)
		}
		rec, err := e.r.Get(ctx, e.sc, recipeID, anna)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, ing := range rec.Ingredients {
			names = append(names, ing.ItemName)
		}
		if rec.Name != "Crispy tofu" || rec.Servings != 3 || len(rec.Steps) != 2 || !slices.Equal(names, []string{"Firm tofu", "Soy sauce", "Maple syrup"}) ||
			rec.Ingredients[1].ItemID != e.items["Soy sauce"] || rec.Ingredients[0].Amount.Text != "400 g" {
			t.Errorf("saved recipe %+v", rec)
		}
		if _, err := e.s.Job(ctx, e.sc, anna, id); !errors.Is(err, ai.ErrJobNotFound) {
			t.Errorf("job after save: %v", err)
		}
		if _, err := e.s.SaveImport(ctx, e.sc, anna, id, ""); !errors.Is(err, ai.ErrJobNotFound) {
			t.Errorf("second save: %v", err)
		}
		if n, _ := e.r.Count(ctx, e.sc); n != 1 {
			t.Errorf("%d recipes after two saves", n)
		}
	})
}

// TestCreateWith checks the transaction an import save relies on: the
// extra statement's error rolls back the recipe and its new items, and a
// new item is filed under the proposed category when the household has it.
func TestCreateWith(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		e := newImportEnv(t, db, "home", nil)
		if _, err := e.g.CreateCategory(ctx, e.sc, groceries.NewCategory{Name: "Pantry", Color: "amber"}); err != nil {
			t.Fatal(err)
		}
		in := recipes.Input{Name: "Pancakes", Servings: 2, Ingredients: []recipes.IngredientInput{
			{Name: "Maple syrup", Category: "pantry"},
			{Name: "Buckwheat flour", Category: "Bakery"},
		}}
		boom := errors.New("boom")
		_, err := e.r.CreateWith(ctx, e.sc, e.hh.Admin, in, func(context.Context, store.Querier) error { return boom })
		if !errors.Is(err, boom) {
			t.Fatalf("create with failing extra: %v", err)
		}
		if n, _ := e.r.Count(ctx, e.sc); n != 0 {
			t.Errorf("%d recipes after a rollback", n)
		}
		if found, _ := e.g.SearchItems(ctx, e.sc, "Maple syrup", 1); len(found) != 0 {
			t.Errorf("item kept after a rollback: %+v", found)
		}

		id, err := e.r.CreateWith(ctx, e.sc, e.hh.Admin, in, nil)
		if err != nil {
			t.Fatal(err)
		}
		rec, err := e.r.Get(ctx, e.sc, id, e.hh.Admin)
		if err != nil {
			t.Fatal(err)
		}
		items, err := e.g.ItemsByID(ctx, e.sc, []uuid.UUID{rec.Ingredients[0].ItemID, rec.Ingredients[1].ItemID})
		if err != nil {
			t.Fatal(err)
		}
		if c := items[rec.Ingredients[0].ItemID].Category; c == nil || c.Name != "Pantry" {
			t.Errorf("maple syrup filed under %+v", c)
		}
		if c := items[rec.Ingredients[1].ItemID].Category; c != nil {
			t.Errorf("unknown category gave %+v", c)
		}
	})
}
