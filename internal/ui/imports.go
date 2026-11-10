package ui

import (
	"context"

	"github.com/raphi011/mise/internal/i18n"
)

// Import tabs.
const (
	ImportTabLink = "link"
	ImportTabText = "text"
)

// ImportFormView is the import form (/recipes/import), and the failed
// screen when Failure is set.
type ImportFormView struct {
	Form Form
	// Tab is ImportTabLink or ImportTabText.
	Tab string
	// AI is set when AI import is on: the page offers pasted text and
	// names the model.
	AI    bool
	Model string
	// Admin links the model to its settings.
	Admin   bool
	Failure *ImportFailure
	// Replace is the failed job the form replaces once the new import
	// starts.
	Replace string
}

// ImportFailure is why an import failed, worded for the viewer.
type ImportFailure struct {
	Title string
	Body  string
	// Detail is the provider's message, under "See details".
	Detail string
}

// Import step states, as ImportStep.State.
const (
	StepRunning = "running"
	StepDone    = "done"
	StepFailed  = "failed"
)

// ImportStep is one entry of the assistant's activity list.
type ImportStep struct {
	Text  string
	Tool  string
	State string
}

// ImportRunningView is the running screen's live region.
type ImportRunningView struct {
	ID     string
	Source string
	Queued bool
	Steps  []ImportStep
	// Progress is a percentage for the progress bar.
	Progress int
}

// ImportReviewView is the review screen of a ready import.
type ImportReviewView struct {
	ID       string
	Name     string
	HasPhoto bool
	Meta     string
	Steps    []ImportStep
	// Elapsed is how long the import took ("8 s"), or empty.
	Elapsed string
	Rows    []ImportRow
	Summary string
	// NewItems counts the items saving creates.
	NewItems int
}

// ImportRow is one ingredient of a draft and its item.
type ImportRow struct {
	Index int
	Text  string
	Item  string
	// ItemID is the matched item, empty for a new item.
	ItemID string
	// Match is "matched", "check" or "new".
	Match    string
	Category string
}

// ImportPickView is the item picker for one review row.
type ImportPickView struct {
	ID    string
	Row   int
	Text  string
	Query string
	// Current is the row's item ID, empty for a new item.
	Current string
	// NewName is the name a new item would get.
	NewName string
	Items   []PickItem
	Form    Form
}

// PickItem is an existing item the picker offers.
type PickItem struct {
	ID       string
	Name     string
	Category string
}

// ImportHref is an import job's page.
func ImportHref(id string) string { return "/imports/" + id }

func importPickHref(id string, row int) string {
	return ImportHref(id) + "/items?row=" + itoa(row)
}

func matchClass(m string) string { return "match match--" + m }

func importSaveLabel(ctx context.Context, newItems int) string {
	switch newItems {
	case 0:
		return i18n.T(ctx, "import.review.save")
	case 1:
		return i18n.T(ctx, "import.review.saveNewOne")
	}
	return i18n.T(ctx, "import.review.saveNew", newItems)
}
