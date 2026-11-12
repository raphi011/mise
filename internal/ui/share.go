package ui

import (
	"context"
	"strings"

	"github.com/raphi011/mise/internal/i18n"
)

// SharedSummary is the line a link preview shows under the title: the
// recipe's own description, else its times and servings.
func SharedSummary(ctx context.Context, description string, v SharedRecipeView) string {
	if d := strings.TrimSpace(description); d != "" {
		return d
	}
	var parts []string
	if v.Prep != "" {
		parts = append(parts, i18n.T(ctx, "recipes.prep")+" "+v.Prep)
	}
	if v.Cook != "" {
		parts = append(parts, i18n.T(ctx, "recipes.cook")+" "+v.Cook)
	}
	return strings.Join(append(parts, plural(ctx, v.Servings, "recipes.servingsOne", "recipes.servings")), " · ")
}
