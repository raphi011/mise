package recipes

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/google/uuid"
	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"github.com/raphi011/mise/internal/store"
)

// A recipe's slug is its address in member URLs, /recipes/{slug}: made from
// its name, unique within the household, and changed when the name changes.
// The ID stays the recipe's key everywhere else (foreign keys, the API, live
// events, form actions). recipe_slugs keeps the slugs a recipe had before,
// so links to them redirect to the current one.

const (
	// maxSlugLen caps a slug made from a name, before a collision suffix.
	maxSlugLen = 60
	// MaxHandleLen caps what Resolve looks up: a slug with its suffix, or a
	// recipe ID.
	MaxHandleLen = 80
	// fallbackSlug names a recipe whose name has no Latin letters or digits.
	fallbackSlug = "recipe"
)

// reservedSlugs are the static paths under /recipes/ (see web.Server's
// routes). A recipe named like one gets a suffix, since the static route
// would shadow it.
var reservedSlugs = []string{"new", "import", "search", "filters", "editor"}

// slugFold spells out letters that have no decomposition into a Latin base
// letter, and the German umlauts the way German writes them without dots.
var slugFold = strings.NewReplacer(
	"ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss",
	"æ", "ae", "œ", "oe", "ø", "o", "å", "aa",
	"ł", "l", "đ", "d", "ð", "d", "þ", "th", "ı", "i",
)

// Slugify turns a recipe name into the base of its slug: lowercase ASCII
// letters and digits joined by single hyphens, at most maxSlugLen long
// (cut at a hyphen where possible). A name with no Latin letters or digits
// gives fallbackSlug.
func Slugify(name string) string {
	s := slugFold.Replace(strings.ToLower(name))
	if t, _, err := transform.String(transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn))), s); err == nil {
		s = t
	}
	var b strings.Builder
	hyphen := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if hyphen && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			hyphen = false
			continue
		}
		hyphen = true
	}
	out := b.String()
	if len(out) > maxSlugLen {
		out = out[:maxSlugLen]
		if i := strings.LastIndexByte(out, '-'); i > maxSlugLen/2 {
			out = out[:i]
		}
		out = strings.TrimRight(out, "-")
	}
	if out == "" {
		return fallbackSlug
	}
	return out
}

// usableAlone reports whether base can be a slug without a suffix: it must
// not be a reserved path and must not read as a recipe ID, which Resolve
// tries first.
func usableAlone(base string) bool {
	if slices.Contains(reservedSlugs, base) {
		return false
	}
	_, err := uuid.Parse(base)
	return err != nil
}

// assignSlug gives recipe id a slug made from name: the base itself if no
// other recipe of the household has it, else the base with the lowest free
// suffix from -2. A slug in another recipe's history is free; taking it
// drops that history entry, so the old link now opens this recipe. It
// returns the slug. The caller writes it to the recipe.
func assignSlug(ctx context.Context, q store.Querier, sc store.Scoped, id uuid.UUID, name string) (string, error) {
	hh := sc.HouseholdID()
	base := Slugify(name)
	var taken []string
	if err := q.Select(ctx, &taken, `SELECT slug FROM recipes
		WHERE household_id = ? AND id <> ? AND (slug = ? OR slug LIKE ?)`, hh, id, base, base+"-%"); err != nil {
		return "", err
	}
	slug := base
	if !usableAlone(base) || slices.Contains(taken, base) {
		for n := 2; ; n++ {
			slug = base + "-" + strconv.Itoa(n)
			if !slices.Contains(taken, slug) {
				break
			}
		}
	}
	if _, err := q.Exec(ctx, `DELETE FROM recipe_slugs WHERE household_id = ? AND slug = ?`, hh, slug); err != nil {
		return "", err
	}
	return slug, nil
}

// renameSlug gives recipe id a new slug after its name changed from oldSlug's
// name to name, keeping oldSlug in the history. It returns the slug, which
// is oldSlug when the name still makes it.
func renameSlug(ctx context.Context, q store.Querier, sc store.Scoped, id uuid.UUID, oldSlug, name string, now store.Time) (string, error) {
	slug, err := assignSlug(ctx, q, sc, id, name)
	if err != nil || slug == oldSlug {
		return slug, err
	}
	// An ID placeholder (a recipe from before slugs) is no address anyone
	// used as a slug: Resolve finds the ID anyway.
	if _, perr := uuid.Parse(oldSlug); perr == nil {
		return slug, nil
	}
	_, err = q.Exec(ctx, `INSERT INTO recipe_slugs (household_id, slug, recipe_id, created_at) VALUES (?, ?, ?, ?)`,
		sc.HouseholdID(), oldSlug, id, now)
	return slug, err
}

// Resolve finds the recipe a URL handle names: its current slug, a slug it
// had before, or its ID. It returns the recipe's ID and current slug; a
// handle that differs from the slug is an old address the caller can
// redirect from. Unknown handles, and handles of other households, return
// ErrNotFound. Resolve does not check whether the recipe is in the bin.
func (s *Service) Resolve(ctx context.Context, sc store.Scoped, handle string) (uuid.UUID, string, error) {
	if handle == "" || len(handle) > MaxHandleLen {
		return uuid.Nil, "", ErrNotFound
	}
	q := sc.Read()
	hh := sc.HouseholdID()
	var row struct {
		ID   uuid.UUID
		Slug string
	}
	var err error
	if id, perr := uuid.Parse(handle); perr == nil {
		err = q.Get(ctx, &row, `SELECT id, slug FROM recipes WHERE household_id = ? AND id = ?`, hh, id)
	} else {
		handle = strings.ToLower(handle)
		err = q.Get(ctx, &row, `SELECT id, slug FROM recipes WHERE household_id = ? AND slug = ?`, hh, handle)
		if errors.Is(err, sql.ErrNoRows) {
			err = q.Get(ctx, &row, `SELECT r.id, r.slug FROM recipe_slugs h
				JOIN recipes r ON r.household_id = h.household_id AND r.id = h.recipe_id
				WHERE h.household_id = ? AND h.slug = ?`, hh, handle)
		}
	}
	if err != nil {
		return uuid.Nil, "", notFound(err)
	}
	return row.ID, row.Slug, nil
}

// SlugMissing gives every recipe of the household that still has its ID as
// its slug (recipes from before slugs) a slug made from its name. It is
// idempotent and returns how many recipes it changed.
func (s *Service) SlugMissing(ctx context.Context, sc store.Scoped) (int, error) {
	n := 0
	err := retryUnique(func() error {
		n = 0
		return sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
			var rows []struct {
				ID   uuid.UUID
				Name string
				Slug string
			}
			if err := q.Select(ctx, &rows, `SELECT id, name, slug FROM recipes WHERE household_id = ? ORDER BY created_at, id`,
				sc.HouseholdID()); err != nil {
				return err
			}
			for _, r := range rows {
				if id, err := uuid.Parse(r.Slug); err != nil || id != r.ID {
					continue
				}
				slug, err := assignSlug(ctx, q, sc, r.ID, r.Name)
				if err != nil {
					return err
				}
				if _, err := q.Exec(ctx, `UPDATE recipes SET slug = ? WHERE household_id = ? AND id = ?`,
					slug, sc.HouseholdID(), r.ID); err != nil {
					return err
				}
				n++
			}
			return nil
		})
	})
	return n, err
}

// retryUnique runs tx again, up to three times in all, when it fails on a
// unique constraint: two writers that picked the same free slug at once
// (Postgres; SQLite has one writer). Each attempt must be a whole
// transaction.
func retryUnique(tx func() error) error {
	var err error
	for range 3 {
		if err = tx(); !store.IsUniqueViolation(err) {
			return err
		}
	}
	return err
}
