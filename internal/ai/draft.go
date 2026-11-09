package ai

import (
	"context"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"golang.org/x/text/language"

	"github.com/raphi011/mise/internal/groceries"
	"github.com/raphi011/mise/internal/store"
)

// Match says how an imported ingredient relates to the household's items.
type Match string

// Matches, as the review screen shows them.
const (
	// MatchExact: an item with the same name.
	MatchExact Match = "matched"
	// MatchCheck: an item that probably fits ("Soy sauce" for "tamari or
	// soy sauce"); the user should look.
	MatchCheck Match = "check"
	// MatchNew: no item fits; saving creates ItemName.
	MatchNew Match = "new"
)

// Draft is an imported recipe waiting for review, stored as JSON on its
// import job.
type Draft struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	PrepMin     int               `json:"prep_min,omitempty"`
	CookMin     int               `json:"cook_min,omitempty"`
	Servings    int               `json:"servings,omitempty"`
	SourceURL   string            `json:"source_url,omitempty"`
	ImageURL    string            `json:"image_url,omitempty"`
	Ingredients []DraftIngredient `json:"ingredients"`
	Steps       []Step            `json:"steps"`
	// Tags are names of existing household tags.
	Tags []string `json:"tags,omitempty"`
}

// DraftIngredient is one ingredient with its proposed item.
type DraftIngredient struct {
	// Text is the line as the source wrote it.
	Text   string           `json:"text"`
	Amount groceries.Amount `json:"amount"`
	// ItemID is the matched item for MatchExact and MatchCheck.
	ItemID *uuid.UUID `json:"item_id,omitempty"`
	// ItemName is the matched item's name, or the new item's.
	ItemName string `json:"item_name"`
	Match    Match  `json:"match"`
	// Category is a new item's proposed category name, if any.
	Category string `json:"category,omitempty"`
}

// NewItems returns the distinct names of the items saving would create.
func (d Draft) NewItems() []string {
	seen := map[string]bool{}
	var out []string
	for _, ing := range d.Ingredients {
		if ing.Match == MatchNew && !seen[groceries.NameKey(ing.ItemName)] {
			seen[groceries.NameKey(ing.ItemName)] = true
			out = append(out, ing.ItemName)
		}
	}
	return out
}

// Draft limits, matching the recipe editor's.
const (
	maxDraftName        = 120
	maxDraftDescription = 500
	maxDraftURL         = 2000
	maxDraftStep        = 2000
	maxDraftSteps       = 60
	maxDraftIngredients = 80
	maxDraftTags        = 20
	maxItemName         = 80
	maxDraftServings    = 100
	maxDraftMinutes     = 24 * 60
)

// draftFromRecipe matches a source recipe's ingredients and keywords
// against the household and caps everything to the editor's limits. lang
// is the language the amounts are written in.
func (s *Service) draftFromRecipe(ctx context.Context, sc store.Scoped, r Recipe, lang language.Tag) (Draft, error) {
	d := Draft{
		Name:        truncate(r.Name, maxDraftName),
		Description: truncate(r.Description, maxDraftDescription),
		PrepMin:     clamp(r.PrepMin, maxDraftMinutes),
		CookMin:     clamp(r.CookMin, maxDraftMinutes),
		Servings:    clamp(r.Servings, maxDraftServings),
		Ingredients: []DraftIngredient{},
		Steps:       []Step{},
	}
	if len(r.SourceURL) <= maxDraftURL {
		d.SourceURL = r.SourceURL
	}
	if len(r.ImageURL) <= maxDraftURL {
		d.ImageURL = r.ImageURL
	}
	for _, line := range r.Ingredients {
		if len(d.Ingredients) == maxDraftIngredients {
			break
		}
		ing, err := s.matchIngredient(ctx, sc, line, lang)
		if err != nil {
			return Draft{}, err
		}
		if ing.ItemName != "" {
			d.Ingredients = append(d.Ingredients, ing)
		}
	}
	for _, st := range r.Steps {
		if len(d.Steps) == maxDraftSteps {
			break
		}
		if st.Text = truncate(st.Text, maxDraftStep); st.Text != "" {
			st.TimerSeconds = clamp(st.TimerSeconds, maxDraftMinutes*60)
			d.Steps = append(d.Steps, st)
		}
	}
	tags, err := s.matchTags(ctx, sc, r.Keywords)
	if err != nil {
		return Draft{}, err
	}
	d.Tags = tags
	return d, nil
}

// matchIngredient parses an ingredient line and proposes an item.
func (s *Service) matchIngredient(ctx context.Context, sc store.Scoped, line string, lang language.Tag) (DraftIngredient, error) {
	line = truncate(line, 300)
	amount, name := groceries.ParseEntry(line, lang)
	name = itemName(name)
	ing := DraftIngredient{Text: line, Amount: amount}
	if name == "" {
		return ing, nil
	}
	found, err := s.groceries.SearchItems(ctx, sc, name, 5)
	if err != nil {
		return ing, err
	}
	if len(found) > 0 && found[0].Match == groceries.MatchExact {
		id := found[0].Item.ID
		ing.ItemID, ing.ItemName, ing.Match = &id, found[0].Item.Name, MatchExact
		return ing, nil
	}
	within, err := s.groceries.ItemsWithin(ctx, sc, name, 1)
	if err != nil {
		return ing, err
	}
	if len(within) > 0 {
		id := within[0].ID
		ing.ItemID, ing.ItemName, ing.Match = &id, within[0].Name, MatchCheck
		return ing, nil
	}
	if len(found) > 0 && found[0].Match != groceries.MatchContains {
		id := found[0].Item.ID
		ing.ItemID, ing.ItemName, ing.Match = &id, found[0].Item.Name, MatchCheck
		return ing, nil
	}
	ing.ItemName, ing.Match = groceries.CleanName(name), MatchNew
	return ing, nil
}

var parenthetical = regexp.MustCompile(`\s*\([^)]*\)`)

// itemName turns what follows an ingredient's amount into an item name:
// "onion, finely chopped (optional)" is "onion".
func itemName(name string) string {
	name = parenthetical.ReplaceAllString(name, "")
	if i := strings.IndexAny(name, ",;"); i >= 0 {
		name = name[:i]
	}
	name = strings.TrimSpace(strings.Trim(strings.TrimSpace(name), "*-–•"))
	return truncate(name, maxItemName)
}

// matchTags keeps the keywords that name existing tags, as those tags.
func (s *Service) matchTags(ctx context.Context, sc store.Scoped, keywords []string) ([]string, error) {
	if len(keywords) == 0 {
		return nil, nil
	}
	tags, err := s.recipes.Tags(ctx, sc)
	if err != nil {
		return nil, err
	}
	byKey := map[string]string{}
	for _, t := range tags {
		byKey[groceries.NameKey(t.Name)] = t.Name
	}
	seen := map[string]bool{}
	var out []string
	for _, k := range keywords {
		name, ok := byKey[groceries.NameKey(k)]
		if ok && !seen[name] && len(out) < maxDraftTags {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out, nil
}

func truncate(s string, runes int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= runes {
		return s
	}
	return strings.TrimSpace(string([]rune(s)[:runes]))
}

func clamp(n, max int) int {
	if n < 0 || n > max {
		return 0
	}
	return n
}
