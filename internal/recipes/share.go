package recipes

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/store"
)

// Share is a recipe's public link: anyone with its token can read the
// recipe at /s/{token} without signing in. A recipe has at most one.
type Share struct {
	Token     string
	CreatedBy Person
	CreatedAt time.Time
}

// shareTokenBytes is the token's entropy: 256 bits, so links cannot be
// guessed or enumerated.
const shareTokenBytes = 32

func newShareToken() string {
	b := make([]byte, shareTokenBytes)
	_, _ = rand.Read(b) // never fails; see crypto/rand.Read
	return base64.RawURLEncoding.EncodeToString(b)
}

// Share returns the recipe's public link, if it has one. It returns
// ErrNotFound when the recipe does not exist or is in the bin.
func (s *Service) Share(ctx context.Context, sc store.Scoped, id uuid.UUID) (Share, bool, error) {
	q := sc.Read()
	var live uuid.UUID
	if err := q.Get(ctx, &live, `SELECT id FROM recipes WHERE household_id = ? AND id = ? AND deleted_at IS NULL AND draft = ?`,
		sc.HouseholdID(), id, false); err != nil {
		return Share{}, false, notFound(err)
	}
	return share(ctx, q, sc, id)
}

func share(ctx context.Context, q store.Reader, sc store.Scoped, id uuid.UUID) (Share, bool, error) {
	var row struct {
		Token     string
		CreatedBy uuid.UUID
		Name      string
		Color     string
		CreatedAt store.Time
	}
	err := q.Get(ctx, &row, `SELECT sh.token, sh.created_by, u.name, u.avatar_color AS color, sh.created_at
		FROM recipe_shares sh JOIN users u ON u.id = sh.created_by
		WHERE sh.household_id = ? AND sh.recipe_id = ?`, sc.HouseholdID(), id)
	if errors.Is(err, sql.ErrNoRows) {
		return Share{}, false, nil
	}
	if err != nil {
		return Share{}, false, err
	}
	return Share{
		Token:     row.Token,
		CreatedBy: Person{ID: row.CreatedBy, Name: row.Name, Color: row.Color},
		CreatedAt: row.CreatedAt.Time,
	}, true, nil
}

// CreateShare gives the recipe a public link and returns it. A recipe that
// already has one keeps it, so two members turning it on at once get the
// same link.
func (s *Service) CreateShare(ctx context.Context, sc store.Scoped, actor, id uuid.UUID) (Share, error) {
	var out Share
	var created bool
	err := sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		if err := lockRecipe(ctx, q, sc, id); err != nil {
			return err
		}
		now := s.clock()
		res, err := q.Exec(ctx, `INSERT INTO recipe_shares (household_id, recipe_id, token, created_by, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (household_id, recipe_id) DO NOTHING`,
			sc.HouseholdID(), id, newShareToken(), actor, now, now)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		created = n > 0
		var ok bool
		out, ok, err = share(ctx, q.Reader, sc, id)
		if err == nil && !ok {
			err = ErrNotFound
		}
		return err
	})
	if err != nil {
		return Share{}, err
	}
	if created {
		s.publish(sc, recipeChanged(id))
	}
	return out, nil
}

// RevokeShare removes the recipe's public link; its address stops working
// at once. It returns ErrNotFound when the recipe does not exist or is in
// the bin, and nil when it had no link.
func (s *Service) RevokeShare(ctx context.Context, sc store.Scoped, id uuid.UUID) error {
	var removed bool
	err := sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		if err := lockRecipe(ctx, q, sc, id); err != nil {
			return err
		}
		res, err := q.Exec(ctx, `DELETE FROM recipe_shares WHERE household_id = ? AND recipe_id = ?`, sc.HouseholdID(), id)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		removed = n > 0
		return err
	})
	if err != nil {
		return err
	}
	if removed {
		s.publish(sc, recipeChanged(id))
	}
	return nil
}

// Public returns what a public link shows of a recipe: name, description,
// photo, times, servings, source, ingredients and steps. Everything else
// (people, notes, tags, favourites, links, history) is left zero. It
// returns ErrNotFound when the recipe does not exist, is in the bin or has
// no public link.
func (s *Service) Public(ctx context.Context, sc store.Scoped, id uuid.UUID) (Recipe, error) {
	q := sc.Read()
	hh := sc.HouseholdID()
	var r struct {
		ID          uuid.UUID
		Name        string
		Description string
		Photo       *string
		PrepMin     *int
		CookMin     *int
		Servings    int
		SourceURL   *string
	}
	err := q.Get(ctx, &r, `SELECT r.id, r.name, r.description, r.photo, r.prep_min, r.cook_min, r.servings, r.source_url
		FROM recipes r
		JOIN recipe_shares sh ON sh.household_id = r.household_id AND sh.recipe_id = r.id
		WHERE r.household_id = ? AND r.id = ? AND r.deleted_at IS NULL AND r.draft = ?`, hh, id, false)
	if err != nil {
		return Recipe{}, notFound(err)
	}
	out := Recipe{
		ID: r.ID, Name: r.Name, Description: r.Description, Photo: deref(r.Photo),
		PrepMin: r.PrepMin, CookMin: r.CookMin, Servings: r.Servings, SourceURL: deref(r.SourceURL),
		Shared: true,
	}
	if err := loadMethod(ctx, q, hh, id, &out); err != nil {
		return Recipe{}, err
	}
	return out, nil
}
