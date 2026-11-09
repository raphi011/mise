// Package recipes owns recipes, their ingredients and steps, tags,
// favourites and the library's filters and search. Ingredients link to
// grocery items (internal/groceries), so they can go onto a list one by one.
//
// Every method takes a store.Scoped and filters every query on its
// household. Writes publish live events after they commit.
package recipes

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/groceries"
	"github.com/raphi011/mise/internal/i18n"
	"github.com/raphi011/mise/internal/live"
	"github.com/raphi011/mise/internal/store"
)

// UndoWindow is how long a deleted recipe can be restored before the sweep
// in internal/jobs deletes it for good.
const UndoWindow = groceries.UndoWindow

// ErrNotFound means the recipe, ingredient or tag does not exist in the
// household (or was deleted). Validation problems are returned as
// FieldErrors.
var ErrNotFound = errors.New("recipes: not found")

// FieldErrors maps form field names to the message describing what is wrong
// with them. Fields of repeated rows are named "ingredients.3" or "steps.0".
type FieldErrors map[string]i18n.Key

func (e FieldErrors) Error() string {
	fields := make([]string, 0, len(e))
	for f := range e {
		fields = append(fields, f)
	}
	slices.Sort(fields)
	return "recipes: invalid " + strings.Join(fields, ", ")
}

// Fields returns the messages by field.
func (e FieldErrors) Fields() map[string]i18n.Key { return e }

func (e FieldErrors) orNil() error {
	if len(e) == 0 {
		return nil
	}
	return e
}

// Person is a member as recipes show them: name and avatar colour.
type Person struct {
	ID    uuid.UUID
	Name  string
	Color string
}

// Service implements recipes on top of the store.
type Service struct {
	pub       live.Publisher
	groceries *groceries.Service
	now       func() time.Time
}

// New returns a Service that publishes changes to pub and puts ingredients
// on lists through g. now defaults to time.Now.
func New(pub live.Publisher, g *groceries.Service, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{pub: pub, groceries: g, now: now}
}

func (s *Service) clock() store.Time { return store.T(s.now()) }

func (s *Service) undoCutoff() store.Time { return store.T(s.now().Add(-UndoWindow)) }

func (s *Service) publish(sc store.Scoped, events ...live.Event) {
	if s.pub != nil && len(events) > 0 {
		s.pub.Publish(sc.HouseholdID(), events...)
	}
}

func recipeChanged(id uuid.UUID) live.Event {
	return live.Event{Kind: live.RecipeChanged, RecipeID: id}
}

var (
	recipesChanged = live.Event{Kind: live.RecipesChanged}
	catalogChanged = live.Event{Kind: live.CatalogChanged}
)

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func mustAffect(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// lockRecipe checks that a live recipe exists and, on Postgres, locks its
// row for the transaction, so concurrent edits replacing its ingredients
// and steps are ordered. SQLite transactions already hold the write lock.
func lockRecipe(ctx context.Context, q store.Querier, sc store.Scoped, id uuid.UUID) error {
	query := `SELECT id FROM recipes WHERE household_id = ? AND id = ? AND deleted_at IS NULL`
	if q.Dialect() == store.Postgres {
		query += ` FOR UPDATE`
	}
	var got uuid.UUID
	return notFound(q.Get(ctx, &got, query, sc.HouseholdID(), id))
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func timePtr(t *store.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := t.Time
	return &v
}

// escapeLike escapes LIKE wildcards for use with ESCAPE '\'.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}
