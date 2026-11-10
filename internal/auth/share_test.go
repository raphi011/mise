package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
	"github.com/raphi011/mise/internal/tenant"
)

// addSharedRecipe inserts a recipe with a public link into f's household.
func addSharedRecipe(t *testing.T, db *store.DB, f fixture, token string) uuid.UUID {
	t.Helper()
	id := store.NewID()
	now := store.Now()
	err := db.System().Tx(t.Context(), func(ctx context.Context, q store.Querier) error {
		if _, err := q.Exec(ctx, `INSERT INTO recipes (id, household_id, name, name_key, description, servings, notes, draft, created_by, updated_by, created_at, updated_at)
			VALUES (?, ?, 'Dal', 'dal', '', 2, '', ?, ?, ?, ?, ?)`, id, f.household, false, f.admin, f.admin, now, now); err != nil {
			return err
		}
		_, err := q.Exec(ctx, `INSERT INTO recipe_shares (household_id, recipe_id, token, created_by, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)`, f.household, id, token, f.member, now, now)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestShareByToken(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		s, _ := newService(t, db)
		a, b := addHousehold(t, db, "a"), addHousehold(t, db, "b")
		recA := addSharedRecipe(t, db, a, "token-a")
		recB := addSharedRecipe(t, db, b, "token-b")

		// Each token resolves to its own household and recipe only.
		for _, c := range []struct {
			token     string
			household uuid.UUID
			recipe    uuid.UUID
		}{{"token-a", a.household, recA}, {"token-b", b.household, recB}} {
			sh, err := s.ShareByToken(ctx, c.token)
			if err != nil || sh.HouseholdID != c.household || sh.RecipeID != c.recipe {
				t.Errorf("%s: %+v, %v", c.token, sh, err)
			}
		}
		for _, bad := range []string{"", "token", "TOKEN-A", "token-a ", strings.Repeat("a", 65)} {
			if _, err := s.ShareByToken(ctx, bad); !errors.Is(err, ErrShareInvalid) {
				t.Errorf("%q: %v", bad, err)
			}
		}

		// A binned recipe's link stops working; a revoked link at once.
		exec := func(query string, args ...any) {
			t.Helper()
			if err := db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
				_, err := q.Exec(ctx, query, args...)
				return err
			}); err != nil {
				t.Fatal(err)
			}
		}
		exec(`UPDATE recipes SET deleted_at = ? WHERE id = ?`, store.Now(), recA)
		if _, err := s.ShareByToken(ctx, "token-a"); !errors.Is(err, ErrShareInvalid) {
			t.Errorf("binned recipe: %v", err)
		}
		exec(`UPDATE recipes SET deleted_at = NULL WHERE id = ?`, recA)
		exec(`DELETE FROM recipe_shares WHERE household_id = ? AND recipe_id = ?`, a.household, recA)
		if _, err := s.ShareByToken(ctx, "token-a"); !errors.Is(err, ErrShareInvalid) {
			t.Errorf("revoked link: %v", err)
		}
		if sh, err := s.ShareByToken(ctx, "token-b"); err != nil || sh.HouseholdID != b.household {
			t.Errorf("other household's link after revoke: %+v, %v", sh, err)
		}
	})
}

func TestRequireShare(t *testing.T) {
	db := storetest.SQLite(t)
	s, _ := newService(t, db)
	a := addHousehold(t, db, "a")
	recA := addSharedRecipe(t, db, a, "token-a")

	var got Share
	var household uuid.UUID
	mux := http.NewServeMux()
	notFound := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
	mux.Handle("GET /s/{token}", s.RequireShare(notFound)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, _ = ShareFromContext(r.Context())
		household, _ = tenant.HouseholdID(r.Context())
		if _, ok := tenant.UserID(r.Context()); ok {
			t.Error("a public link carries a user")
		}
	})))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/s/token-a", nil))
	if rec.Code != http.StatusOK || got.RecipeID != recA || household != a.household {
		t.Errorf("valid link: %d, %+v, household %v", rec.Code, got, household)
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/s/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown link: %d", rec.Code)
	}
}
