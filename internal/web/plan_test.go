package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

var (
	mealHrefRe   = regexp.MustCompile(`/plan/meals/([0-9a-f-]{36})\?`)
	ingredientRe = regexp.MustCompile(`name="ingredient" value="([0-9a-f-]{36})"`)
	placementsRe = regexp.MustCompile(`name="placements" value="([^"]+)"`)
)

// planClock is Monday 9 November 2026, midday in Vienna.
var planClock = time.Date(2026, 11, 9, 11, 0, 0, 0, time.UTC)

// datastarJSON posts a JSON payload the way a Datastar action without a
// form does (drops, swap picks).
func (b *browser) datastarJSON(path string, payload map[string]string) page {
	b.t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		b.t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(b.t.Context(), http.MethodPost, b.url(path), strings.NewReader(string(body)))
	if err != nil {
		b.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Datastar-Request", "true")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Origin", b.base)
	return b.do(req)
}

// datastarGet loads a page the way Datastar's @get does.
func (b *browser) datastarGet(path string) page {
	b.t.Helper()
	req, err := http.NewRequestWithContext(b.t.Context(), http.MethodGet, b.url(path), nil)
	if err != nil {
		b.t.Fatal(err)
	}
	req.Header.Set("Datastar-Request", "true")
	return b.do(req)
}

func mealIDs(t *testing.T, body string) []string {
	t.Helper()
	var ids []string
	seen := map[string]bool{}
	for _, m := range mealHrefRe.FindAllStringSubmatch(body, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			ids = append(ids, m[1])
		}
	}
	return ids
}

func contains(t *testing.T, what, body string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Errorf("%s has no %q:\n%.2000s", what, want, body)
		}
	}
}

// TestPlanWeekByTap is P3's done-criterion on a phone: a week planned by
// tapping, without JavaScript, with the ingredients picked in the add sheet
// on the list.
func TestPlanWeekByTap(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		s, srv := startTimedServer(t, db)
		s.clock = func() time.Time { return planClock }
		anna, _ := household(t, srv)
		list := firstList(t, anna)
		tofu := createRecipe(t, anna, stickyTofu())

		week := anna.get("/plan").expect(t, "plan", http.StatusOK)
		contains(t, "plan", week.body, "This week", "9 – 15 Nov · 0 of 7 days planned", "Plan a meal",
			"Haven’t cooked in a while", "Sticky tofu", "Never cooked")

		// The add sheet as a page: day, servings and ingredient chips.
		add := anna.get("/plan/add?recipe="+tofu).expect(t, "add sheet", http.StatusOK)
		contains(t, "add sheet", add.body, "Add to plan", "Also put on Shopping list", "Firm tofu", "Soy sauce",
			`value="2026-11-09" required checked`)
		ings := ingredientRe.FindAllStringSubmatch(add.body, -1)
		if len(ings) != 2 {
			t.Fatalf("ingredient chips: %v", ings)
		}

		// Next week keeps the weekday; the stepper reloads at the new value
		// with the picks.
		reload := anna.get("/plan/add?"+url.Values{
			"recipe": {tofu}, "form": {"1"}, "date": {"2026-11-10"}, "week": {"2026-11-16"},
			"servings": {"2"}, "set_servings": {"4"}, "ingredient": {ings[0][1]},
		}.Encode()).expect(t, "add sheet reload", http.StatusOK)
		contains(t, "add sheet reload", reload.body, "16 – 22 Nov", `value="2026-11-17" required checked`,
			`name="servings" value="4"`, `value="`+ings[0][1]+`" checked`)

		// Plan it for Tuesday for 4 with the tofu on the list.
		anna.post("/plan", url.Values{
			"recipe": {tofu}, "date": {"2026-11-10"}, "servings": {"4"}, "ingredient": {ings[0][1]}, "back": {"/plan?week=2026-11-09"},
		}).expect(t, "plan meal", http.StatusSeeOther, "/plan?week=2026-11-09")
		week = anna.get("/plan?week=2026-11-09").expect(t, "plan", http.StatusOK)
		contains(t, "plan", week.body, "1 of 7 days planned", "Sticky tofu on Tuesday, 10 November", "4 servings")
		lp := anna.get("/groceries/lists/"+list).expect(t, "list", http.StatusOK)
		contains(t, "list", lp.body, "Firm tofu", "800 g")
		if strings.Contains(lp.body, "Soy sauce") {
			t.Error("soy sauce was not picked but is on the list")
		}
		meal := mealIDs(t, week.body)[0]

		// The menu as pages; move to Thursday.
		menu := anna.get("/plan/meals/"+meal).expect(t, "menu", http.StatusOK)
		contains(t, "menu", menu.body, "Open recipe", "Move to…", "Change servings", "Swap recipe", "Remove from plan")
		if strings.Contains(menu.body, "I made this") {
			t.Error("a future meal offers I made this")
		}
		move := anna.get("/plan/meals/"+meal+"?view=move").expect(t, "move view", http.StatusOK)
		contains(t, "move view", move.body, "9 – 15 Nov", "Tuesday, 10 November, current day")
		anna.post("/plan/meals/"+meal+"/move", url.Values{"date": {"2026-11-12"}, "mode": {"swap"}}).
			expect(t, "move", http.StatusSeeOther, "/plan?week=2026-11-09")
		week = anna.get("/plan?week=2026-11-09").expect(t, "plan", http.StatusOK)
		contains(t, "plan after move", week.body, "Sticky tofu on Thursday, 12 November")

		// Servings: the stepper reloads the view, saving changes the meal.
		sv := anna.get("/plan/meals/"+meal+"?view=servings&set_servings=6").expect(t, "servings view", http.StatusOK)
		contains(t, "servings view", sv.body, `name="servings" value="6"`, "amounts already on the list stay as they are")
		anna.post("/plan/meals/"+meal+"/servings", url.Values{"servings": {"6"}}).expect(t, "servings", http.StatusSeeOther)
		contains(t, "plan after servings", anna.get("/plan?week=2026-11-09").body, "6 servings")
		lp = anna.get("/groceries/lists/" + list)
		contains(t, "list after servings", lp.body, "800 g")

		// Plan a meal on an empty day: the picker lists the library.
		pick := anna.get("/plan/pick?date=2026-11-13").expect(t, "picker", http.StatusOK)
		contains(t, "picker", pick.body, "Friday, 13 November", "All recipes", "/plan/add?back=")
		pick = anna.get("/plan/pick?date=2026-11-13&q=soy").expect(t, "picker search", http.StatusOK)
		contains(t, "picker search", pick.body, "Results", "Sticky tofu")

		// Remove, then Undo.
		anna.post("/plan/meals/"+meal+"/remove", nil).expect(t, "remove", http.StatusSeeOther)
		if strings.Contains(anna.get("/plan?week=2026-11-09").body, "Sticky tofu on") {
			t.Error("removed meal still planned")
		}
		anna.post("/plan/meals/"+meal+"/restore", nil).expect(t, "restore", http.StatusSeeOther)
		contains(t, "plan after restore", anna.get("/plan?week=2026-11-09").body, "Sticky tofu on Thursday")

		// The month shows it.
		month := anna.get("/plan/month?month=2026-11&date=2026-11-12").expect(t, "month", http.StatusOK)
		contains(t, "month", month.body, "November 2026", "1 meal planned", "Thursday, 12 November, Sticky tofu", `aria-current="date"`)

		// Unknown meals are not found.
		anna.get("/plan/meals/00000000-0000-7000-8000-000000000000").expect(t, "unknown meal", http.StatusNotFound)
	})
}

// TestPlanByDrag is P3's done-criterion on a desktop: meals and
// suggestions dragged onto days, with Undo, live for the other member.
func TestPlanByDrag(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		s, srv := startTimedServer(t, db)
		s.clock = func() time.Time { return planClock }
		anna, ben := household(t, srv)
		tofu := createRecipe(t, anna, stickyTofu())
		dalForm := stickyTofu()
		dalForm.Set("name", "Red lentil dal")
		dal := createRecipe(t, anna, dalForm)

		benWeek := ben.openLive("page=plan&week=2026-11-09")
		benWeek.next("ben week initial", liveDeadline, `id="plan-live"`)

		// A suggestion dropped on Monday plans it at the recipe's servings.
		res := anna.datastarJSON("/plan", map[string]string{"recipe": tofu, "date": "2026-11-09"}).expect(t, "drop suggestion", http.StatusOK)
		contains(t, "drop response", res.body, "Sticky tofu planned for Mon 9 Nov", "/unplan")
		benWeek.next("ben sees the drop", liveDeadline, "Sticky tofu on Monday, 9 November")
		anna.datastarJSON("/plan", map[string]string{"recipe": dal, "date": "2026-11-11"}).expect(t, "drop dal", http.StatusOK)
		benWeek.next("ben sees dal", liveDeadline, "Red lentil dal on Wednesday, 11 November")

		ids := mealIDs(t, anna.get("/plan?week=2026-11-09").body)
		if len(ids) != 2 {
			t.Fatalf("meals: %v", ids)
		}
		tofuMeal := ids[0]

		// Dropping on a free day moves; on a planned day swaps; Undo puts
		// both back.
		res = anna.datastarJSON("/plan/meals/"+tofuMeal+"/move", map[string]string{"date": "2026-11-10", "mode": "swap"}).
			expect(t, "drop on free day", http.StatusOK)
		contains(t, "move response", res.body, "Sticky tofu moved to Tue 10 Nov", "/plan/place")
		benWeek.next("ben sees the move", liveDeadline, "Sticky tofu on Tuesday, 10 November")
		res = anna.datastarJSON("/plan/meals/"+tofuMeal+"/move", map[string]string{"date": "2026-11-11", "mode": "swap"}).
			expect(t, "drop on planned day", http.StatusOK)
		contains(t, "swap response", res.body, "Sticky tofu swapped with Red lentil dal")
		benWeek.next("ben sees the swap", liveDeadline, "Sticky tofu on Wednesday, 11 November", "Red lentil dal on Tuesday, 10 November")
		undo := placementsRe.FindStringSubmatch(res.body)
		if undo == nil {
			t.Fatalf("swap has no Undo:\n%s", res.body)
		}
		anna.datastar(http.MethodPost, "/plan/place", url.Values{"placements": {undo[1]}}).expect(t, "undo swap", http.StatusOK)
		benWeek.next("ben sees the undo", liveDeadline, "Sticky tofu on Tuesday, 10 November", "Red lentil dal on Wednesday, 11 November")

		// "Keep both" adds the meal after the day's meals: the week shows
		// the first and +1.
		anna.datastar(http.MethodPost, "/plan/meals/"+tofuMeal+"/move", url.Values{"date": {"2026-11-11"}, "mode": {"move"}}).
			expect(t, "keep both", http.StatusOK)
		benWeek.next("ben sees +1", liveDeadline, "Red lentil dal on Wednesday, 11 November", "+1")

		// Swap the recipe from the menu, with Undo.
		menu := anna.datastarGet("/plan/meals/"+tofuMeal+"?view=swap&part=body").expect(t, "swap view", http.StatusOK)
		contains(t, "swap view", menu.body, `id="meal-menu-body"`, "Red lentil dal")
		res = anna.datastarJSON("/plan/meals/"+tofuMeal+"/replace", map[string]string{"recipe": dal}).expect(t, "replace", http.StatusOK)
		contains(t, "replace response", res.body, "Swapped for Red lentil dal", `name="recipe" value="`+tofu+`"`)
		anna.datastar(http.MethodPost, "/plan/meals/"+tofuMeal+"/replace", url.Values{"recipe": {tofu}}).expect(t, "undo replace", http.StatusOK)

		// Planning from the sheet can be undone with its shopping.
		list := firstList(t, anna)
		ings := ingredientRe.FindAllStringSubmatch(anna.get("/plan/add?recipe="+dal).body, -1)
		res = anna.datastar(http.MethodPost, "/plan", url.Values{
			"recipe": {dal}, "date": {"2026-11-14"}, "servings": {"2"}, "ingredient": {ings[0][1], ings[1][1]},
		}).expect(t, "plan with shopping", http.StatusOK)
		contains(t, "plan with shopping", res.body, "Red lentil dal planned for Sat 14 Nov · 2 items on Shopping list", `name="entries"`)
		entries := regexp.MustCompile(`name="entries" value="([^"]+)"`).FindStringSubmatch(res.body)
		unplan := regexp.MustCompile(`/plan/meals/([0-9a-f-]{36})/unplan`).FindStringSubmatch(res.body)
		anna.datastar(http.MethodPost, unplan[0], url.Values{"entries": {entries[1]}}).expect(t, "unplan", http.StatusOK)
		lp := anna.get("/groceries/lists/" + list).body
		if strings.Contains(lp, "Firm tofu") {
			t.Error("Undo of planning left its shopping on the list")
		}

		// Both recipes are planned this week and none today: no tonight
		// card.
		if strings.Contains(anna.get("/recipes").body, "Suggested for tonight") {
			t.Error("tonight suggests a recipe planned this week")
		}
	})
}

// TestCookedAndDeleteConfirm covers "I made this" and the planned-meals line
// of the delete confirmation.
func TestCookedAndDeleteConfirm(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		s, srv := startTimedServer(t, db)
		s.clock = func() time.Time { return planClock }
		anna, _ := household(t, srv)
		tofu := createRecipe(t, anna, stickyTofu())

		contains(t, "recipe menu", anna.get("/recipes/"+tofu+"/menu").body, "I made this")
		res := anna.datastar(http.MethodPost, "/recipes/"+tofu+"/cooked", url.Values{}).expect(t, "cooked", http.StatusOK)
		contains(t, "cooked response", res.body, "Marked as cooked today", "/cooked/")
		contains(t, "recipe page", anna.get("/recipes/"+tofu).body, "Once")
		undo := regexp.MustCompile(`/recipes/[0-9a-f-]{36}/cooked/[0-9a-f-]{36}/undo`).FindString(res.body)
		anna.post(undo, nil).expect(t, "undo cooked", http.StatusSeeOther, "/recipes/"+tofu)
		contains(t, "recipe page after undo", anna.get("/recipes/"+tofu).body, "Not yet")
		// Never cooked and not planned: tonight suggests it.
		contains(t, "library", anna.get("/recipes").body, "Suggested for tonight", "Never cooked")

		anna.post("/plan", url.Values{"recipe": {tofu}, "date": {"2026-11-10"}}).expect(t, "plan", http.StatusSeeOther)
		anna.post("/plan", url.Values{"recipe": {tofu}, "date": {"2026-11-02"}}).expect(t, "plan in the past", http.StatusSeeOther)
		del := anna.get("/recipes/"+tofu+"/delete").expect(t, "delete confirm", http.StatusOK)
		contains(t, "delete confirm", del.body, "It’s on the plan for Tue 10 Nov.")

		// Today's meal offers I made this, and is tonight's suggestion, live
		// on the library.
		library := anna.openLive("page=recipes")
		library.next("library initial", liveDeadline, `id="recipes-live"`)
		anna.post("/plan", url.Values{"recipe": {tofu}, "date": {"2026-11-09"}}).expect(t, "plan today", http.StatusSeeOther)
		library.next("library follows the plan", liveDeadline, "On tonight’s plan")
		contains(t, "library", anna.get("/recipes").body, "Suggested for tonight", "On tonight’s plan")
		today := mealIDs(t, anna.get("/plan/month?month=2026-11&date=2026-11-09").body)
		found := false
		for _, id := range today {
			if strings.Contains(anna.get("/plan/meals/"+id).body, "I made this") {
				found = true
			}
		}
		if !found {
			t.Error("no meal of today or the past offers I made this")
		}
	})
}
