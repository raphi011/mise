package recipes

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/groceries"
	"github.com/raphi011/mise/internal/store"
)

// Card is a recipe as the library and search list it.
type Card struct {
	ID uuid.UUID
	// Slug is the recipe's address, see Slugify.
	Slug         string
	Name         string
	Photo        string
	TotalMin     *int
	Servings     int
	Tags         []string
	Favourite    bool
	CreatedBy    Person
	CreatedAt    time.Time
	TimesCooked  int
	LastCookedAt *time.Time
}

// Sort orders the library.
type Sort string

// Sorts.
const (
	SortRecentlyCooked Sort = "cooked"
	SortRecentlyAdded  Sort = "added"
	SortMostCooked     Sort = "most"
	SortName           Sort = "name"
	SortQuickest       Sort = "quickest"
)

// Sorts lists the sorts in the order the filter sheet offers them.
var Sorts = []Sort{SortRecentlyCooked, SortRecentlyAdded, SortMostCooked, SortName, SortQuickest}

// Filter narrows the library. Within AddedBy and within Tags any one
// matches; all set conditions must hold.
type Filter struct {
	AddedBy []uuid.UUID
	Tags    []uuid.UUID
	// MaxMinutes keeps recipes whose known prep plus cook time is at most
	// this; zero means any.
	MaxMinutes int
	Favourites bool
	// NotCookedRecently keeps recipes not cooked within NotCookedWindow,
	// never-cooked ones included.
	NotCookedRecently bool
	NeverCooked       bool
	Sort              Sort
	// Exclude leaves out one recipe, such as the one a picker would
	// replace. It is not a filter the viewer chose, so IsZero ignores it.
	Exclude uuid.UUID
}

// NotCookedWindow is "a month" for Filter.NotCookedRecently.
const NotCookedWindow = 30 * 24 * time.Hour

// IsZero reports whether the filter keeps every recipe.
func (f Filter) IsZero() bool {
	return len(f.AddedBy) == 0 && len(f.Tags) == 0 && f.MaxMinutes == 0 &&
		!f.Favourites && !f.NotCookedRecently && !f.NeverCooked
}

type cardRow struct {
	ID           uuid.UUID
	Slug         string
	Name         string
	NameKey      string
	Photo        *string
	PrepMin      *int
	CookMin      *int
	Servings     int
	Favourite    bool
	CreatedBy    uuid.UUID
	CreatedName  string
	CreatedColor string
	CreatedAt    store.Time
	TimesCooked  int
	LastCookedAt *store.Time
}

func (r cardRow) card() Card {
	return Card{
		ID: r.ID, Slug: r.Slug, Name: r.Name, Photo: deref(r.Photo), TotalMin: totalMin(r.PrepMin, r.CookMin), Servings: r.Servings,
		Favourite: r.Favourite,
		CreatedBy: Person{ID: r.CreatedBy, Name: r.CreatedName, Color: r.CreatedColor},
		CreatedAt: r.CreatedAt.Time, TimesCooked: r.TimesCooked, LastCookedAt: timePtr(r.LastCookedAt),
	}
}

// cardSelect selects cardRow columns of live, non-draft recipes. Its
// arguments are the viewer, the household and false (for draft); callers
// append conditions on r.
const cardSelect = `SELECT r.id, r.slug, r.name, r.name_key, r.photo, r.prep_min, r.cook_min, r.servings,
		EXISTS (SELECT 1 FROM favourites f WHERE f.household_id = r.household_id AND f.recipe_id = r.id AND f.user_id = ?) AS favourite,
		r.created_by, u.name AS created_name, u.avatar_color AS created_color, r.created_at,
		(SELECT count(*) FROM cook_events ce WHERE ce.household_id = r.household_id AND ce.recipe_id = r.id) AS times_cooked,
		(SELECT max(ce.cooked_at) FROM cook_events ce WHERE ce.household_id = r.household_id AND ce.recipe_id = r.id) AS last_cooked_at
	FROM recipes r JOIN users u ON u.id = r.created_by
	WHERE r.household_id = ? AND r.deleted_at IS NULL AND r.draft = ?`

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

// Library returns the recipes matching the filter as viewer sees them.
func (s *Service) Library(ctx context.Context, sc store.Scoped, viewer uuid.UUID, f Filter) ([]Card, error) {
	cards, _, err := s.LibraryPage(ctx, sc, viewer, f, 0)
	return cards, err
}

// LibraryPage returns the first limit recipes matching the filter, in its
// sort order, and how many match in all. A limit of zero or less returns
// every match. Sorting happens in Go (see sortCards), so every match is
// read; only the returned cards get their tags.
func (s *Service) LibraryPage(ctx context.Context, sc store.Scoped, viewer uuid.UUID, f Filter, limit int) ([]Card, int, error) {
	cards, err := s.matching(ctx, sc, viewer, f)
	if err != nil {
		return nil, 0, err
	}
	matched := len(cards)
	if limit > 0 && limit < matched {
		cards = cards[:limit:limit]
	}
	if err := s.attachTags(ctx, sc, cards); err != nil {
		return nil, 0, err
	}
	return cards, matched, nil
}

// CountMatching returns how many recipes match the filter.
func (s *Service) CountMatching(ctx context.Context, sc store.Scoped, viewer uuid.UUID, f Filter) (int, error) {
	cards, err := s.matching(ctx, sc, viewer, f)
	return len(cards), err
}

// matching returns the cards matching the filter in its sort order,
// without their tags.
func (s *Service) matching(ctx context.Context, sc store.Scoped, viewer uuid.UUID, f Filter) ([]Card, error) {
	query := cardSelect
	args := []any{viewer, sc.HouseholdID(), false}
	if len(f.AddedBy) > 0 {
		query += ` AND r.created_by IN (` + placeholders(len(f.AddedBy)) + `)`
		for _, id := range f.AddedBy {
			args = append(args, id)
		}
	}
	if len(f.Tags) > 0 {
		query += ` AND EXISTS (SELECT 1 FROM recipe_tags rt WHERE rt.household_id = r.household_id AND rt.recipe_id = r.id
			AND rt.tag_id IN (` + placeholders(len(f.Tags)) + `))`
		for _, id := range f.Tags {
			args = append(args, id)
		}
	}
	if f.MaxMinutes > 0 {
		query += ` AND (r.prep_min IS NOT NULL OR r.cook_min IS NOT NULL)
			AND COALESCE(r.prep_min, 0) + COALESCE(r.cook_min, 0) <= ?`
		args = append(args, f.MaxMinutes)
	}
	if f.Favourites {
		query += ` AND EXISTS (SELECT 1 FROM favourites f WHERE f.household_id = r.household_id AND f.recipe_id = r.id AND f.user_id = ?)`
		args = append(args, viewer)
	}
	if f.NotCookedRecently {
		query += ` AND NOT EXISTS (SELECT 1 FROM cook_events ce WHERE ce.household_id = r.household_id AND ce.recipe_id = r.id
			AND ce.cooked_at >= ?)`
		args = append(args, store.T(s.now().Add(-NotCookedWindow)))
	}
	if f.NeverCooked {
		query += ` AND NOT EXISTS (SELECT 1 FROM cook_events ce WHERE ce.household_id = r.household_id AND ce.recipe_id = r.id)`
	}
	if f.Exclude != uuid.Nil {
		query += ` AND r.id <> ?`
		args = append(args, f.Exclude)
	}
	var rows []cardRow
	if err := sc.Read().Select(ctx, &rows, query, args...); err != nil {
		return nil, err
	}
	cards := make([]Card, len(rows))
	for i, r := range rows {
		cards[i] = r.card()
	}
	sortCards(cards, rows, f.Sort)
	return cards, nil
}

// sortCards orders cards (and their rows alike) in Go, where nil times and
// durations sort last on both engines.
func sortCards(cards []Card, rows []cardRow, by Sort) {
	keys := make(map[uuid.UUID]string, len(rows))
	for _, r := range rows {
		keys[r.ID] = r.NameKey
	}
	byName := func(a, b Card) int { return cmp.Compare(keys[a.ID], keys[b.ID]) }
	slices.SortStableFunc(cards, func(a, b Card) int {
		switch by {
		case SortRecentlyAdded:
			if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
				return c
			}
		case SortMostCooked:
			if c := cmp.Compare(b.TimesCooked, a.TimesCooked); c != 0 {
				return c
			}
		case SortName:
		case SortQuickest:
			if c := compareNilLast(a.TotalMin, b.TotalMin, func(x, y int) int { return cmp.Compare(x, y) }); c != 0 {
				return c
			}
		default: // SortRecentlyCooked
			if c := compareNilLast(a.LastCookedAt, b.LastCookedAt, func(x, y time.Time) int { return y.Compare(x) }); c != 0 {
				return c
			}
			if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
				return c
			}
		}
		return byName(a, b)
	})
}

func compareNilLast[T any](a, b *T, f func(T, T) int) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return 1
	case b == nil:
		return -1
	}
	return f(*a, *b)
}

// attachTags fills in the tag names of the cards.
func (s *Service) attachTags(ctx context.Context, sc store.Scoped, cards []Card) error {
	if len(cards) == 0 {
		return nil
	}
	var rows []struct {
		RecipeID uuid.UUID
		Name     string
	}
	if err := sc.Read().Select(ctx, &rows, `SELECT rt.recipe_id, t.name FROM recipe_tags rt
		JOIN tags t ON t.household_id = rt.household_id AND t.id = rt.tag_id
		WHERE rt.household_id = ? ORDER BY t.name_key`, sc.HouseholdID()); err != nil {
		return err
	}
	idx := make(map[uuid.UUID]int, len(cards))
	for i, c := range cards {
		idx[c.ID] = i
	}
	for _, r := range rows {
		if i, ok := idx[r.RecipeID]; ok {
			cards[i].Tags = append(cards[i].Tags, r.Name)
		}
	}
	return nil
}

// Count returns how many recipes the library holds.
func (s *Service) Count(ctx context.Context, sc store.Scoped) (int, error) {
	var n int
	err := sc.Read().QueryRow(ctx, `SELECT count(*) FROM recipes WHERE household_id = ? AND deleted_at IS NULL AND draft = ?`,
		sc.HouseholdID(), false).Scan(&n)
	return n, err
}

// Span marks the matched part of a text, as byte offsets.
type Span struct{ Start, End int }

// NameMatch is a recipe whose name contains the search.
type NameMatch struct {
	Card
	Span Span
}

// IngredientMatch is a recipe with an ingredient whose item name contains
// the search.
type IngredientMatch struct {
	Card
	ItemName string
	Amount   groceries.Amount
	Span     Span
}

// SearchResult holds name and ingredient matches. A recipe that matches by
// name is not repeated among the ingredient matches.
type SearchResult struct {
	Names       []NameMatch
	Ingredients []IngredientMatch
}

// searchLimit bounds each part of a search result.
const searchLimit = 50

// Search finds recipes whose name, or one of whose ingredients, contains
// the query, ignoring case. Name matches rank exact, then prefix, then word
// start, then anywhere; ingredient matches rank the same on the item name.
func (s *Service) Search(ctx context.Context, sc store.Scoped, viewer uuid.UUID, query string) (SearchResult, error) {
	key := groceries.NameKey(query)
	if key == "" {
		return SearchResult{}, nil
	}
	like := "%" + escapeLike(key) + "%"
	q := sc.Read()
	hh := sc.HouseholdID()

	var nameRows []cardRow
	if err := q.Select(ctx, &nameRows, cardSelect+` AND r.name_key LIKE ? ESCAPE '\'`, viewer, hh, false, like); err != nil {
		return SearchResult{}, err
	}
	var out SearchResult
	byName := map[uuid.UUID]bool{}
	for _, r := range nameRows {
		byName[r.ID] = true
		out.Names = append(out.Names, NameMatch{Card: r.card(), Span: matchSpan(r.Name, key)})
	}
	slices.SortStableFunc(out.Names, func(a, b NameMatch) int {
		if c := cmp.Compare(rank(a.Name, key), rank(b.Name, key)); c != 0 {
			return c
		}
		return cmp.Compare(groceries.NameKey(a.Name), groceries.NameKey(b.Name))
	})
	if len(out.Names) > searchLimit {
		out.Names = out.Names[:searchLimit]
	}

	var ingRows []struct {
		cardRow
		ItemName   string
		Quantity   *float64
		Unit       *string
		AmountText string
		Position   int
	}
	if err := q.Select(ctx, &ingRows, `SELECT c.*, i.name AS item_name, ri.quantity, ri.unit, ri.amount_text, ri.position
		FROM (`+cardSelect+`) c
		JOIN recipe_ingredients ri ON ri.household_id = ? AND ri.recipe_id = c.id
		JOIN items i ON i.household_id = ri.household_id AND i.id = ri.item_id
		WHERE i.name_key LIKE ? ESCAPE '\'
		ORDER BY ri.position`, viewer, hh, false, hh, like); err != nil {
		return SearchResult{}, err
	}
	seen := map[uuid.UUID]bool{}
	for _, r := range ingRows {
		if byName[r.ID] || seen[r.ID] {
			continue
		}
		seen[r.ID] = true
		out.Ingredients = append(out.Ingredients, IngredientMatch{
			Card: r.card(), ItemName: r.ItemName, Span: matchSpan(r.ItemName, key),
			Amount: groceries.Amount{Quantity: r.Quantity, Unit: deref(r.Unit), Text: r.AmountText},
		})
	}
	slices.SortStableFunc(out.Ingredients, func(a, b IngredientMatch) int {
		if c := cmp.Compare(rank(a.ItemName, key), rank(b.ItemName, key)); c != 0 {
			return c
		}
		return cmp.Compare(groceries.NameKey(a.Name), groceries.NameKey(b.Name))
	})
	if len(out.Ingredients) > searchLimit {
		out.Ingredients = out.Ingredients[:searchLimit]
	}

	cards := make([]Card, 0, len(out.Names)+len(out.Ingredients))
	for _, m := range out.Names {
		cards = append(cards, m.Card)
	}
	for _, m := range out.Ingredients {
		cards = append(cards, m.Card)
	}
	if err := s.attachTags(ctx, sc, cards); err != nil {
		return SearchResult{}, err
	}
	for i := range out.Names {
		out.Names[i].Tags = cards[i].Tags
	}
	for i := range out.Ingredients {
		out.Ingredients[i].Tags = cards[len(out.Names)+i].Tags
	}
	return out, nil
}

// rank orders how text matches key: exact, prefix, word start, anywhere.
func rank(text, key string) int {
	t := groceries.NameKey(text)
	switch {
	case t == key:
		return 0
	case strings.HasPrefix(t, key):
		return 1
	case strings.Contains(t, " "+key):
		return 2
	}
	return 3
}

// matchSpan finds the part of text whose groceries.NameKey is key, as byte
// offsets into text. Folding can change lengths ("ß" folds to "ss"), so it
// compares folded substrings instead of using offsets into the key. It
// returns an empty span when there is none.
func matchSpan(text, key string) Span {
	for i := 0; i < len(text); {
		for j := i; j < len(text); {
			_, size := utf8.DecodeRuneInString(text[j:])
			j += size
			sub := groceries.NameKey(text[i:j])
			if sub == key && strings.TrimSpace(text[i:j]) == text[i:j] {
				return Span{Start: i, End: j}
			}
			if len(sub) > len(key) || !strings.HasPrefix(key, sub) {
				break
			}
		}
		_, size := utf8.DecodeRuneInString(text[i:])
		i += size
	}
	return Span{}
}

// BySourceURL returns the slugs of the household's live recipes imported
// from any of urls, keyed by URL, so a site's search results can mark the
// recipes already in the library.
func (s *Service) BySourceURL(ctx context.Context, sc store.Scoped, urls []string) (map[string]string, error) {
	out := map[string]string{}
	if len(urls) == 0 {
		return out, nil
	}
	args := []any{sc.HouseholdID(), false}
	for _, u := range urls {
		args = append(args, u)
	}
	var rows []struct {
		Slug      string `db:"slug"`
		SourceURL string `db:"source_url"`
	}
	if err := sc.Read().Select(ctx, &rows, `SELECT slug, source_url FROM recipes
		WHERE household_id = ? AND deleted_at IS NULL AND draft = ? AND source_url IN (`+placeholders(len(urls))+`)
		ORDER BY created_at`, args...); err != nil {
		return nil, err
	}
	for _, r := range rows {
		if _, ok := out[r.SourceURL]; !ok {
			out[r.SourceURL] = r.Slug
		}
	}
	return out, nil
}
