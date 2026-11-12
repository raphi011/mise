package web

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/raphi011/mise/internal/groceries"
	"github.com/raphi011/mise/internal/recipes"
	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
	"github.com/raphi011/mise/internal/ui"
)

// TestCookMode is P4's cook-mode criterion as far as the server goes: a
// recipe with a step timer opens in cook mode one step per screen, at the
// servings of the detail page, and Done records the cook event with an
// Undo on the recipe page.
func TestCookMode(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		_, srv := startServer(t, db)
		anna, _ := household(t, srv)
		id := createRecipe(t, anna, stickyTofu())

		// The editor shows the saved timer in minutes.
		edit := anna.open("/recipes/"+id+"/edit").expect(t, "editor", http.StatusOK)
		if !strings.Contains(edit.body, `name="step_timer" type="text" inputmode="numeric" pattern="[0-9]*" autocomplete="off" value="10"`) {
			t.Errorf("editor lost the timer:\n%.6000s", edit.body)
		}

		page := anna.open("/recipes/"+id+"?servings=4").expect(t, "recipe", http.StatusOK)
		if !strings.Contains(page.body, `href="/recipes/sticky-tofu/cook?servings=4"`) {
			t.Fatalf("recipe page has no cook mode link at its servings:\n%.4000s", page.body)
		}

		cook := anna.open("/recipes/"+id+"/cook?servings=4").expect(t, "cook mode", http.StatusOK)
		for _, want := range []string{
			`data-theme="dark"`,
			"Step 1 of 2", "Step 2 of 2",
			`data-seconds="600"`, "Start 10:00 timer",
			"800 g", // the tofu at 4 servings, highlighted in step 1
			`href="/recipes/sticky-tofu/cook?servings=4&amp;step=2"`,
			`name="back" value="/recipes/sticky-tofu?servings=4"`,
		} {
			if !strings.Contains(cook.body, want) {
				t.Errorf("cook mode has no %q", want)
			}
		}
		if strings.Count(cook.body, "cook__step--off") != 1 || !strings.Contains(cook.body, `class="cook__step" id="cook-step-0"`) {
			t.Errorf("cook mode does not start at step 1:\n%.4000s", cook.body)
		}
		second := anna.open("/recipes/"+id+"/cook?step=2").expect(t, "step 2", http.StatusOK)
		if !strings.Contains(second.body, `class="cook__step" id="cook-step-1"`) {
			t.Error("?step=2 does not show the second step")
		}
		if over := anna.open("/recipes/"+id+"/cook?step=99").expect(t, "step 99", http.StatusOK); !strings.Contains(over.body, `class="cook__step" id="cook-step-1"`) {
			t.Error("a step past the end does not show the last one")
		}

		// Done records "I made this" and returns to the recipe, once with
		// the Undo toast.
		anna.post("/recipes/"+id+"/cooked", map[string][]string{"back": {"/recipes/" + id + "?servings=4"}}).
			expect(t, "done", http.StatusSeeOther, "/recipes/"+id+"?servings=4")
		page = anna.open("/recipes/"+id+"?servings=4").expect(t, "recipe after done", http.StatusOK)
		if !strings.Contains(page.body, "<dd>Once</dd>") || !strings.Contains(page.body, "/cooked/") || !strings.Contains(page.body, "toast__undo") {
			t.Errorf("recipe after done has no cook count or Undo: once=%v cooked=%v undo=%v", strings.Contains(page.body, "<dd>Once</dd>"), strings.Contains(page.body, "/cooked/"), strings.Contains(page.body, "toast__undo"))
		}
		page = anna.open("/recipes/"+id).expect(t, "recipe again", http.StatusOK)
		if strings.Contains(page.body, "toast__undo") {
			t.Error("the Undo toast shows again")
		}

		// A recipe without steps has no cook mode.
		bare := stickyTofu()
		bare["step"], bare["step_timer"] = nil, nil
		bareID := createRecipe(t, anna, bare)
		if strings.Contains(anna.open("/recipes/"+bareID).body, "/cook") {
			t.Error("recipe without steps links cook mode")
		}
		// The second "Sticky tofu" gets the next free slug.
		anna.get("/recipes/sticky-tofu-2/cook").expect(t, "cook without steps", http.StatusFound, "/recipes/sticky-tofu-2")
	})
}

// TestStepTimerValidation checks the editor's timer field.
func TestStepTimerValidation(t *testing.T) {
	_, srv := startServer(t, storetest.SQLite(t))
	anna, _ := household(t, srv)
	for _, minutes := range []string{"soon", "0", "99999999999999999"} {
		form := stickyTofu()
		form["step_timer"] = []string{minutes, ""}
		res := anna.post("/recipes", form).expect(t, "timer "+minutes, http.StatusUnprocessableEntity)
		if !strings.Contains(res.body, `id="step-timer-0"`) || !strings.Contains(res.body, `aria-invalid="true"`) {
			t.Errorf("timer %q: no error on the step's timer:\n%.5000s", minutes, res.body)
		}
	}
	// Mismatched step fields are a malformed form.
	form := stickyTofu()
	form["step_timer"] = []string{"10"}
	anna.post("/recipes", form).expect(t, "mismatched steps", http.StatusBadRequest)
}

// TestStepPartsMentionScope: the detail page highlights an ingredient once
// in the method, cook mode once per step, since it shows one step at a
// time.
func TestStepPartsMentionScope(t *testing.T) {
	rec := recipes.Recipe{
		Ingredients: []recipes.Ingredient{{ItemName: "Tofu"}, {ItemName: "Rice"}},
		Steps: []recipes.Step{
			{Text: "Press the tofu."},
			{Text: "Fry the tofu with the rice. Season the tofu."},
		},
	}
	amounts := []string{"400 g", "200 g"}
	mentions := func(steps [][]ui.StepPart) []string {
		var out []string
		for _, parts := range steps {
			var got []string
			for _, p := range parts {
				if p.Mention {
					got = append(got, p.Text+" "+p.Amount)
				}
			}
			out = append(out, strings.Join(got, ", "))
		}
		return out
	}
	for _, tc := range []struct {
		scope mentionScope
		want  []string
	}{
		{firstInMethod, []string{"tofu 400 g", "rice 200 g"}},
		{firstInStep, []string{"tofu 400 g", "tofu 400 g, rice 200 g"}},
	} {
		if got := mentions(stepParts(rec, amounts, groceries.Metric, tc.scope)); !slices.Equal(got, tc.want) {
			t.Errorf("scope %d: mentions %q, want %q", tc.scope, got, tc.want)
		}
	}
}
