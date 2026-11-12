// Package nutrition estimates a recipe's nutrition per serving from its
// ingredients (docs/plan.md, Nutrition).
//
// Values are per 100 g and belong to grocery items. Turning an ingredient
// line into grams needs the amount's unit and, for volumes and counts, a
// density or a weight per piece, clove and so on; both live next to the
// values. The estimate is derived when a page renders and never stored.
//
// Only values a household typed in are used for now; the embedded
// ingredient catalogue is the second source once items link to it.
package nutrition

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/i18n"
	"github.com/raphi011/mise/internal/live"
	"github.com/raphi011/mise/internal/store"
)

// ErrNotFound means the item does not exist in the household. Validation
// problems are returned as FieldErrors.
var ErrNotFound = errors.New("nutrition: not found")

// FieldErrors maps form field names to the message describing what is wrong
// with them. Weights are named "weight.clove".
type FieldErrors map[string]i18n.Key

func (e FieldErrors) Error() string {
	fields := make([]string, 0, len(e))
	for f := range e {
		fields = append(fields, f)
	}
	slices.Sort(fields)
	return "nutrition: invalid " + strings.Join(fields, ", ")
}

// Fields returns the messages by field.
func (e FieldErrors) Fields() map[string]i18n.Key { return e }

func (e FieldErrors) orNil() error {
	if len(e) == 0 {
		return nil
	}
	return e
}

// WeightUnits are the units that can have a weight per unit, in the order
// the editor lists them. They are canonical groceries unit keys; a bare
// number counts as a piece.
var WeightUnits = []string{"piece", "clove", "slice", "bunch", "can", "pack"}

// Facts is what is known about one item: values per 100 g and the weights
// that convert other units to grams. Optional values are nil when unknown.
type Facts struct {
	Kcal                                                          float64
	Protein, Fat, SatFat, Carbs, Sugars, Fibre, SodiumMg, Density *float64
	// Weights are grams per unit, keyed by one of WeightUnits.
	Weights map[string]float64
}

// Service reads and writes the household's nutrition values.
type Service struct {
	pub live.Publisher
	now func() time.Time
}

// New returns a Service that publishes changes to pub. now defaults to
// time.Now.
func New(pub live.Publisher, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{pub: pub, now: now}
}

func (s *Service) clock() store.Time { return store.T(s.now()) }

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// itemRef is the row of a household item the nutrition belongs to.
func itemName(ctx context.Context, q store.Reader, sc store.Scoped, id uuid.UUID) (string, error) {
	var name string
	err := q.Get(ctx, &name, `SELECT name FROM items WHERE household_id = ? AND id = ?`, sc.HouseholdID(), id)
	return name, notFound(err)
}
