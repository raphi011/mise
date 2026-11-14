// Package plan owns the meal plan: meals planned by day, cook events, and
// the suggestions built on them ("haven't cooked in a while", "tonight").
// Planned meals belong to recipes (internal/recipes); planning one can put
// some of its ingredients on the default list in the same transaction.
//
// Days are calendar dates in the household's time zone, and weeks start on
// the household's first day of the week.
//
// Every method takes a store.Scoped and filters every query on its
// household. Writes publish live events after they commit.
package plan

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
	"github.com/raphi011/mise/internal/recipes"
	"github.com/raphi011/mise/internal/store"
)

// UndoWindow is how long a removed meal or a cook event can be undone. The
// sweep in internal/jobs deletes removed meals for good afterwards.
const UndoWindow = groceries.UndoWindow

// MaxServings bounds a meal's servings, like a recipe's.
const MaxServings = 100

// ErrNotFound means the meal, recipe or cook event does not exist in the
// household (or was removed, or its recipe deleted).
var ErrNotFound = errors.New("plan: not found")

// FieldErrors maps form field names to the message describing what is wrong
// with them.
type FieldErrors map[string]i18n.Key

func (e FieldErrors) Error() string {
	fields := make([]string, 0, len(e))
	for f := range e {
		fields = append(fields, f)
	}
	slices.Sort(fields)
	return "plan: invalid " + strings.Join(fields, ", ")
}

// Fields returns the messages by field.
func (e FieldErrors) Fields() map[string]i18n.Key { return e }

// Service implements the meal plan on top of the store.
type Service struct {
	pub     live.Publisher
	recipes *recipes.Service
	now     func() time.Time
}

// New returns a Service that publishes changes to pub and puts ingredients
// on the default list through r. now defaults to time.Now.
func New(pub live.Publisher, r *recipes.Service, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{pub: pub, recipes: r, now: now}
}

func (s *Service) clock() store.Time { return store.T(s.now()) }

func (s *Service) undoCutoff() store.Time { return store.T(s.now().Add(-UndoWindow)) }

func (s *Service) publish(sc store.Scoped, events ...live.Event) {
	if s.pub != nil && len(events) > 0 {
		s.pub.Publish(sc.HouseholdID(), events...)
	}
}

var planChanged = live.Event{Kind: live.PlanChanged}

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

// Calendar is how the household counts days: its time zone and the first
// day of its week.
type Calendar struct {
	Location  *time.Location
	WeekStart time.Weekday
}

// Today returns the household's date at t.
func (c Calendar) Today(t time.Time) store.Date { return store.DateOf(t.In(c.Location)) }

// WindowOf returns the first day of the planner's seven days that contain
// d. The planner's weeks run from today rather than from the first day of
// the week: today's, and every seven days before and after it.
func WindowOf(today, d store.Date) store.Date {
	n := int(d.In(time.UTC).Sub(today.In(time.UTC)).Hours()) / 24
	k := n / 7
	if n%7 < 0 {
		k--
	}
	return today.AddDays(7 * k)
}

// WeekOf returns the first day of the week that contains d.
func (c Calendar) WeekOf(d store.Date) store.Date {
	return d.AddDays(-((int(d.Weekday()) - int(c.WeekStart) + 7) % 7))
}

// Calendar returns the household's time zone and week start. An unknown
// time zone counts as UTC.
func (s *Service) Calendar(ctx context.Context, sc store.Scoped) (Calendar, error) {
	var h struct {
		Timezone  string
		WeekStart string
	}
	if err := sc.Read().Get(ctx, &h, `SELECT timezone, week_start FROM households WHERE id = ?`, sc.HouseholdID()); err != nil {
		return Calendar{}, err
	}
	c := Calendar{Location: time.UTC, WeekStart: time.Monday}
	if loc, err := time.LoadLocation(h.Timezone); err == nil {
		c.Location = loc
	}
	if h.WeekStart == "sun" {
		c.WeekStart = time.Sunday
	}
	return c, nil
}

// Today returns the household's current date.
func (s *Service) Today(ctx context.Context, sc store.Scoped) (store.Date, error) {
	c, err := s.Calendar(ctx, sc)
	if err != nil {
		return store.Date{}, err
	}
	return c.Today(s.now()), nil
}

// lockRecipe checks that a live, non-draft recipe exists and returns its
// servings. On Postgres it locks the row so a concurrent delete waits.
func lockRecipe(ctx context.Context, q store.Querier, sc store.Scoped, id uuid.UUID) (int, error) {
	query := `SELECT servings FROM recipes WHERE household_id = ? AND id = ? AND deleted_at IS NULL AND draft = ?`
	if q.Dialect() == store.Postgres {
		query += ` FOR UPDATE`
	}
	var servings int
	return servings, notFound(q.Get(ctx, &servings, query, sc.HouseholdID(), id, false))
}
