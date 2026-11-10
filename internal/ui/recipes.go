package ui

import (
	"encoding/json"
	"hash/fnv"
	"strconv"

	"github.com/raphi011/mise/internal/images"
)

// tints are the palette colours recipe tiles without a photo take.
var tints = []string{"amber", "sky", "sand", "herb", "coral", "plum"}

// Tint picks a stable palette colour for a recipe without a photo.
func Tint(id string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	return tints[h.Sum32()%uint32(len(tints))]
}

// ImageURL is the URL of a stored recipe photo in a size ("thumb" or
// "full").
func ImageURL(hash, size string) string { return "/images/" + hash + "/" + size }

// RecipeHref is a recipe's detail page.
func RecipeHref(id string) string { return "/recipes/" + id }

// ---- Library ----

// LibraryView is the Recipes page.
type LibraryView struct {
	// Query is the encoded filter of the page ("" for none), for links and
	// the live stream.
	Query string
	// Total counts every recipe in the library.
	Total int
	// Matched counts the recipes the filter keeps; Cards holds the first
	// Shown of them.
	Matched int
	Shown   int
	Cards   []RecipeCard
	// Chips are the quick filters: Mine, Favourites, Under 30 min and the
	// pinned tags.
	Chips []FilterChip
	// People is the desktop "Added by" row.
	People []FilterChip
	// Tags is the desktop sidebar's tag list.
	Tags []FilterChip
	// Sort is the translated current sort.
	Sort string
	// Filtered is set when any filter applies.
	Filtered bool
	// Tonight is "Suggested for tonight", or nil.
	Tonight *TonightCard
	// Imports are the viewer's import jobs, newest first.
	Imports []ImportJobRow
}

// ImportJobRow is one of the viewer's imports above the library: running,
// ready to review or failed.
type ImportJobRow struct {
	Href  string
	Title string
	// State is "running", "ready" or "failed".
	State string
}

// RecipeCard is one recipe in the library grid.
type RecipeCard struct {
	ID        string
	Name      string
	Photo     string
	Meta      string
	Favourite bool
	// By is who added it; ByLabel is "you" or their name, translated.
	By      Viewer
	ByLabel string
}

// FilterChip is a filter that is a link toggling itself.
type FilterChip struct {
	Label string
	Href  string
	On    bool
	// Count, when not negative, is shown after the label.
	Count int
	// Color and Initial render an avatar (people only).
	Color   string
	Initial string
}

// FilterSheetView is the full filter sheet.
type FilterSheetView struct {
	Query  string
	People []FilterOption
	Tags   []FilterOption
	// Max is the total-time choice: "", "30" or "60".
	Max        string
	Favourites bool
	Stale      bool
	Never      bool
	Sorts      []Option
	Sort       string
	// Count is how many recipes the filter shows.
	Count int
}

// FilterOption is one checkbox of the filter sheet.
type FilterOption struct {
	Value string
	Label string
	// Initial is a person's avatar letter; Label may read "You".
	Initial string
	Color   string
	Checked bool
}

// ---- Search ----

// SearchView is the recipe search page and its results.
type SearchView struct {
	Query string
	// Total counts the library, for the empty state.
	Total       int
	Names       []SearchHit
	Ingredients []SearchHit
	// Status announces the result counts.
	Status string
}

// SearchHit is one result, with the matched part of Pre+Hit+Post marked.
type SearchHit struct {
	ID    string
	Name  string
	Photo string
	Pre   string
	Hit   string
	Post  string
	// Meta is the second line of a name match; Amount the amount of the
	// matched ingredient.
	Meta   string
	Amount string
}

// ---- Detail ----

// RecipeView is a recipe's detail page region.
type RecipeView struct {
	ID          string
	Name        string
	Description string
	Photo       string
	Tags        []string
	// Prep, Cook and Cooked are translated values, or "" when unknown.
	Prep   string
	Cook   string
	Cooked string
	// Servings is what the viewer scaled to; Base what the recipe makes.
	Servings int
	Base     int
	// Attribution is the translated "Added by … · … · last edited by …".
	Attribution string
	By          Viewer
	ListID      string
	ListName    string
	// ListStatus is the translated "N of M on <list>" line.
	ListStatus  string
	Ingredients []IngredientView
	Steps       [][]StepPart
	Notes       string
	// NotesBy and NotesWhen describe the last edit of the notes.
	NotesBy    *Viewer
	NotesWhen  string
	SourceURL  string
	SourceHost string
	Favourite  bool
	// Links are the recipes that go well with this one.
	Links []LinkView
	// Gone is set when the recipe was deleted while the page was open.
	Gone bool
	// Toast, when set, shows on the page as it loads.
	Toast *Toast
}

// LinkView is a linked recipe under "Goes well with".
type LinkView struct {
	ID    string
	Name  string
	Photo string
	// Meta is the translated total time, or "".
	Meta string
}

// CookHref is cook mode at the viewer's servings.
func (v RecipeView) CookHref() string {
	if v.Servings == v.Base {
		return CookHref(v.ID)
	}
	return CookHref(v.ID) + "?servings=" + strconv.Itoa(v.Servings)
}

// ServingsHref is the page at n servings, for the stepper without
// JavaScript.
func (v RecipeView) ServingsHref(n int) string {
	if n == v.Base {
		return RecipeHref(v.ID) + "#ingredients"
	}
	return RecipeHref(v.ID) + "?servings=" + strconv.Itoa(n) + "#ingredients"
}

// IngredientView is one ingredient row with its +/✓ toggle.
type IngredientView struct {
	ID       string
	Name     string
	Amount   string
	Optional bool
	On       bool
}

// StepPart is plain text, or an ingredient mention with its amount.
type StepPart struct {
	Text    string
	Mention bool
	Amount  string
}

// RecipeMenuView is the ⋯ menu of a recipe.
type RecipeMenuView struct {
	ID       string
	Name     string
	ByLabel  string
	ShareURL string
}

// DeleteRecipeView is the delete confirmation.
type DeleteRecipeView struct {
	ID   string
	Name string
	// Body is the translated consequences.
	Body string
}

// ---- Editor ----

// RecipeEditView is the recipe editor.
type RecipeEditView struct {
	// ID is empty for a new recipe.
	ID    string
	Form  Form
	Photo string
	Rows  []IngredientRow
	Steps []StepRow
	Tags  []TagChoice
	// Links are the recipes this one goes well with, as checkboxes;
	// LinkOptions the names the add field suggests.
	Links       []LinkChoice
	LinkOptions []string
	// Import is the import job a new recipe was opened from. ImportPhoto
	// shows the draft's photo, which saving downloads unless it is
	// removed or replaced.
	Import      string
	ImportPhoto bool
	// ImportCard offers import on a new recipe: "ai" when AI import is on,
	// "link" when only links import, empty for none.
	ImportCard string
}

// StepRow is one step of the editor: its text and its timer in minutes,
// as typed.
type StepRow struct {
	Text  string
	Timer string
}

// Action is where the editor posts.
func (v RecipeEditView) Action() string {
	if v.ID == "" {
		return "/recipes"
	}
	return "/recipes/" + v.ID + "/edit"
}

// Cancel is where Cancel leads.
func (v RecipeEditView) Cancel() string {
	if v.Import != "" {
		return ImportHref(v.Import)
	}
	if v.ID == "" {
		return "/recipes"
	}
	return RecipeHref(v.ID)
}

// IngredientRow is one ingredient row of the editor. Key identifies the
// row across reorders; Error is its translated message.
type IngredientRow struct {
	Key      string
	Amount   string
	Name     string
	Optional bool
	Error    string
}

// TagChoice is a tag the recipe can have.
// LinkChoice is a linked recipe in the editor.
type LinkChoice struct {
	ID   string
	Name string
}

type TagChoice struct {
	Name    string
	Checked bool
}

// StepError returns the message for step i ("steps.i").
func (v RecipeEditView) StepError(i int) string { return v.Form.Error("steps." + strconv.Itoa(i)) }

// TimerError returns the message for step i's timer ("steps.i.timer").
func (v RecipeEditView) TimerError(i int) string {
	return v.Form.Error("steps." + strconv.Itoa(i) + ".timer")
}

// withQuery appends an encoded query to path.
func withQuery(path, query string) string {
	if query == "" {
		return path
	}
	return path + "?" + query
}

// LibraryPageSize is how many cards the library shows at first and adds
// per "Show more". It fills whole rows at 2, 3, 4 and 6 columns. The plan's
// recipe pickers page their lists by it too.
const LibraryPageSize = 48

// libraryLive opens the Recipes page's live stream for a filter. It reads
// $shown, so Datastar reopens the stream when "Show more" raises it, and
// the first render has the longer page.
func libraryLive(query string) string {
	url := "/live?page=recipes"
	if query != "" {
		url += "&" + query
	}
	return "$shown && @get('" + url + "', {filterSignals: {include: /^shown$/}, retryMaxCount: 1000})"
}

// libraryMoreHref is "Show more" without JavaScript: the page with the
// next page of cards.
func libraryMoreHref(v LibraryView) string {
	q := "shown=" + itoa(v.Shown+LibraryPageSize)
	if v.Query != "" {
		q = v.Query + "&" + q
	}
	return "/recipes?" + q
}

// recipeLive opens a recipe page's live stream with the viewer's servings.
// It reads $servings, so Datastar reopens the stream (cancelling the old
// request) when the stepper changes it, and the first render is at the new
// servings.
func recipeLive(id string) string {
	return "$servings && @get('/live?page=recipe&id=" + id +
		"', {filterSignals: {include: /^servings$/}, retryMaxCount: 1000})"
}

// jsString quotes s as a JavaScript string literal for a Datastar
// expression.
func jsString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

// MaxServings bounds the servings stepper (the editor's limit too).
const MaxServings = 100

func boolValue(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func toggleURL(recipeID, ingredientID string) string {
	return "/recipes/" + recipeID + "/ingredients/" + ingredientID + "/toggle"
}

// MaxUploadBytes is the largest photo the editor accepts.
const MaxUploadBytes = images.MaxUploadBytes

// addRowAction asks the server to parse the typed ingredient line into a
// row appended to the editor.
const addRowAction = "$newing.trim() && @get('/recipes/editor/ingredient', {filterSignals: {include: /^newing$/}})"

// rowLabel names an ingredient row in button labels.
func rowLabel(r IngredientRow) string {
	if r.Name != "" {
		return r.Name
	}
	return r.Amount
}

// PhotoSrc is the photo the editor shows: the recipe's, else an import's.
func (v RecipeEditView) PhotoSrc() string {
	if v.Photo != "" {
		return ImageURL(v.Photo, "thumb")
	}
	if v.ImportPhoto {
		return ImportHref(v.Import) + "/photo"
	}
	return ""
}
