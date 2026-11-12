package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

var nutritionItemRe = regexp.MustCompile(`/items/([0-9a-f-]{36})/nutrition\?next=`)

// nutritionItems returns the item IDs the "What's missing" list links to, in
// order and without repeats.
func nutritionItems(body string) []string {
	var ids []string
	seen := map[string]bool{}
	for _, m := range nutritionItemRe.FindAllStringSubmatch(body, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			ids = append(ids, m[1])
		}
	}
	return ids
}

// TestNutrition covers the nutrition estimate end to end: the recipe page
// names what is missing, the item editor validates and saves with and
// without JavaScript, the estimate follows live and reaches the public page,
// and the editor never opens another household's item.
func TestNutrition(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		_, srv := startTimedServer(t, db)
		anna, ben := household(t, srv)
		id := createRecipe(t, anna, stickyTofu())
		page := "/recipes/" + id

		// Nothing is known yet: no numbers, a way to add them.
		body := anna.get(page).expect(t, "recipe", http.StatusOK).body
		for _, want := range []string{"Not enough data yet", "None of the 2 ingredients has nutrition yet.", "Add nutrition"} {
			if !strings.Contains(body, want) {
				t.Errorf("recipe page has no %q", want)
			}
		}
		if strings.Contains(body, "nutrition__kcal") {
			t.Error("numbers shown without data")
		}
		items := nutritionItems(body)
		if len(items) != 2 {
			t.Fatalf("missing list links %v", items)
		}
		tofu, soy := items[0], items[1]

		editor := "/items/" + tofu + "/nutrition"
		form := anna.get(editor+"?next="+url.QueryEscape(page)).expect(t, "editor", http.StatusOK).body
		for _, want := range []string{"Nutrition of Firm tofu", `name="kcal"`, `name="salt"`, `name="per100ml"`, `name="weight_clove"`, "Used in 1 recipe."} {
			if !strings.Contains(form, want) {
				t.Errorf("editor has no %q", want)
			}
		}

		// Invalid input comes back with the values and the errors, saved nowhere.
		bad := anna.post(editor, url.Values{"next": {page}, "kcal": {""}, "protein": {"lots"}}).
			expect(t, "invalid", http.StatusUnprocessableEntity).body
		for _, want := range []string{"Enter a number, like 12.5.", `value="lots"`, "Fill this in."} {
			if !strings.Contains(bad, want) {
				t.Errorf("invalid editor has no %q:\n%.300s", want, between(bad, "nutrition-form__grid"))
			}
		}
		part := anna.post(editor, url.Values{"next": {page}, "kcal": {"90"}, "fat": {"3"}, "saturated_fat": {"9"}}).
			expect(t, "saturated over fat", http.StatusUnprocessableEntity).body
		if !strings.Contains(part, "This is more than the total it is part of.") {
			t.Errorf("no error on saturated fat:\n%.300s", between(part, "nutrition-form__grid"))
		}
		anna.post(editor, url.Values{"next": {page}, "kcal": {"10"}, "protein": {"14"}, "fat": {"9"}, "carbs": {"38"}, "fibre": {"62"}}).
			expect(t, "macros over 100 g", http.StatusUnprocessableEntity)

		// Ben has the recipe open; Anna saves without JavaScript.
		live := ben.openLive("page=recipe&id=" + id + "&datastar=" + url.QueryEscape(`{"servings":2}`))
		live.next("ben recipe initial", liveDeadline, "Not enough data yet")
		anna.post(editor, url.Values{"next": {page}, "kcal": {"144"}, "protein": {"17,3"}, "fat": {"8.7"}, "saturated_fat": {"1.3"},
			"carbs": {"2.8"}, "fibre": {"2.3"}, "salt": {"0.04"}}).expect(t, "save tofu", http.StatusSeeOther, page)
		live.next("ben sees the estimate", liveDeadline, "Based on 1 of 2 ingredients")

		// Half the ingredients are counted: numbers show, the rest is listed.
		body = anna.get(page).expect(t, "recipe", http.StatusOK).body
		for _, want := range []string{"nutrition__kcal", "Based on 1 of 2 ingredients", "What’s missing (1)", "Soy sauce", "No nutrition values for this item.",
			"Saturated fat", "ingredient__uncounted", "per serving · 2 servings"} {
			if !strings.Contains(body, want) {
				t.Errorf("estimate has no %q", want)
			}
		}
		// 400 g tofu at 144 kcal per 100 g for 2 servings.
		if !strings.Contains(body, `nutrition__kcal">290<`) {
			t.Errorf("energy is not 290 kcal:\n%.200s", between(body, "nutrition__kcal"))
		}

		// The editor shows what was saved, in the viewer's decimal notation.
		saved := anna.get(editor).body
		for _, want := range []string{`value="144"`, `value="17.3"`, `value="0.04"`} {
			if !strings.Contains(saved, want) {
				t.Errorf("saved editor has no %q", want)
			}
		}

		// Weights turn "3 tbsp" into grams: soy sauce counts once it has a density.
		anna.post("/items/"+soy+"/nutrition", url.Values{"next": {page}, "kcal": {"53"}, "protein": {"8,1"}, "per100ml": {"115"}}).
			expect(t, "save soy sauce", http.StatusSeeOther, page)
		body = anna.get(page).body
		if strings.Contains(body, "Based on") || strings.Contains(body, "What’s missing") {
			t.Errorf("all ingredients counted, but the page shows coverage or missing:\n%.300s", between(body, "nutrition__card"))
		}

		// The public page shows the estimate, and nothing to fix.
		anna.post(page+"/share", nil).expect(t, "share", http.StatusSeeOther)
		link := publicLink(t, anna.get(page+"/menu").body)
		_, pub := newBrowser(t, srv.URL).rawGet(link)
		for _, want := range []string{"nutrition__kcal", "Nutrition"} {
			if !strings.Contains(pub, want) {
				t.Errorf("public page has no %q", want)
			}
		}
		for _, leak := range []string{"/items/", "Add nutrition", "What’s missing"} {
			if strings.Contains(pub, leak) {
				t.Errorf("public page shows %q", leak)
			}
		}

		// Clearing removes the values again; the next goes only to local paths.
		anna.post(editor+"/clear", url.Values{"next": {"https://example.com/"}}).expect(t, "clear", http.StatusSeeOther, "/recipes")
		if body := anna.get(page).body; !strings.Contains(body, "Based on 1 of 2 ingredients") {
			t.Errorf("clearing tofu did not leave 1 of 2:\n%.300s", between(body, "nutrition__card"))
		}

		// Unknown and malformed items are not found; the entry sheet links in.
		anna.get("/items/"+uuid.NewString()+"/nutrition").expect(t, "unknown item", http.StatusNotFound)
		anna.get("/items/nope/nutrition").expect(t, "bad id", http.StatusNotFound)
		anna.post("/items/"+uuid.NewString()+"/nutrition", url.Values{"kcal": {"1"}}).expect(t, "save unknown", http.StatusNotFound)
	})
}

// between returns a window of s after the first occurrence of marker, for
// failure messages.
func between(s, marker string) string {
	i := strings.Index(s, marker)
	if i < 0 {
		return "(missing " + marker + ")"
	}
	return s[i:min(len(s), i+400)]
}
