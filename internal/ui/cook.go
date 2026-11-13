package ui

import (
	"fmt"
	"strconv"
)

// CookView is cook mode: a recipe's method one step per screen.
type CookView struct {
	ID string
	// Slug is the recipe's address, for links to its pages (see
	// recipes.Slugify); actions keep the ID.
	Slug string
	Name string
	// Servings is what the detail page was scaled to; Base what the recipe
	// makes. Mentioned amounts are at Servings.
	Servings int
	Base     int
	// Step is the index of the step shown first.
	Step  int
	Steps []Step
	// Ingredients are the recipe's ingredients at Servings, for the
	// ingredients sheet.
	Ingredients []IngredientView
}

// servingsQuery is the servings parameter, when they differ from the
// recipe's own.
func (v CookView) servingsQuery() string {
	if v.Servings == v.Base {
		return ""
	}
	return "servings=" + strconv.Itoa(v.Servings)
}

// RecipeBack is the detail page at the same servings.
func (v CookView) RecipeBack() string {
	return withQuery(RecipeHref(v.Slug), v.servingsQuery())
}

// StepHref is cook mode at step i (0-based), for links without
// JavaScript.
func (v CookView) StepHref(i int) string {
	q := "step=" + strconv.Itoa(i+1)
	if s := v.servingsQuery(); s != "" {
		q = s + "&" + q
	}
	return CookHref(v.Slug) + "?" + q
}

// CookHref is a recipe's cook mode.
func CookHref(slug string) string { return RecipeHref(slug) + "/cook" }

// Clock formats a duration in seconds as m:ss, or h:mm:ss from an hour.
func Clock(seconds int) string {
	if seconds >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", seconds/3600, seconds/60%60, seconds%60)
	}
	return fmt.Sprintf("%d:%02d", seconds/60, seconds%60)
}
