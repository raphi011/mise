package recipes

import (
	"encoding/base64"
	"errors"
	"slices"
	"testing"

	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

func TestShare(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		e := newEnv(t, db, "home")
		r := e.create(t, Input{
			Name: "Dal", Description: "Weeknight lentils", Notes: "Ben likes it hot",
			Tags: []string{"Vegan"}, Ingredients: ings("250 g red lentils"), Steps: steps("Rinse the red lentils."),
		})
		e.rec.take()

		if _, ok, err := e.s.Share(ctx, e.sc, r.ID); err != nil || ok {
			t.Fatalf("share before creating: %v %v", ok, err)
		}
		if _, err := e.s.Public(ctx, e.sc, r.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("public view without a link: %v", err)
		}

		sh, err := e.s.CreateShare(ctx, e.sc, e.hh.Member, r.ID)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := base64.RawURLEncoding.DecodeString(sh.Token)
		if err != nil || len(raw) != shareTokenBytes || len(sh.Token) != 22 {
			t.Errorf("token %q: %d bytes, %v", sh.Token, len(raw), err)
		}
		if sh.CreatedBy.ID != e.hh.Member || !sh.CreatedAt.Equal(e.clk.Now()) {
			t.Errorf("share %+v", sh)
		}
		if ev := e.rec.take(); !slices.Contains(ev, recipeChanged(r.ID)) {
			t.Errorf("create published %v", ev)
		}
		if got, _ := e.s.Get(ctx, e.sc, r.ID, e.hh.Admin); !got.Shared {
			t.Error("Get does not report the link")
		}

		// Creating again keeps the link and publishes nothing.
		again, err := e.s.CreateShare(ctx, e.sc, e.hh.Admin, r.ID)
		if err != nil || again.Token != sh.Token || again.CreatedBy.ID != e.hh.Member {
			t.Errorf("second create: %+v, %v", again, err)
		}
		if ev := e.rec.take(); len(ev) != 0 {
			t.Errorf("second create published %v", ev)
		}

		// The public view carries the recipe and nothing about the household.
		pub, err := e.s.Public(ctx, e.sc, r.ID)
		if err != nil {
			t.Fatal(err)
		}
		if pub.Name != "Dal" || pub.Description != "Weeknight lentils" || len(pub.Ingredients) != 1 || len(pub.Steps) != 1 {
			t.Errorf("public view %+v", pub)
		}
		if pub.Notes != "" || len(pub.Tags) != 0 || pub.CreatedBy.Name != "" || pub.UpdatedBy.Name != "" || pub.NotesBy != nil {
			t.Errorf("public view leaks household data: %+v", pub)
		}

		// A recipe in the bin has no public view; restoring brings it back.
		if err := e.s.Delete(ctx, e.sc, r.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := e.s.Public(ctx, e.sc, r.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("public view of binned recipe: %v", err)
		}
		if _, err := e.s.CreateShare(ctx, e.sc, e.hh.Admin, r.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("share binned recipe: %v", err)
		}
		if err := e.s.Restore(ctx, e.sc, r.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := e.s.Public(ctx, e.sc, r.ID); err != nil {
			t.Errorf("public view after restore: %v", err)
		}
		e.rec.take()

		// Revoking removes the link; a new one has a new token.
		if err := e.s.RevokeShare(ctx, e.sc, r.ID); err != nil {
			t.Fatal(err)
		}
		if ev := e.rec.take(); !slices.Contains(ev, recipeChanged(r.ID)) {
			t.Errorf("revoke published %v", ev)
		}
		if _, err := e.s.Public(ctx, e.sc, r.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("public view after revoke: %v", err)
		}
		if err := e.s.RevokeShare(ctx, e.sc, r.ID); err != nil {
			t.Errorf("revoke without a link: %v", err)
		}
		if ev := e.rec.take(); len(ev) != 0 {
			t.Errorf("revoke without a link published %v", ev)
		}
		fresh, err := e.s.CreateShare(ctx, e.sc, e.hh.Admin, r.ID)
		if err != nil || fresh.Token == sh.Token {
			t.Errorf("new link reuses the token: %+v, %v", fresh, err)
		}
	})
}
