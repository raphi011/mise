// Package groceries owns categories, the item catalogue, shopping lists and
// their entries, the amount parser and the rules around them (merging
// amounts, purchase counts, suggestions, Undo).
//
// Every method takes a store.Scoped and filters every query on its
// household. Writes publish live events after they commit.
package groceries

import (
	"errors"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/i18n"
	"github.com/raphi011/mise/internal/live"
	"github.com/raphi011/mise/internal/store"
)

// UndoWindow is how long removed or cleared entries and deleted lists can
// be restored before the sweep in internal/jobs deletes them for good.
const UndoWindow = 24 * time.Hour

// Errors returned by Service methods. Validation problems are returned as
// FieldErrors instead.
var (
	// ErrNotFound means the list, entry, item or category does not exist in
	// the household (or was deleted).
	ErrNotFound = errors.New("groceries: not found")
	// ErrLastList means the household's only list cannot be deleted.
	ErrLastList = errors.New("groceries: the last list cannot be deleted")
)

// FieldErrors maps form field names to the message describing what is wrong
// with them.
type FieldErrors map[string]i18n.Key

func (e FieldErrors) Error() string {
	fields := make([]string, 0, len(e))
	for f := range e {
		fields = append(fields, f)
	}
	slices.Sort(fields)
	return "groceries: invalid " + strings.Join(fields, ", ")
}

// Fields returns the messages by field.
func (e FieldErrors) Fields() map[string]i18n.Key { return e }

func (e FieldErrors) orNil() error {
	if len(e) == 0 {
		return nil
	}
	return e
}

// Colors is the palette categories pick their tile colour from; the UI maps
// each name to a token pair for light and dark mode.
var Colors = []string{"herb", "sky", "amber", "coral", "plum", "sand"}

// Field limits.
const (
	maxNameLen   = 80
	maxAmountLen = 40
	maxNoteLen   = 200
)

// NameKey returns the case-folded, space-normalised form of a name used for
// uniqueness and lookup, so "Äpfel", "äpfel " and "ÄPFEL" are one item.
func NameKey(name string) string {
	return norm.NFC.String(cases.Fold().String(normalizeSpace(name)))
}

// CleanName normalises white space in a new item name and upper-cases its first
// letter, so "lemons" typed into the add field becomes "Lemons".
func CleanName(name string) string {
	name = normalizeSpace(name)
	r, size := utf8.DecodeRuneInString(name)
	if r == utf8.RuneError {
		return name
	}
	return string(unicode.ToUpper(r)) + name[size:]
}

func checkName(errs FieldErrors, field, name string) {
	switch {
	case name == "":
		errs[field] = "form.error.required"
	case utf8.RuneCountInString(name) > maxNameLen:
		errs[field] = "form.error.tooLong"
	}
}

func checkColor(errs FieldErrors, field, color string) {
	if !slices.Contains(Colors, color) {
		errs[field] = "form.error.choice"
	}
}

// Service implements groceries on top of the store.
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

// publish sends events for the scoped household once a write has
// committed.
func (s *Service) publish(sc store.Scoped, events ...live.Event) {
	if s.pub != nil {
		s.pub.Publish(sc.HouseholdID(), events...)
	}
}

func listChanged(id uuid.UUID) live.Event { return live.Event{Kind: live.ListChanged, ListID: id} }

var (
	listsChanged   = live.Event{Kind: live.ListsChanged}
	catalogChanged = live.Event{Kind: live.CatalogChanged}
)

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

// timePtr converts a nullable store time for callers.
func timePtr(t *store.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := t.Time
	return &v
}
