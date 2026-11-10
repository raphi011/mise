package ui

import (
	"maps"
	"net/url"
	"slices"
	"strings"
)

// ---- Week and month ----

// PlanWeekView is the week page's live region.
type PlanWeekView struct {
	// Week is the first day of the week, 'YYYY-MM-DD'.
	Week string
	// Summary is the translated "9 – 15 Nov · 3 of 7 days planned".
	Summary   string
	PrevHref  string
	NextHref  string
	TodayHref string
	// Current is set when the week contains today.
	Current   bool
	MonthHref string
	Days      []PlanDay
	// Suggestions are "haven't cooked in a while".
	Suggestions []PlanSuggestion
	ListName    string
	ListHref    string
}

// PlanDay is one day of the week or month.
type PlanDay struct {
	// Date is 'YYYY-MM-DD'.
	Date string
	// Dow is the short weekday ("Mon"), Num the day of the month.
	Dow string
	Num int
	// Label is the translated full date ("Monday, 9 November").
	Label string
	Today bool
	Past  bool
	// InMonth is false for the neighbouring months' days in a month view.
	InMonth bool
	// Selected marks the month view's selected day; SelectHref selects it.
	Selected   bool
	SelectHref string
	Meals      []PlanMeal
	// AddHref plans a meal on the day.
	AddHref string
	// MoreHref shows every meal of the day (the "+N" marker).
	MoreHref string
}

// More is how many meals the week view leaves out.
func (d PlanDay) More() int {
	if len(d.Meals) < 2 {
		return 0
	}
	return len(d.Meals) - 1
}

// PlanMeal is a planned meal on a card or chip.
type PlanMeal struct {
	ID       string
	RecipeID string
	Name     string
	Photo    string
	// Time and Serves are translated; Time is "" when unknown.
	Time   string
	Serves string
	// Label is the card's accessible name.
	Label string
	// Href opens the meal's menu.
	Href string
}

// MenuHref opens the meal's menu.
func (m PlanMeal) MenuHref() string { return m.Href }

// PlanSuggestion is a recipe the plan suggests.
type PlanSuggestion struct {
	RecipeID string
	Name     string
	Photo    string
	// Last is the translated "Last: 5 weeks ago" or "Never cooked".
	Last string
	// AddHref opens the add-to-plan sheet for it.
	AddHref string
}

// PlanMonthView is the month page's live region.
type PlanMonthView struct {
	// Month is 'YYYY-MM'.
	Month string
	// Summary is the translated "12 meals planned".
	Summary   string
	PrevHref  string
	NextHref  string
	TodayHref string
	WeekHref  string
	// Current is set for the month that contains today.
	Current bool
	// Weekdays are the short weekday names, Initials their first letters,
	// in the household's week order.
	Weekdays []string
	Initials []string
	Weeks    [][]PlanDay
	// Selected is the phone's selected day.
	Selected PlanDay
}

// planLive opens the week or month page's live stream.
func planLive(query string) string {
	return liveAction("/live?page=plan&" + query)
}

// ---- Add to plan ----

// AddPlanView is the add-to-plan sheet.
type AddPlanView struct {
	RecipeID string
	Name     string
	// Meta is the translated "35 min" line, or "".
	Meta string
	Days PlanPicker
	// Servings is the stepper's value, Base the recipe's own.
	Servings int
	Base     int
	ListName string
	// Shop are the ingredients not on the list, offered as chips.
	Shop []ShopChip
	// Already is the translated "Already on it: …" line, or "".
	Already string
	// Also are the recipe's linked recipes, offered to plan on the same day.
	Also []ShopChip
	// Back is where Cancel and a plain form post lead.
	Back string
	Form Form
}

// ShopChip is an ingredient offered for the list.
type ShopChip struct {
	ID      string
	Name    string
	Checked bool
}

// PlanPicker is the day picker of the add and move sheets: one week with
// a day chosen.
type PlanPicker struct {
	// Date is the chosen day, 'YYYY-MM-DD'.
	Date  string
	Week  string
	Range string
	Prev  string
	Next  string
	Days  []PickDay
	// Notes and CTAs map each day to its note and button label.
	Notes map[string]string
	CTAs  map[string]string
}

// PickDay is one day of a PlanPicker.
type PickDay struct {
	Date    string
	Initial string
	Num     int
	Label   string
	// Planned is set when the day has meals; Tag is the move sheet's
	// translated "now", "swap" or "free".
	Planned bool
	Tag     string
	// Current is the moved meal's own day, which cannot be chosen.
	Current bool
}

// note returns the chosen day's note.
func (p PlanPicker) note() string { return p.Notes[p.Date] }

// cta returns the chosen day's button label.
func (p PlanPicker) cta() string { return p.CTAs[p.Date] }

// lookup is a Datastar expression that picks m[$signal], falling back to
// the value for the chosen day.
func (p PlanPicker) lookup(m map[string]string, signal string) string {
	var b strings.Builder
	b.WriteString("({")
	for i, k := range slices.Sorted(maps.Keys(m)) {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(jsString(k) + ":" + jsString(m[k]))
	}
	b.WriteString("})[$" + signal + "] || " + jsString(m[p.Date]))
	return b.String()
}

// plannedExpr reports, as a Datastar expression, whether the day in
// $signal has meals.
func (p PlanPicker) plannedExpr(signal string) string {
	var days []string
	for _, d := range p.Days {
		if d.Planned {
			days = append(days, jsString(d.Date))
		}
	}
	return "[" + strings.Join(days, ",") + "].includes($" + signal + ")"
}

// addDaysAction loads another week into the add sheet's day picker.
func addDaysAction(recipeID, week string) string {
	q := url.Values{"recipe": {recipeID}, "week": {week}}
	return "@get('/plan/add/days?" + q.Encode() + "', {filterSignals: {include: /^plandate$/}})"
}

// ---- Recipe picker ----

// PlanPickView is the recipe picker of "Plan a meal" and "Swap recipe".
type PlanPickView struct {
	// Date is the day to plan for (picking for a new meal).
	Date      string
	DayLabel  string
	Query     string
	Suggested []PickRecipe
	Recipes   []PickRecipe
	// Shown is the page size of Recipes without a query; Matched counts
	// the recipes there are in all.
	Shown, Matched int
	// Status announces the results.
	Status string
	Back   string
}

// PickRecipe is one recipe in the picker.
type PickRecipe struct {
	ID    string
	Name  string
	Photo string
	Meta  string
	// Href opens the add-to-plan sheet for it.
	Href string
}

// pickQuery is the picker's date and back, plus shown when it is set.
func (v PlanPickView) pickQuery(shown int) string {
	q := url.Values{"date": {v.Date}}
	if v.Back != "" {
		q.Set("back", v.Back)
	}
	if shown > 0 {
		q.Set("shown", itoa(shown))
	}
	return q.Encode()
}

// resultsAction re-renders the picker's list for the search signal.
func (v PlanPickView) resultsAction() string {
	return "@get('/plan/pick/results?" + v.pickQuery(0) + "', {filterSignals: {include: /^pickq$/}})"
}

// MoreHref is "Show more" without JavaScript: the picker page with the
// next page of recipes.
func (v PlanPickView) MoreHref() string { return "/plan/pick?" + v.pickQuery(v.Shown+LibraryPageSize) }

// moreAction is "Show more" with JavaScript: the list with the next page.
func (v PlanPickView) moreAction() string {
	return "@get('/plan/pick/results?" + v.pickQuery(v.Shown+LibraryPageSize) + "', {filterSignals: {include: /^pickq$/}})"
}

// ---- Meal menu ----

// MealMenuView is the menu of a planned meal.
type MealMenuView struct {
	ID       string
	RecipeID string
	Name     string
	Photo    string
	// DayLabel is the translated "Monday, 9 Nov"; Day the weekday alone.
	DayLabel string
	Day      string
	Servings int
	Base     int
	// View is "", "move", "servings" or "swap".
	View string
	// Cooked offers "I made this" (today's and past meals).
	Cooked bool
	Move   PlanPicker
	// Swap lists recipes to swap in; Query is the swap search. SwapShown
	// is the page size without a query; SwapMatched counts the recipes
	// there are in all.
	Swap                   []PickRecipe
	SwapShown, SwapMatched int
	Query                  string
	Back                   string
}

// MenuHref is the meal menu at a view.
func (v MealMenuView) MenuHref(view string) string {
	q := url.Values{}
	if view != "" {
		q.Set("view", view)
	}
	if v.Back != "" {
		q.Set("back", v.Back)
	}
	if len(q) == 0 {
		return "/plan/meals/" + v.ID
	}
	return "/plan/meals/" + v.ID + "?" + q.Encode()
}

// bodyHref is the meal menu at a view, for replacing the open sheet's
// body.
func (v MealMenuView) bodyHref(view string) string {
	h := v.MenuHref(view)
	if strings.Contains(h, "?") {
		return h + "&part=body"
	}
	return h + "?part=body"
}

// moveDaysAction loads another week into the move view.
func (v MealMenuView) moveDaysAction(week string) string {
	q := url.Values{"view": {"move"}, "week": {week}, "part": {"body"}}
	if v.Back != "" {
		q.Set("back", v.Back)
	}
	return "@get('/plan/meals/" + v.ID + "?" + q.Encode() + "', {filterSignals: {include: /^movedate$/}})"
}

// swapResultsAction searches recipes to swap in.
func (v MealMenuView) swapResultsAction() string {
	return "@get('/plan/meals/" + v.ID + "?view=swap&part=results', {filterSignals: {include: /^swapq$/}})"
}

// swapMoreQuery is the swap view with the next page of recipes.
func (v MealMenuView) swapMoreQuery() url.Values {
	q := url.Values{"view": {"swap"}, "shown": {itoa(v.SwapShown + LibraryPageSize)}}
	if v.Back != "" {
		q.Set("back", v.Back)
	}
	return q
}

// SwapMoreHref is the swap list's "Show more" without JavaScript.
func (v MealMenuView) SwapMoreHref() string {
	return "/plan/meals/" + v.ID + "?" + v.swapMoreQuery().Encode()
}

// swapMoreAction is the swap list's "Show more" with JavaScript.
func (v MealMenuView) swapMoreAction() string {
	q := v.swapMoreQuery()
	q.Set("part", "results")
	return "@get('/plan/meals/" + v.ID + "?" + q.Encode() + "', {filterSignals: {include: /^swapq$/}})"
}

// replaceAction swaps the meal's recipe for recipeID.
func (v MealMenuView) replaceAction(recipeID string) string {
	return "@post('/plan/meals/" + v.ID + "/replace', {payload: {recipe: " + jsString(recipeID) + ", back: " + jsString(v.Back) + "}})"
}

// ---- Tonight ----

// TonightCard is the library's "Suggested for tonight".
type TonightCard struct {
	RecipeID string
	Name     string
	Photo    string
	// Meta is the translated second line.
	Meta string
}

// ---- Drag and drop ----

// dragStart starts dragging a meal ("m:<id>") or a recipe ("r:<id>").
func dragStart(what string) string {
	return "$_drag = '" + what + "'; evt.dataTransfer.effectAllowed = 'move'; evt.dataTransfer.setData('text/plain', '" + what + "')"
}

// dragOver marks the day under a drag as the drop target.
func dragOver(date string) string {
	return "$_drag && (evt.dataTransfer.dropEffect = 'move', $_over = '" + date + "')"
}

// dragLeave unmarks the day when the drag leaves it for outside the day.
func dragLeave(date string) string {
	return "!el.contains(evt.relatedTarget) && $_over === '" + date + "' && ($_over = '')"
}

// dropOn moves or swaps the dragged meal onto the day, or plans the
// dragged recipe on it.
func dropOn(date string) string {
	d := "'" + date + "'"
	return "$_drag.startsWith('m:') ? @post('/plan/meals/' + $_drag.slice(2) + '/move', {payload: {date: " + d + ", mode: 'swap'}})" +
		" : $_drag.startsWith('r:') && @post('/plan', {payload: {recipe: $_drag.slice(2), date: " + d + "}});" +
		" $_drag = ''; $_over = ''"
}

// The plan sheets' signals, declared on the dialog and on the page that
// stands in for it when opened directly, so Datastar has them either way.

func addPlanSignals(v AddPlanView) string {
	return "{plandate: " + jsString(v.Days.Date) + ", planservings: " + itoa(v.Servings) + "}"
}

func planPickSignals(v PlanPickView) string { return "{pickq: " + jsString(v.Query) + "}" }

func mealMenuSignals(v MealMenuView) string {
	return "{movedate: '', mealservings: " + itoa(v.Servings) + ", swapq: ''}"
}
