package recipes

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"golang.org/x/text/language"

	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

// TestHouseholdIsolation walks every Scoped method of this package against
// two households with identical content and checks that neither sees or
// changes the other's rows (docs/plan.md, Multi-tenancy).
func TestHouseholdIsolation(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		type fixture struct {
			env
			recipe, binned Recipe
			tag, spare     Tag
			share          Share
		}
		setup := func(name string) fixture {
			e := newEnv(t, db, name)
			f := fixture{env: e}
			f.recipe = e.create(t, Input{Name: "Dal", Tags: []string{"Vegan"}, Ingredients: ings("250 g red lentils")})
			f.binned = e.create(t, Input{Name: "Soup"})
			if err := e.s.Delete(ctx, e.sc, f.binned.ID); err != nil {
				t.Fatal(err)
			}
			if err := e.s.SetFavourite(ctx, e.sc, e.hh.Admin, f.recipe.ID, true); err != nil {
				t.Fatal(err)
			}
			f.tag = f.recipe.Tags[0]
			var err error
			if f.share, err = e.s.CreateShare(ctx, e.sc, e.hh.Admin, f.recipe.ID); err != nil {
				t.Fatal(err)
			}
			if f.spare, err = e.s.CreateTag(ctx, e.sc, "Spare"); err != nil {
				t.Fatal(err)
			}
			return f
		}
		a, b := setup("a"), setup("b")

		notFound := func(what string, err error) {
			t.Helper()
			if !errors.Is(err, ErrNotFound) {
				t.Errorf("%s: %v, want ErrNotFound", what, err)
			}
		}

		for _, pair := range []struct{ own, other fixture }{{a, b}, {b, a}} {
			own, other := pair.own, pair.other
			s, sc, me := own.s, own.sc, own.hh.Admin

			// Reads return only own rows.
			if cards, err := s.Library(ctx, sc, me, Filter{}); err != nil || len(cards) != 1 || cards[0].ID != own.recipe.ID {
				t.Errorf("library: %v, %v", names(cards), err)
			}
			if cards, _ := s.Library(ctx, sc, me, Filter{Tags: []uuid.UUID{other.tag.ID}}); len(cards) != 0 {
				t.Errorf("filter by other's tag: %v", names(cards))
			}
			if cards, _ := s.Library(ctx, sc, me, Filter{AddedBy: []uuid.UUID{other.hh.Admin}}); len(cards) != 0 {
				t.Errorf("filter by other's member: %v", names(cards))
			}
			if n, err := s.Count(ctx, sc); err != nil || n != 1 {
				t.Errorf("count %d, %v", n, err)
			}
			if res, err := s.Search(ctx, sc, me, "lentil"); err != nil || len(res.Ingredients) != 1 || res.Ingredients[0].ID != own.recipe.ID {
				t.Errorf("search: %+v, %v", res, err)
			}
			if res, _ := s.Search(ctx, sc, me, "dal"); len(res.Names) != 1 || res.Names[0].ID != own.recipe.ID {
				t.Errorf("search by name: %+v", res.Names)
			}
			if tags, err := s.Tags(ctx, sc); err != nil || len(tags) != 2 || tags[1].Count != 1 {
				t.Errorf("tags %+v, %v", tags, err)
			}
			_, err := s.Get(ctx, sc, other.recipe.ID, me)
			notFound("get other's recipe", err)
			_, err = s.DeleteInfo(ctx, sc, other.recipe.ID, me)
			notFound("delete info of other's recipe", err)
			if info, _ := s.DeleteInfo(ctx, sc, own.recipe.ID, me); len(info.Others) != 1 || info.Others[0].ID != own.hh.Member {
				t.Errorf("delete info lists %+v", info.Others)
			}
			if sh, ok, err := s.Share(ctx, sc, own.recipe.ID); err != nil || !ok || sh.Token != own.share.Token {
				t.Errorf("own share: %+v %v %v", sh, ok, err)
			}
			_, _, err = s.Share(ctx, sc, other.recipe.ID)
			notFound("share of other's recipe", err)
			_, err = s.Public(ctx, sc, other.recipe.ID)
			notFound("public view of other's recipe", err)
			if pub, err := s.Public(ctx, sc, own.recipe.ID); err != nil || pub.ID != own.recipe.ID {
				t.Errorf("public view of own recipe: %v, %v", pub.ID, err)
			}
			st, err := s.ListStatus(ctx, sc, own.recipe.Ingredients)
			if err != nil || st.List.ID != own.list.ID {
				t.Errorf("list status on %v, %v", st.List.ID, err)
			}

			// Writes to the other's rows fail and leave them alone.
			in := Input{Name: "Hijacked", Servings: 1}
			notFound("update", s.Update(ctx, sc, other.recipe.ID, me, in))
			notFound("delete", s.Delete(ctx, sc, other.recipe.ID))
			notFound("restore", s.Restore(ctx, sc, other.binned.ID))
			notFound("favourite", s.SetFavourite(ctx, sc, me, other.recipe.ID, false))
			_, err = s.CreateShare(ctx, sc, me, other.recipe.ID)
			notFound("share other's recipe", err)
			notFound("revoke other's share", s.RevokeShare(ctx, sc, other.recipe.ID))
			notFound("rename tag", s.RenameTag(ctx, sc, other.tag.ID, "Hijacked"))
			notFound("pin tag", s.SetTagPinned(ctx, sc, other.tag.ID, true))
			notFound("delete tag", s.DeleteTag(ctx, sc, other.spare.ID))
			notFound("merge other's tag in", s.MergeTag(ctx, sc, other.tag.ID, own.tag.ID))
			notFound("merge into other's tag", s.MergeTag(ctx, sc, own.spare.ID, other.tag.ID))
			_, err = s.ToggleIngredient(ctx, sc, me, other.recipe.ID, other.recipe.Ingredients[0].ID, 2, true, language.English)
			notFound("toggle other's ingredient", err)
			_, err = s.ToggleIngredient(ctx, sc, me, own.recipe.ID, other.recipe.Ingredients[0].ID, 2, true, language.English)
			notFound("toggle other's ingredient on own recipe", err)

			// Links to the other's recipes are ignored; names stay apart.
			linked := own.create(t, Input{Name: "Naan", Links: []uuid.UUID{other.recipe.ID, own.recipe.ID}})
			if len(linked.Links) != 1 || linked.Links[0].ID != own.recipe.ID {
				t.Errorf("links with other's recipe: %+v", linked.Links)
			}
			if _, ok, _ := s.FindByName(ctx, sc, "Hijacked"); ok {
				t.Errorf("found other's recipe by name")
			}
			if got, _ := s.NamesOf(ctx, sc, []uuid.UUID{other.recipe.ID}); len(got) != 0 {
				t.Errorf("names of other's recipe: %+v", got)
			}
			if err := s.Delete(ctx, sc, linked.ID); err != nil {
				t.Fatal(err)
			}

			// An ingredient may not point at the other's item.
			in.Ingredients = []IngredientInput{{ItemID: &other.recipe.Ingredients[0].ItemID}}
			var fe FieldErrors
			if _, err := s.Create(ctx, sc, me, in); !errors.As(err, &fe) {
				t.Errorf("create with other's item: %v", err)
			}

			if got, err := other.s.Get(ctx, other.sc, other.recipe.ID, other.hh.Admin); err != nil ||
				got.Name != "Dal" || !got.Favourite || len(got.Tags) != 1 || got.Tags[0].Name != "Vegan" || got.Tags[0].Pinned {
				t.Errorf("other's recipe changed: %+v, %v", got, err)
			}
			if sh, ok, err := other.s.Share(ctx, other.sc, other.recipe.ID); err != nil || !ok || sh.Token != other.share.Token {
				t.Errorf("other's share changed: %+v %v %v", sh, ok, err)
			}
			if tags, _ := other.s.Tags(ctx, other.sc); len(tags) != 2 {
				t.Errorf("other's tags changed: %+v", tags)
			}
			if err := other.s.Restore(ctx, other.sc, other.binned.ID); err != nil {
				t.Errorf("other's binned recipe: %v", err)
			}
			if err := other.s.Delete(ctx, other.sc, other.binned.ID); err != nil {
				t.Fatal(err)
			}
		}
	})
}
