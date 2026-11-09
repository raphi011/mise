package ui

import (
	"fmt"
	"strconv"
)

// CookView is cook mode: a recipe's method one step per screen.
type CookView struct {
	ID   string
	Name string
	// Servings is what the detail page was scaled to; Base what the recipe
	// makes. Mentioned amounts are at Servings.
	Servings int
	Base     int
	// Step is the index of the step shown first.
	Step  int
	Steps []CookStep
}

// CookStep is one step of cook mode.
type CookStep struct {
	Parts []StepPart
	// TimerSeconds is the step's timer, or 0 for none.
	TimerSeconds int
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
	return withQuery(RecipeHref(v.ID), v.servingsQuery())
}

// StepHref is cook mode at step i (0-based), for links without
// JavaScript.
func (v CookView) StepHref(i int) string {
	q := "step=" + strconv.Itoa(i+1)
	if s := v.servingsQuery(); s != "" {
		q = s + "&" + q
	}
	return CookHref(v.ID) + "?" + q
}

// CookHref is a recipe's cook mode.
func CookHref(id string) string { return RecipeHref(id) + "/cook" }

// Clock formats a duration in seconds as m:ss, or h:mm:ss from an hour.
func Clock(seconds int) string {
	if seconds >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", seconds/3600, seconds/60%60, seconds%60)
	}
	return fmt.Sprintf("%d:%02d", seconds/60, seconds%60)
}
