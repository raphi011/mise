package ui

import (
	"context"
	"strconv"
	"strings"

	"github.com/raphi011/mise/internal/i18n"
)

// listNameForm is the rename form of list l: the submitted form after an
// error, else the current name.
func listNameForm(v SettingsView, l ListTab) Form {
	if v.ListFormID == l.ID {
		return v.ListForm
	}
	return Form{Values: map[string]string{"name": l.Name}}
}

// newListForm is the new-list form.
func newListForm(v SettingsView) Form {
	if v.ListFormID == "" {
		return v.ListForm
	}
	return Form{}
}

// categoryForm is the edit form of category c.
func categoryForm(v CategoriesView, c CategoryRow) Form {
	if v.EditID == c.ID {
		return v.Edit
	}
	return Form{Values: map[string]string{"name": c.Name, "color": c.Color}}
}

// plural renders one for n == 1 and other, with n, otherwise.
func plural(ctx context.Context, n int, one, other i18n.Key) string {
	if n == 1 {
		return i18n.T(ctx, one)
	}
	return i18n.T(ctx, other, n)
}

// entryLabel is the accessible name of an entry's check-off button.
func entryLabel(ctx context.Context, e EntryRow) string {
	s := i18n.T(ctx, "groceries.checkOff", e.Name)
	if e.Amount != "" {
		s += ", " + e.Amount
	}
	if e.Recipe != "" {
		s += ", " + i18n.T(ctx, "groceries.fromRecipe", e.Recipe)
	}
	if e.By != nil {
		s += ", " + i18n.T(ctx, "groceries.addedByShort", e.By.Name)
	}
	return s
}

// GroceriesView is the list page.
type GroceriesView struct {
	List ListTab
	// Lists are all lists, for the tabs.
	Lists  []ListTab
	Open   int
	InCart int
	Groups []EntryGroup
	Cart   []EntryRow
	Usual  []ItemChip
	// Gone is set when the list was deleted while the page was open.
	Gone bool
	// AddForm carries the add field's value and error after a failed add
	// without JavaScript.
	AddForm Form
}

// ListTab is one shopping list in the tabs or settings.
type ListTab struct {
	ID   string
	Name string
	Open int
	// Current marks the list being shown.
	Current bool
}

// Href is the list page.
func (l ListTab) Href() string { return "/groceries/lists/" + l.ID }

// EntryGroup is one category's entries.
type EntryGroup struct {
	// Name is the category name, or the translated "Uncategorised".
	Name    string
	Color   string
	Entries []EntryRow
}

// EntryRow is one entry on the list or in the cart.
type EntryRow struct {
	ID     string
	Name   string
	Color  string
	Amount string
	Note   string
	// Recipe names the recipe the entry was added from, if any.
	Recipe string
	// By is set when someone other than the viewer added the entry.
	By *Viewer
}

// ItemChip is a catalogue item offered for adding.
type ItemChip struct {
	ID   string
	Name string
}

// SuggestView is the suggestion box under the add field.
type SuggestView struct {
	// Amount is the amount parsed from what was typed, if any.
	Amount  string
	Options []SuggestOption
	// Create offers to create a new item with this name ("" for none).
	Create string
}

// SuggestOption is one matching item.
type SuggestOption struct {
	ItemID string
	Name   string
	Color  string
	// Detail is the translated second line (category, "already on the
	// list", "you buy these often").
	Detail string
	OnList bool
}

// Toast is a short status message with an optional Undo.
type Toast struct {
	// Key distinguishes successive toasts so each one restarts its timer.
	Key     string
	Message string
	// UndoAction is the URL the Undo button posts to; empty for none.
	UndoAction string
	// UndoFields are posted with Undo.
	UndoFields map[string]string
}

// EntryEditView is the item edit sheet.
type EntryEditView struct {
	ID         string
	ItemName   string
	Color      string
	ListID     string
	Form       Form
	Categories []Option
	Lists      []Option
}

// ClearCartView is the clear-cart confirmation.
type ClearCartView struct {
	ListID   string
	ListName string
	Items    []string
}

// tileClass is the class of an item tile in a category colour.
func tileClass(color, size string) string {
	c := "tile"
	if color != "" {
		c += " avatar--" + color
	}
	if size != "" {
		c += " tile--" + size
	}
	return c
}

// postForm returns the Datastar action that posts a form as form data, so
// the same form works without JavaScript.
func postForm(url string) string {
	return "@post('" + url + "', {contentType: 'form'})"
}

// getAction returns the Datastar action that loads url into the page.
func getAction(url string) string {
	return "@get('" + url + "')"
}

func itoa(n int) string { return strconv.Itoa(n) }

// liveAction opens the page's live stream. Signals are not sent; the
// stream reopens (and refreshes) when a hidden tab becomes visible again.
func liveAction(url string) string {
	return "@get('" + url + "', {filterSignals: {include: /^$/}, retryMaxCount: 1000})"
}

// suggestAction asks for suggestions for the add field's text.
func suggestAction(listID string) string {
	return "@get('/groceries/lists/" + listID + "/suggest', {filterSignals: {include: /^add$/}})"
}

// addKeys moves the highlighted suggestion with the arrow keys and closes
// the box with Escape.
const addKeys = `evt.key === 'ArrowDown' && $_opts.length ? (evt.preventDefault(), $_sel = ($_sel + 1) % $_opts.length)
: evt.key === 'ArrowUp' && $_opts.length ? (evt.preventDefault(), $_sel = ($_sel + $_opts.length - 1) % $_opts.length)
: evt.key === 'Escape' ? ($_opts = [], $_sel = -1) : null`

// selectedExpr reports whether option i is highlighted.
func selectedExpr(i int) string { return "String($_sel === " + strconv.Itoa(i) + ")" }

func joinNonEmpty(sep string, parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}
