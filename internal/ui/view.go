package ui

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/a-h/templ"
)

// Viewer is the signed-in user as the layout shows them.
type Viewer struct {
	Name  string
	Color string
	Admin bool
}

// Initial returns the first letter of name, upper-cased, for avatars.
func Initial(name string) string {
	r, _ := utf8.DecodeRuneInString(strings.TrimSpace(name))
	if r == utf8.RuneError {
		return "?"
	}
	return string(unicode.ToUpper(r))
}

// avatarClass returns the CSS classes for an avatar of the given palette
// colour and size modifier ("" for the default 44 px).
func avatarClass(color, size string) string {
	c := "avatar avatar--" + color
	if size != "" {
		c += " avatar--" + size
	}
	return c
}

// Form carries submitted values and translated validation messages back
// into a re-rendered form.
type Form struct {
	Values map[string]string
	// Errors maps field names to translated messages.
	Errors map[string]string
	// Alert is a translated message about the form as a whole.
	Alert string
}

// Value returns the submitted value of a field.
func (f Form) Value(name string) string { return f.Values[name] }

// Error returns the message for a field, or "".
func (f Form) Error(name string) string { return f.Errors[name] }

// HasErrors reports whether any field or the form has a message.
func (f Form) HasErrors() bool { return len(f.Errors) > 0 || f.Alert != "" }

// Option is one choice in a select or radio group.
type Option struct {
	Value string
	// Label is already translated.
	Label string
	// Hint is an optional translated description.
	Hint string
}

// Field describes a labelled text input.
type Field struct {
	ID           string
	Name         string
	Label        string
	Type         string
	Autocomplete string
	// Hint is optional helper text under the input.
	Hint string
	// LabelNote is optional muted text after the label, e.g. "(optional)".
	LabelNote string
	Required  bool
	// Attrs are extra input attributes (e.g. Datastar).
	Attrs templ.Attributes
}
