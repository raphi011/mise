package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/starfederation/datastar-go/datastar"

	"github.com/raphi011/mise/internal/auth"
	"github.com/raphi011/mise/internal/groceries"
	"github.com/raphi011/mise/internal/i18n"
	"github.com/raphi011/mise/internal/live"
	"github.com/raphi011/mise/internal/plan"
	"github.com/raphi011/mise/internal/recipes"
	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/ui"
)

// staleShown is how many "haven't cooked in a while" picks the plan shows.
const staleShown = 3

// ---- Dates ----

var weekdayKeys = [7]i18n.Key{
	"date.weekday.0", "date.weekday.1", "date.weekday.2", "date.weekday.3", "date.weekday.4", "date.weekday.5", "date.weekday.6",
}

var weekdayShortKeys = [7]i18n.Key{
	"date.weekdayShort.0", "date.weekdayShort.1", "date.weekdayShort.2", "date.weekdayShort.3",
	"date.weekdayShort.4", "date.weekdayShort.5", "date.weekdayShort.6",
}

var weekdayInitialKeys = [7]i18n.Key{
	"date.weekdayInitial.0", "date.weekdayInitial.1", "date.weekdayInitial.2", "date.weekdayInitial.3",
	"date.weekdayInitial.4", "date.weekdayInitial.5", "date.weekdayInitial.6",
}

func weekday(ctx context.Context, d store.Date) string { return i18n.T(ctx, weekdayKeys[d.Weekday()]) }

// fullDate is "Monday, 9 November".
func fullDate(ctx context.Context, d store.Date) string {
	return i18n.T(ctx, "date.full", weekday(ctx, d), d.Day, i18n.T(ctx, monthKeys[d.Month-1]))
}

// shortDate is "Mon 9 Nov".
func shortDate(ctx context.Context, d store.Date) string {
	return i18n.T(ctx, "date.short", i18n.T(ctx, weekdayShortKeys[d.Weekday()]), d.Day, i18n.T(ctx, shortMonthKeys[d.Month-1]))
}

// weekRange is "9 – 15 Nov" or "30 Nov – 6 Dec" for the week from start.
func weekRange(ctx context.Context, start store.Date) string {
	end := start.AddDays(6)
	if start.Month == end.Month {
		return i18n.T(ctx, "date.rangeSameMonth", start.Day, end.Day, i18n.T(ctx, shortMonthKeys[end.Month-1]))
	}
	return i18n.T(ctx, "date.range",
		i18n.T(ctx, "date.dayMonth", start.Day, i18n.T(ctx, shortMonthKeys[start.Month-1])),
		i18n.T(ctx, "date.dayMonth", end.Day, i18n.T(ctx, shortMonthKeys[end.Month-1])))
}

// lastCooked is "Last: 5 weeks ago", or "Never cooked".
func lastCooked(ctx context.Context, now time.Time, at *time.Time) string {
	if at == nil {
		return i18n.T(ctx, "plan.last.never")
	}
	days := int(now.Sub(*at).Hours() / 24)
	switch {
	case days < 14:
		return i18n.T(ctx, "plan.last.days", max(days, 0))
	case days < 63:
		return i18n.T(ctx, "plan.last.weeks", days/7)
	case days < 365:
		return i18n.T(ctx, "plan.last.months", days/30)
	}
	return i18n.T(ctx, "plan.last.year")
}

// dateParam reads a 'YYYY-MM-DD' value; the zero Date when missing or
// invalid.
func dateParam(v string) store.Date {
	d, err := store.ParseDate(v)
	if err != nil {
		return store.Date{}
	}
	return d
}

// monthParam reads 'YYYY-MM'.
func monthParam(v string) (int, time.Month, bool) {
	t, err := time.Parse("2006-01", v)
	if err != nil {
		return 0, 0, false
	}
	return t.Year(), t.Month(), true
}

func monthString(year int, month time.Month) string {
	return fmt.Sprintf("%04d-%02d", year, int(month))
}

// localPath returns raw when it is a path on this site, else fallback.
func localPath(raw, fallback string) string {
	if raw == "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || strings.HasPrefix(raw, "/\\") {
		return fallback
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" {
		return fallback
	}
	return raw
}

func weekHref(week store.Date) string { return "/plan?week=" + week.String() }

// weekBack is the week page of the week that contains d, where a plain
// form post returns to. Without the calendar it links d itself, which the
// week page also shows.
func (s *Server) weekBack(ctx context.Context, sc store.Scoped, d store.Date) string {
	cal, err := s.plan.Calendar(ctx, sc)
	if err != nil {
		return weekHref(d)
	}
	return weekHref(cal.WeekOf(d))
}

func monthHref(year int, month time.Month, date store.Date) string {
	h := "/plan/month?month=" + monthString(year, month)
	if !date.IsZero() {
		h += "&date=" + date.String()
	}
	return h
}

func withParams(path string, q url.Values) string {
	if len(q) == 0 {
		return path
	}
	return path + "?" + q.Encode()
}

// pickHref opens the recipe picker for a day.
func pickHref(date store.Date, back string) string {
	return withParams("/plan/pick", url.Values{"date": {date.String()}, "back": {back}})
}

// addHref opens the add-to-plan sheet on date or, when date is zero, in
// week (today's weekday; today when week is zero too).
func addHref(recipeID uuid.UUID, date, week store.Date, back string) string {
	q := url.Values{"recipe": {recipeID.String()}}
	if !date.IsZero() {
		q.Set("date", date.String())
	} else if !week.IsZero() {
		q.Set("week", week.String())
	}
	if back != "" {
		q.Set("back", back)
	}
	return withParams("/plan/add", q)
}

// dayHref opens the day sheet with all of a day's meals.
func dayHref(date store.Date, back string) string {
	return withParams("/plan/day", url.Values{"date": {date.String()}, "back": {back}})
}

func mealHref(id uuid.UUID, back string) string {
	return withParams("/plan/meals/"+id.String(), url.Values{"back": {back}})
}

// ---- Week and month ----

func (s *Server) planMeal(ctx context.Context, m plan.Meal, back string) ui.PlanMeal {
	pm := ui.PlanMeal{
		ID: m.ID.String(), RecipeID: m.Recipe.ID.String(), Name: m.Recipe.Name, Photo: m.Recipe.Photo,
		Time: minutes(ctx, m.Recipe.TotalMin), Serves: servingsText(ctx, m.Servings), Href: mealHref(m.ID, back),
	}
	pm.Label = i18n.T(ctx, "plan.mealLabel", m.Recipe.Name, fullDate(ctx, m.Date))
	return pm
}

func (s *Server) planDay(ctx context.Context, d plan.Day, today store.Date, back string) ui.PlanDay {
	pd := ui.PlanDay{
		Date: d.Date.String(), Dow: i18n.T(ctx, weekdayShortKeys[d.Date.Weekday()]), Num: d.Date.Day,
		Label: fullDate(ctx, d.Date), Today: d.Date == today, InMonth: true,
		AddHref: pickHref(d.Date, back), DayHref: dayHref(d.Date, back),
	}
	for _, m := range d.Meals {
		pd.Meals = append(pd.Meals, s.planMeal(ctx, m, back))
	}
	return pd
}

// planWeekView loads the week that contains date for the viewer.
func (s *Server) planWeekView(ctx context.Context, sc store.Scoped, date store.Date) (ui.PlanWeekView, error) {
	w, err := s.plan.Week(ctx, sc, date)
	if err != nil {
		return ui.PlanWeekView{}, err
	}
	back := weekHref(w.Start)
	cal, err := s.plan.Calendar(ctx, sc)
	if err != nil {
		return ui.PlanWeekView{}, err
	}
	thisWeek := cal.WeekOf(w.Today)
	// The month link shows today's month for this week, else the month
	// the week starts in: the same for the page and its live refresh.
	month := w.Start
	if w.Start == thisWeek {
		month = w.Today
	}
	v := ui.PlanWeekView{
		Week: w.Start.String(), PrevHref: weekHref(w.Start.AddDays(-7)), NextHref: weekHref(w.Start.AddDays(7)),
		TodayHref: "/plan", Current: w.Start == thisWeek,
		MonthHref: monthHref(month.Year, month.Month, store.Date{}),
	}
	planned := 0
	for _, d := range w.Days {
		if len(d.Meals) > 0 {
			planned++
		}
		v.Days = append(v.Days, s.planDay(ctx, d, w.Today, back))
	}
	v.Summary = weekRange(ctx, w.Start) + " · " + i18n.T(ctx, "plan.planned", planned)
	sugg, err := s.plan.Suggestions(ctx, sc, w.Start, w.Start.AddDays(7), staleShown)
	if err != nil {
		return ui.PlanWeekView{}, err
	}
	now := s.now()
	for _, sg := range sugg {
		v.Suggestions = append(v.Suggestions, ui.PlanSuggestion{
			RecipeID: sg.Recipe.ID.String(), Name: sg.Recipe.Name, Photo: sg.Recipe.Photo,
			Last: lastCooked(ctx, now, sg.LastCookedAt), AddHref: addHref(sg.Recipe.ID, store.Date{}, w.Start, back),
		})
	}
	list, err := s.groceries.FirstList(ctx, sc)
	switch {
	case err == nil:
		v.ListName, v.ListHref = list.Name, "/groceries/lists/"+list.ID.String()
	case !errors.Is(err, groceries.ErrNotFound):
		return ui.PlanWeekView{}, err
	}
	return v, nil
}

// weekTitle is "This week", "Next week", "Last week" or the range.
func weekTitle(ctx context.Context, start, thisWeek store.Date) string {
	switch start {
	case thisWeek:
		return i18n.T(ctx, "page.plan.title")
	case thisWeek.AddDays(7):
		return i18n.T(ctx, "plan.nextWeekTitle")
	case thisWeek.AddDays(-7):
		return i18n.T(ctx, "plan.lastWeekTitle")
	}
	return i18n.T(ctx, "plan.weekOf", i18n.T(ctx, "date.dayMonth", start.Day, i18n.T(ctx, shortMonthKeys[start.Month-1])))
}

func (s *Server) planPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	cal, err := s.plan.Calendar(ctx, sc)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	today := cal.Today(s.now())
	date := dateParam(r.URL.Query().Get("week"))
	if date.IsZero() {
		date = today
	}
	v, err := s.planWeekView(ctx, sc, date)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	start := cal.WeekOf(date)
	p := s.page(r, weekTitle(ctx, start, cal.WeekOf(today)), ui.NavPlan)
	s.render(w, r, http.StatusOK, ui.PlanWeek(p, v))
}

// planDayPage shows every meal of a day: the sheet behind the week's
// "+N more" card, or a page without JavaScript.
func (s *Server) planDayPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	date := dateParam(r.URL.Query().Get("date"))
	if date.IsZero() {
		s.notFound(w, r)
		return
	}
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	back := localPath(r.URL.Query().Get("back"), s.weekBack(ctx, sc, date))
	wk, err := s.plan.Week(ctx, sc, date)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	v := ui.PlanDayView{Back: back}
	for _, d := range wk.Days {
		if d.Date == date {
			v.Day = s.planDay(ctx, d, wk.Today, back)
		}
	}
	if !isDatastar(r) {
		p := s.page(r, v.Day.Label, ui.NavPlan)
		p.BackHref, p.BackLabel = back, i18n.T(ctx, "plan.day.back")
		s.render(w, r, http.StatusOK, ui.PlanDayPage(p, v))
		return
	}
	s.sendPatches(w, r, patchTempl(ui.PlanDaySheet(v)))
}

// planMonthView loads a month with date selected (today or the first of
// the month when date is zero or outside it).
func (s *Server) planMonthView(ctx context.Context, sc store.Scoped, year int, month time.Month, date store.Date) (ui.PlanMonthView, error) {
	m, err := s.plan.Month(ctx, sc, year, month)
	if err != nil {
		return ui.PlanMonthView{}, err
	}
	if date.IsZero() || date.Year != m.Year || date.Month != m.Month {
		date = store.DateOf(time.Date(m.Year, m.Month, 1, 12, 0, 0, 0, time.UTC))
		if m.Today.Year == m.Year && m.Today.Month == m.Month {
			date = m.Today
		}
	}
	back := monthHref(m.Year, m.Month, date)
	prev := time.Date(m.Year, m.Month-1, 1, 12, 0, 0, 0, time.UTC)
	next := time.Date(m.Year, m.Month+1, 1, 12, 0, 0, 0, time.UTC)
	v := ui.PlanMonthView{
		Month:     monthString(m.Year, m.Month),
		PrevHref:  monthHref(prev.Year(), prev.Month(), store.Date{}),
		NextHref:  monthHref(next.Year(), next.Month(), store.Date{}),
		TodayHref: "/plan/month",
		WeekHref:  weekHref(date),
		Current:   m.Today.Year == m.Year && m.Today.Month == m.Month,
	}
	planned := 0
	for _, week := range m.Weeks {
		var row []ui.PlanDay
		for _, d := range week {
			pd := s.planDay(ctx, d, m.Today, back)
			pd.InMonth = d.Date.Month == m.Month
			pd.Selected = d.Date == date
			pd.SelectHref = monthHref(m.Year, m.Month, d.Date)
			if !pd.InMonth {
				pd.SelectHref = monthHref(d.Date.Year, d.Date.Month, d.Date)
			} else {
				planned += len(d.Meals)
			}
			if pd.Selected {
				v.Selected = pd
			}
			row = append(row, pd)
		}
		v.Weeks = append(v.Weeks, row)
	}
	if len(m.Weeks) > 0 {
		for _, d := range m.Weeks[0] {
			v.Weekdays = append(v.Weekdays, i18n.T(ctx, weekdayShortKeys[d.Date.Weekday()]))
			v.Initials = append(v.Initials, i18n.T(ctx, weekdayInitialKeys[d.Date.Weekday()]))
		}
	}
	if planned == 1 {
		v.Summary = i18n.T(ctx, "plan.month.plannedOne")
	} else {
		v.Summary = i18n.T(ctx, "plan.month.planned", planned)
	}
	return v, nil
}

func (s *Server) planMonthPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	q := r.URL.Query()
	year, month, ok := monthParam(q.Get("month"))
	if !ok {
		today, err := s.plan.Today(ctx, sc)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		year, month = today.Year, today.Month
	}
	v, err := s.planMonthView(ctx, sc, year, month, dateParam(q.Get("date")))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	p := s.page(r, monthName(ctx, year, month), ui.NavPlan)
	s.render(w, r, http.StatusOK, ui.PlanMonth(p, v))
}

// planLive re-renders the viewer's week or month when the plan, recipes
// or lists change.
func (s *Server) planLive(w http.ResponseWriter, r *http.Request, sc store.Scoped, id *auth.Identity) {
	q := r.URL.Query()
	var refresh func(ctx context.Context, sse *datastar.ServerSentEventGenerator) error
	if year, month, ok := monthParam(q.Get("month")); ok {
		date := dateParam(q.Get("date"))
		refresh = func(ctx context.Context, sse *datastar.ServerSentEventGenerator) error {
			v, err := s.planMonthView(ctx, sc, year, month, date)
			if err != nil {
				return err
			}
			return sse.PatchElementTempl(ui.PlanMonthRegion(v))
		}
	} else {
		week := dateParam(q.Get("week"))
		if week.IsZero() {
			s.notFound(w, r)
			return
		}
		refresh = func(ctx context.Context, sse *datastar.ServerSentEventGenerator) error {
			v, err := s.planWeekView(ctx, sc, week)
			if err != nil {
				return err
			}
			return sse.PatchElementTempl(ui.PlanWeekRegion(v))
		}
	}
	s.serveLive(w, r, id, live.Stream{
		Refresh: refresh,
		Relevant: func(e live.Event) bool {
			switch e.Kind {
			case live.PlanChanged, live.RecipeChanged, live.RecipesChanged, live.ListsChanged:
				return true
			}
			return false
		},
	})
}

// ---- Writes: shared ----

// fields are a write's values: form fields, or the JSON payload of a
// Datastar action without a form (drops, swap picks).
type fields struct {
	r       *http.Request
	payload map[string]any
}

func readFields(r *http.Request) (fields, error) {
	f := fields{r: r}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		if err != nil {
			return f, err
		}
		if err := json.Unmarshal(body, &f.payload); err != nil {
			return f, err
		}
	}
	return f, nil
}

func (f fields) get(name string) string {
	if f.payload != nil {
		switch v := f.payload[name].(type) {
		case string:
			return v
		case float64:
			return strconv.FormatFloat(v, 'f', -1, 64)
		}
		return ""
	}
	return f.r.PostFormValue(name)
}

// all returns every value of name: a repeated form field, or a payload
// string or array of strings.
func (f fields) all(name string) []string {
	if f.payload != nil {
		switch v := f.payload[name].(type) {
		case string:
			return []string{v}
		case []any:
			out := make([]string, 0, len(v))
			for _, x := range v {
				if str, ok := x.(string); ok {
					out = append(out, str)
				}
			}
			return out
		}
		return nil
	}
	if err := f.r.ParseForm(); err != nil {
		return nil
	}
	return f.r.PostForm[name]
}

func (f fields) int(name string) int {
	n, err := strconv.Atoi(f.get(name))
	if err != nil {
		return 0
	}
	return n
}

// planDone answers a successful plan write: Datastar closes the sheet and
// shows the message (the live stream re-renders the plan); a form post
// goes back.
func (s *Server) planDone(w http.ResponseWriter, r *http.Request, back string, msg patch) {
	if !isDatastar(r) {
		seeOther(w, r, back)
		return
	}
	s.sendPatches(w, r, patchTempl(ui.EmptySheet()), msg)
}

// planUndone answers a successful Undo from a toast: Datastar clears the
// toast and leaves any sheet opened since alone; a form post goes back.
func (s *Server) planUndone(w http.ResponseWriter, r *http.Request, back string) {
	if !isDatastar(r) {
		seeOther(w, r, back)
		return
	}
	s.sendPatches(w, r, clearToast)
}

// planError answers a failed plan write; it reports whether err was one.
func (s *Server) planError(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	ctx := r.Context()
	var fe plan.FieldErrors
	switch {
	case errors.Is(err, plan.ErrNotFound):
		if !isDatastar(r) {
			s.notFound(w, r)
			return true
		}
		s.sendPatches(w, r, patchTempl(ui.EmptySheet()), toast(i18n.T(ctx, "plan.toast.gone"), "", nil))
	case errors.As(err, &fe):
		if !isDatastar(r) {
			http.Error(w, i18n.T(ctx, "form.error.summary"), http.StatusUnprocessableEntity)
			return true
		}
		s.sendPatches(w, r, toast(i18n.T(ctx, "form.error.summary"), "", nil))
	default:
		s.serverError(w, r, err)
	}
	return true
}

// mealTarget parses the {id} path value and the scope.
func (s *Server) mealTarget(w http.ResponseWriter, r *http.Request) (uuid.UUID, store.Scoped, bool) {
	return s.groceriesTarget(w, r)
}

// ---- Add to plan ----

// addPlanState is what the add sheet shows.
type addPlanState struct {
	recipes []addRecipeState
	date    store.Date
	week    store.Date
	back    string
	form    ui.Form
}

// addRecipeState is one recipe of the add sheet and what was chosen for
// it.
type addRecipeState struct {
	id       uuid.UUID
	servings int
	picked   map[uuid.UUID]bool
	// also are the linked recipes picked to plan too.
	also map[uuid.UUID]bool
}

// The add sheet names each recipe's fields after it, so one form plans
// several recipes.
func servingsField(id uuid.UUID) string   { return ui.AddServingsField(id.String()) }
func ingredientField(id uuid.UUID) string { return ui.AddIngredientField(id.String()) }
func alsoField(id uuid.UUID) string       { return ui.AddAlsoField(id.String()) }

// addPlanView builds the add sheet. It returns recipes.ErrNotFound when
// one of its recipes is gone.
func (s *Server) addPlanView(ctx context.Context, sc store.Scoped, id *auth.Identity, st addPlanState) (ui.AddPlanView, error) {
	cal, err := s.plan.Calendar(ctx, sc)
	if err != nil {
		return ui.AddPlanView{}, err
	}
	today := cal.Today(s.now())
	if st.date.IsZero() {
		st.date = today
	}
	if st.week.IsZero() {
		st.week = st.date
	}
	week := cal.WeekOf(st.week)
	// A week other than the chosen day's keeps its weekday.
	if from := cal.WeekOf(st.date); from != week {
		st.date = week.AddDays(dayIndex(from, st.date))
	}
	picker, err := s.addPicker(ctx, sc, week, st.date)
	if err != nil {
		return ui.AddPlanView{}, err
	}
	v := ui.AddPlanView{Days: picker, Form: st.form}
	inSheet := make(map[uuid.UUID]bool, len(st.recipes))
	for _, rs := range st.recipes {
		inSheet[rs.id] = true
	}
	fallback := "/plan"
	for i, rs := range st.recipes {
		rec, err := s.recipes.Get(ctx, sc, rs.id, id.UserID)
		if err != nil {
			return ui.AddPlanView{}, err
		}
		ls, err := s.recipes.ListStatus(ctx, sc, rec.Ingredients)
		if err != nil {
			return ui.AddPlanView{}, err
		}
		if i == 0 {
			v.ListName = ls.List.Name
			fallback = ui.RecipeHref(rec.Slug)
		}
		r := ui.AddPlanRecipe{
			ID: rec.ID.String(), Name: rec.Name, Meta: minutes(ctx, rec.TotalMin()),
			Servings: clampServings(rs.servings, rec.Servings), Base: rec.Servings,
		}
		var already []string
		for _, ing := range rec.Ingredients {
			if ls.On[ing.ItemID] {
				already = append(already, ing.ItemName)
				continue
			}
			r.Shop = append(r.Shop, ui.ShopChip{ID: ing.ID.String(), Name: ing.ItemName, Checked: rs.picked[ing.ID]})
		}
		for _, l := range rec.Links {
			// A linked recipe in the sheet already is planned as itself.
			if inSheet[l.ID] {
				continue
			}
			r.Also = append(r.Also, ui.ShopChip{ID: l.ID.String(), Name: l.Name, Checked: rs.also[l.ID]})
		}
		switch {
		case len(already) > 0:
			r.Already = i18n.T(ctx, "plan.addTo.already", joinNames(ctx, already))
		case len(r.Shop) > 0:
			r.Already = i18n.T(ctx, "plan.addTo.none")
		}
		v.Recipes = append(v.Recipes, r)
	}
	if len(v.Recipes) > 1 {
		fallback = weekHref(week)
	}
	v.Back = localPath(st.back, fallback)
	return v, nil
}

// dayIndex is how many days d is after start.
func dayIndex(start, d store.Date) int {
	n := 0
	for x := start; x.Before(d); x = x.AddDays(1) {
		n++
	}
	return n
}

// addPicker is the add sheet's week with date chosen.
func (s *Server) addPicker(ctx context.Context, sc store.Scoped, week, date store.Date) (ui.PlanPicker, error) {
	w, err := s.plan.Week(ctx, sc, week)
	if err != nil {
		return ui.PlanPicker{}, err
	}
	p := ui.PlanPicker{
		Date: date.String(), Week: week.String(), Range: weekRange(ctx, week),
		Prev: week.AddDays(-7).String(), Next: week.AddDays(7).String(),
		Notes: map[string]string{}, CTAs: map[string]string{"": i18n.T(ctx, "plan.addTo.pickDay")},
	}
	for _, d := range w.Days {
		key := d.Date.String()
		p.Days = append(p.Days, ui.PickDay{
			Date: key, Initial: i18n.T(ctx, weekdayInitialKeys[d.Date.Weekday()]), Num: d.Date.Day,
			Label: dayPickLabel(ctx, d), Planned: len(d.Meals) > 0,
		})
		if len(d.Meals) > 0 {
			p.Notes[key] = i18n.T(ctx, "plan.addTo.both", joinNames(ctx, mealNames(d.Meals)))
		} else {
			p.Notes[key] = i18n.T(ctx, "plan.addTo.free")
		}
		p.CTAs[key] = i18n.T(ctx, "plan.addTo.cta", weekday(ctx, d.Date))
	}
	return p, nil
}

func mealNames(ms []plan.Meal) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Recipe.Name
	}
	return out
}

func dayPickLabel(ctx context.Context, d plan.Day) string {
	if len(d.Meals) == 0 {
		return i18n.T(ctx, "plan.cell.free", fullDate(ctx, d.Date))
	}
	return i18n.T(ctx, "plan.cell.has", fullDate(ctx, d.Date), joinNames(ctx, mealNames(d.Meals)))
}

// addStateFromQuery reads the add sheet's query: its first load, or a
// reload without JavaScript (stepper, week buttons, removing a recipe)
// carrying what was chosen.
func addStateFromQuery(r *http.Request) (addPlanState, bool) {
	q := r.URL.Query()
	ids := parseIDs(q["recipe"])
	if drop, err := uuid.Parse(q.Get("drop")); err == nil && len(ids) > 1 {
		ids = slices.DeleteFunc(ids, func(id uuid.UUID) bool { return id == drop })
	}
	if len(ids) == 0 {
		return addPlanState{}, false
	}
	st := addPlanState{date: dateParam(q.Get("date")), week: dateParam(q.Get("week")), back: q.Get("back")}
	for _, id := range ids {
		rs := addRecipeState{id: id, servings: atoi(q.Get(servingsField(id))), picked: map[uuid.UUID]bool{}, also: map[uuid.UUID]bool{}}
		if n := atoi(q.Get(ui.AddSetServingsField(id.String()))); n > 0 {
			rs.servings = n
		}
		for _, x := range parseIDs(q[ingredientField(id)]) {
			rs.picked[x] = true
		}
		for _, x := range parseIDs(q[alsoField(id)]) {
			rs.also[x] = true
		}
		st.recipes = append(st.recipes, rs)
	}
	return st, true
}

func atoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

func (s *Server) addPlanPage(w http.ResponseWriter, r *http.Request) {
	st, ok := addStateFromQuery(r)
	if !ok {
		// The picker's "Plan selected" without a recipe ticked goes back to
		// the picker.
		q := r.URL.Query()
		if d := dateParam(q.Get("date")); !d.IsZero() && !isDatastar(r) {
			seeOther(w, r, pickHref(d, localPath(q.Get("back"), "")))
			return
		}
		s.notFound(w, r)
		return
	}
	ctx := r.Context()
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderAddPlan(w, r, http.StatusOK, sc, st)
}

func (s *Server) renderAddPlan(w http.ResponseWriter, r *http.Request, status int, sc store.Scoped, st addPlanState) {
	ctx := r.Context()
	v, err := s.addPlanView(ctx, sc, identity(r), st)
	if errors.Is(err, recipes.ErrNotFound) {
		s.recipeGone(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if isDatastar(r) {
		s.sendPatches(w, r, patchTempl(ui.AddPlanSheet(v)))
		return
	}
	p := s.page(r, i18n.T(ctx, "plan.addTo.title"), ui.NavPlan)
	p.BackHref, p.BackLabel = v.Back, i18n.T(ctx, "recipes.menu.cancel")
	s.render(w, r, status, ui.AddPlanPage(p, v))
}

// addPlanDays loads another week into the open add sheet, keeping the
// chosen weekday.
func (s *Server) addPlanDays(w http.ResponseWriter, r *http.Request) {
	st, ok := addStateFromQuery(r)
	if !ok || st.week.IsZero() {
		s.notFound(w, r)
		return
	}
	var sig struct {
		Date string `json:"plandate"`
	}
	if err := datastar.ReadSignals(r, &sig); err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	st.date = dateParam(sig.Date)
	ctx := r.Context()
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	v, err := s.addPlanView(ctx, sc, identity(r), st)
	if errors.Is(err, recipes.ErrNotFound) {
		s.recipeGone(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	// The days first: the signal's bind checks the radio whose value is
	// the date, and the morphed radios only take the new week's dates.
	s.sendPatches(w, r, patchTempl(ui.AddPlanDays(v)), patchTempl(ui.AddPlanSubmit(v)),
		patchSignals(map[string]any{"plandate": v.Days.Date}))
}

// addMeal plans recipes: the add sheet's form, or suggestions dropped on
// a day.
func (s *Server) addMeal(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f, err := readFields(r)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	raw := f.all("recipe")
	if len(raw) == 0 {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	id := identity(r)
	in := plan.AddInput{Date: dateParam(f.get("date")), By: id.UserID, Lang: i18n.Locale(ctx)}
	st := addPlanState{date: in.Date, back: f.get("back")}
	for _, v := range raw {
		recipeID, err := uuid.Parse(v)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
		mi := plan.MealInput{RecipeID: recipeID, Servings: f.int(servingsField(recipeID))}
		rs := addRecipeState{id: recipeID, servings: mi.Servings, picked: map[uuid.UUID]bool{}, also: map[uuid.UUID]bool{}}
		var ok bool
		if mi.Ingredients, ok = fieldIDs(f.all(ingredientField(recipeID))); !ok {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
		if mi.Also, ok = fieldIDs(f.all(alsoField(recipeID))); !ok {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
		for _, x := range mi.Ingredients {
			rs.picked[x] = true
		}
		for _, x := range mi.Also {
			rs.also[x] = true
		}
		in.Meals = append(in.Meals, mi)
		st.recipes = append(st.recipes, rs)
	}
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	added, err := s.plan.Add(ctx, sc, in)
	var fe plan.FieldErrors
	if errors.As(err, &fe) && !isDatastar(r) {
		st.form = ui.Form{Errors: map[string]string{}}
		for field, key := range fe {
			st.form.Errors[field] = i18n.T(ctx, key)
		}
		if _, ok := fe["date"]; ok {
			st.form.Alert = i18n.T(ctx, "plan.addTo.pickDay")
		}
		s.renderAddPlan(w, r, http.StatusUnprocessableEntity, sc, st)
		return
	}
	if errors.Is(err, plan.ErrNotFound) {
		// A recipe was deleted (or an ingredient is no longer its).
		s.recipeGone(w, r)
		return
	}
	if s.planError(w, r, err) {
		return
	}
	cal, err := s.plan.Calendar(ctx, sc)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	first := added.Meals[0]
	msg := i18n.T(ctx, "plan.toast.planned", joinNames(ctx, mealNames(added.Meals)), shortDate(ctx, first.Date))
	if len(added.Also) > 0 {
		msg += " · " + i18n.T(ctx, "plan.toast.also", joinNames(ctx, mealNames(added.Also)))
	}
	switch n := len(added.Shopping.Added); n {
	case 0:
	case 1:
		msg += " · " + i18n.T(ctx, "plan.toast.onListOne", added.Shopping.List.Name)
	default:
		msg += " · " + i18n.T(ctx, "plan.toast.onList", n, added.Shopping.List.Name)
	}
	undo := map[string]string{}
	if len(added.Shopping.Added) > 0 {
		ids := make([]uuid.UUID, len(added.Shopping.Added))
		for i, e := range added.Shopping.Added {
			ids[i] = e.ID
		}
		undo["entries"] = joinIDs(ids)
	}
	var others []uuid.UUID
	for _, m := range added.All() {
		if m.ID != first.ID {
			others = append(others, m.ID)
		}
	}
	if len(others) > 0 {
		undo["also"] = joinIDs(others)
	}
	back := localPath(f.get("back"), weekHref(cal.WeekOf(first.Date)))
	s.planDone(w, r, back, toast(msg, "/plan/meals/"+first.ID.String()+"/unplan", undo))
}

// fieldIDs parses form or payload values as IDs; ok is false when one is
// malformed.
func fieldIDs(vals []string) (ids []uuid.UUID, ok bool) {
	for _, v := range vals {
		id, err := uuid.Parse(v)
		if err != nil {
			return nil, false
		}
		ids = append(ids, id)
	}
	return ids, true
}

// joinIDs is the comma-separated list idList reads.
func joinIDs(ids []uuid.UUID) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = id.String()
	}
	return strings.Join(parts, ",")
}

// unplanMeal is the Undo of planning a meal: it removes the meal and the
// entries planning it put on the list, unless they changed since.
func (s *Server) unplanMeal(w http.ResponseWriter, r *http.Request) {
	mealID, sc, ok := s.mealTarget(w, r)
	if !ok {
		return
	}
	entries, ok1 := idList(r.PostFormValue("entries"))
	also, ok2 := idList(r.PostFormValue("also"))
	if !ok1 || !ok2 {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	m, err := s.plan.Unplan(ctx, sc, mealID, entries, also)
	if s.planError(w, r, err) {
		return
	}
	s.planUndone(w, r, localPath(r.PostFormValue("back"), s.weekBack(ctx, sc, m.Date)))
}

// idList parses a comma-separated list of IDs; ok is false when one is
// malformed.
func idList(raw string) (ids []uuid.UUID, ok bool) {
	if raw == "" {
		return nil, true
	}
	for _, e := range strings.Split(raw, ",") {
		id, err := uuid.Parse(e)
		if err != nil {
			return nil, false
		}
		ids = append(ids, id)
	}
	return ids, true
}

// ---- Recipe picker ----

func (s *Server) pickView(ctx context.Context, sc store.Scoped, id *auth.Identity, date store.Date, q, back string, shown int) (ui.PlanPickView, error) {
	cal, err := s.plan.Calendar(ctx, sc)
	if err != nil {
		return ui.PlanPickView{}, err
	}
	if date.IsZero() {
		date = cal.Today(s.now())
	}
	back = localPath(back, weekHref(cal.WeekOf(date)))
	v := ui.PlanPickView{Date: date.String(), DayLabel: fullDate(ctx, date), Query: q, Back: back}
	w, err := s.plan.Week(ctx, sc, date)
	if err != nil {
		return ui.PlanPickView{}, err
	}
	onDay := map[uuid.UUID]bool{}
	for _, d := range w.Days {
		if d.Date == date {
			for _, m := range d.Meals {
				onDay[m.Recipe.ID] = true
			}
		}
	}
	cards, matched, err := s.pickCards(ctx, sc, id, q, shown, uuid.Nil)
	if err != nil {
		return ui.PlanPickView{}, err
	}
	v.Shown, v.Matched = clampShown(shown), matched
	// Suggestions only help when the list below does not already show the
	// whole library.
	if q == "" && matched > len(cards) {
		week := cal.WeekOf(date)
		sugg, err := s.plan.Suggestions(ctx, sc, week, week.AddDays(7), staleShown)
		if err != nil {
			return ui.PlanPickView{}, err
		}
		now := s.now()
		for _, sg := range sugg {
			v.Suggested = append(v.Suggested, ui.PickRecipe{
				ID: sg.Recipe.ID.String(), Name: sg.Recipe.Name, Photo: sg.Recipe.Photo,
				Meta: joinNonEmpty(" · ", minutes(ctx, sg.Recipe.TotalMin), lastCooked(ctx, now, sg.LastCookedAt)),
				Href: addHref(sg.Recipe.ID, date, store.Date{}, back), Planned: onDay[sg.Recipe.ID],
			})
		}
	}
	for _, c := range cards {
		v.Recipes = append(v.Recipes, ui.PickRecipe{
			ID: c.ID.String(), Name: c.Name, Photo: c.Photo, Meta: cardMeta(ctx, c), Href: addHref(c.ID, date, store.Date{}, back),
			Planned: onDay[c.ID],
		})
	}
	if q != "" {
		v.Status = plural(ctx, len(v.Recipes), "recipes.resultsOne", "recipes.results")
	}
	return v, nil
}

// pickCards lists the recipes to pick from, leaving out exclude (uuid.Nil
// for none): without a query the first shown by name, in whole pages of
// ui.LibraryPageSize like the library, else every match of q. It also
// returns how many recipes there are to pick from in all.
func (s *Server) pickCards(ctx context.Context, sc store.Scoped, id *auth.Identity, q string, shown int, exclude uuid.UUID) ([]recipes.Card, int, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		f := recipes.Filter{Sort: recipes.SortName, Exclude: exclude}
		return s.recipes.LibraryPage(ctx, sc, id.UserID, f, clampShown(shown))
	}
	res, err := s.recipes.Search(ctx, sc, id.UserID, q)
	if err != nil {
		return nil, 0, err
	}
	cards := make([]recipes.Card, 0, len(res.Names)+len(res.Ingredients))
	for _, n := range res.Names {
		cards = append(cards, n.Card)
	}
	for _, m := range res.Ingredients {
		cards = append(cards, m.Card)
	}
	cards = slices.DeleteFunc(cards, func(c recipes.Card) bool { return c.ID == exclude })
	return cards, len(cards), nil
}

func plural(ctx context.Context, n int, one, other i18n.Key) string {
	if n == 1 {
		return i18n.T(ctx, one)
	}
	return i18n.T(ctx, other, n)
}

func (s *Server) pickPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	q := r.URL.Query()
	v, err := s.pickView(ctx, sc, identity(r), dateParam(q.Get("date")), q.Get("q"), q.Get("back"), shownParam(q))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if isDatastar(r) {
		s.sendPatches(w, r, patchTempl(ui.PlanPickSheet(v)))
		return
	}
	p := s.page(r, i18n.T(ctx, "plan.pick.title"), ui.NavPlan)
	p.BackHref, p.BackLabel = v.Back, i18n.T(ctx, "recipes.menu.cancel")
	s.render(w, r, http.StatusOK, ui.PlanPickPage(p, v))
}

// pickResults re-renders the picker's list for the search signal.
func (s *Server) pickResults(w http.ResponseWriter, r *http.Request) {
	var sig struct {
		Q string `json:"pickq"`
	}
	if err := datastar.ReadSignals(r, &sig); err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	q := r.URL.Query()
	v, err := s.pickView(ctx, sc, identity(r), dateParam(q.Get("date")), sig.Q, q.Get("back"), shownParam(q))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.sendPatches(w, r, patchTempl(ui.PlanPickResults(v, q.Get(ui.PageParam) == "")))
}

// ---- Meal menu ----

// mealMenuState is what the meal menu shows.
type mealMenuState struct {
	view     string
	week     store.Date
	moveTo   store.Date
	servings int
	query    string
	// shown is how many recipes the swap list shows without a query.
	shown int
	back  string
}

func (s *Server) mealMenuView(ctx context.Context, sc store.Scoped, id *auth.Identity, mealID uuid.UUID, st mealMenuState) (ui.MealMenuView, error) {
	m, err := s.plan.Get(ctx, sc, mealID)
	if err != nil {
		return ui.MealMenuView{}, err
	}
	cal, err := s.plan.Calendar(ctx, sc)
	if err != nil {
		return ui.MealMenuView{}, err
	}
	today := cal.Today(s.now())
	v := ui.MealMenuView{
		ID: m.ID.String(), RecipeID: m.Recipe.ID.String(), RecipeSlug: m.Recipe.Slug, Name: m.Recipe.Name, Photo: m.Recipe.Photo,
		DayLabel: fullDate(ctx, m.Date), Day: weekday(ctx, m.Date), Servings: m.Servings, Base: m.Recipe.Servings,
		View: st.view, Cooked: !today.Before(m.Date), Query: st.query,
		Back: localPath(st.back, weekHref(cal.WeekOf(m.Date))),
	}
	switch st.view {
	case "move":
		week := st.week
		if week.IsZero() {
			week = m.Date
		}
		if v.Move, err = s.movePicker(ctx, sc, m, cal.WeekOf(week), st.moveTo); err != nil {
			return ui.MealMenuView{}, err
		}
	case "servings":
		v.Servings = clampServings(st.servings, m.Servings)
	case "swap":
		cards, matched, err := s.pickCards(ctx, sc, id, st.query, st.shown, m.Recipe.ID)
		if err != nil {
			return ui.MealMenuView{}, err
		}
		v.SwapShown, v.SwapMatched = clampShown(st.shown), matched
		for _, c := range cards {
			v.Swap = append(v.Swap, ui.PickRecipe{ID: c.ID.String(), Name: c.Name, Photo: c.Photo, Meta: cardMeta(ctx, c)})
		}
	}
	return v, nil
}

// movePicker is the move view's week with to chosen (none when zero or
// outside the week).
func (s *Server) movePicker(ctx context.Context, sc store.Scoped, m plan.Meal, week, to store.Date) (ui.PlanPicker, error) {
	w, err := s.plan.Week(ctx, sc, week)
	if err != nil {
		return ui.PlanPicker{}, err
	}
	p := ui.PlanPicker{
		Week: week.String(), Range: weekRange(ctx, week), Prev: week.AddDays(-7).String(), Next: week.AddDays(7).String(),
		Notes: map[string]string{"": i18n.T(ctx, "plan.move.now", weekday(ctx, m.Date))},
		CTAs:  map[string]string{"": i18n.T(ctx, "plan.move.pick")},
	}
	for _, d := range w.Days {
		key := d.Date.String()
		var others []plan.Meal
		for _, x := range d.Meals {
			if x.ID != m.ID {
				others = append(others, x)
			}
		}
		pd := ui.PickDay{
			Date: key, Initial: i18n.T(ctx, weekdayInitialKeys[d.Date.Weekday()]), Num: d.Date.Day,
			Label: dayPickLabel(ctx, d), Current: d.Date == m.Date,
		}
		switch {
		case pd.Current:
			pd.Tag = i18n.T(ctx, "plan.tag.now")
			pd.Label = i18n.T(ctx, "plan.cell.current", fullDate(ctx, d.Date))
		case len(others) > 0:
			pd.Tag, pd.Planned = i18n.T(ctx, "plan.tag.swap"), true
			p.Notes[key] = i18n.T(ctx, "plan.move.swapNote", others[0].Recipe.Name, weekday(ctx, d.Date))
			p.CTAs[key] = i18n.T(ctx, "plan.move.swap", weekday(ctx, d.Date))
		default:
			pd.Tag = i18n.T(ctx, "plan.tag.free")
			p.Notes[key] = i18n.T(ctx, "plan.move.freeNote", weekday(ctx, d.Date))
			p.CTAs[key] = i18n.T(ctx, "plan.move.to", weekday(ctx, d.Date))
		}
		if !pd.Current && d.Date == to {
			p.Date = key
		}
		p.Days = append(p.Days, pd)
	}
	return p, nil
}

func (s *Server) mealMenu(w http.ResponseWriter, r *http.Request) {
	mealID, sc, ok := s.mealTarget(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	q := r.URL.Query()
	st := mealMenuState{
		view: q.Get("view"), week: dateParam(q.Get("week")), moveTo: dateParam(q.Get("date")),
		servings: atoi(q.Get("servings")), query: q.Get("q"), shown: shownParam(q), back: q.Get("back"),
	}
	if n := atoi(q.Get("set_servings")); n > 0 {
		st.servings = n
	}
	part := ""
	if isDatastar(r) {
		part = q.Get("part")
		var sig struct {
			MoveDate string `json:"movedate"`
			SwapQ    string `json:"swapq"`
		}
		if err := datastar.ReadSignals(r, &sig); err != nil {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
		if st.moveTo.IsZero() {
			st.moveTo = dateParam(sig.MoveDate)
		}
		if part == "results" {
			st.query = sig.SwapQ
		}
	}
	switch st.view {
	case "", "move", "servings", "swap":
	default:
		st.view = ""
	}
	v, err := s.mealMenuView(ctx, sc, identity(r), mealID, st)
	if s.planError(w, r, err) {
		return
	}
	if !isDatastar(r) {
		p := s.page(r, v.Name, ui.NavPlan)
		p.BackHref, p.BackLabel = v.Back, i18n.T(ctx, "recipes.menu.cancel")
		s.render(w, r, http.StatusOK, ui.MealMenuPage(p, v))
		return
	}
	switch part {
	case "body":
		// The body first, so the move view's radios have the week's dates
		// when movedate's bind checks one.
		s.sendPatches(w, r,
			patchTempl(ui.MealMenuBody(v, true)),
			patchSignals(map[string]any{"movedate": v.Move.Date, "mealservings": v.Servings, "swapq": v.Query}))
	case "results":
		s.sendPatches(w, r, patchTempl(ui.MealSwapResults(v, q.Get(ui.PageParam) == "")))
	default:
		s.sendPatches(w, r, patchTempl(ui.MealMenuSheet(v)))
	}
}

// placements encodes the Undo of a move or swap for a toast's form.
func placements(ps []plan.Placement) string {
	parts := make([]string, len(ps))
	for i, p := range ps {
		parts[i] = p.Meal.String() + "~" + p.Date.String() + "~" + strconv.Itoa(p.Position)
	}
	return strings.Join(parts, ",")
}

func parsePlacements(raw string) ([]plan.Placement, error) {
	var out []plan.Placement
	for _, part := range strings.Split(raw, ",") {
		f := strings.Split(part, "~")
		if len(f) != 3 {
			return nil, errors.New("web: bad placement")
		}
		id, err := uuid.Parse(f[0])
		if err != nil {
			return nil, err
		}
		d, err := store.ParseDate(f[1])
		if err != nil {
			return nil, err
		}
		pos, err := strconv.Atoi(f[2])
		if err != nil {
			return nil, err
		}
		out = append(out, plan.Placement{Meal: id, Date: d, Position: pos})
	}
	return out, nil
}

// moveMeal moves a meal to another day: mode "swap" (a drop, or "Swap
// with …") swaps it with that day's first meal; "move" adds it after the
// day's meals.
func (s *Server) moveMeal(w http.ResponseWriter, r *http.Request) {
	mealID, sc, ok := s.mealTarget(w, r)
	if !ok {
		return
	}
	f, err := readFields(r)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	to := dateParam(f.get("date"))
	var ch plan.Change
	if f.get("mode") == "move" {
		ch, err = s.plan.Move(ctx, sc, mealID, to)
	} else {
		ch, err = s.plan.Swap(ctx, sc, mealID, to)
	}
	if s.planError(w, r, err) {
		return
	}
	back := localPath(f.get("back"), s.weekBack(ctx, sc, to))
	if len(ch.Undo) == 0 {
		s.planDone(w, r, back, clearToast)
		return
	}
	var msg string
	if ch.With != nil {
		msg = i18n.T(ctx, "plan.toast.swapped", ch.Meal.Recipe.Name, ch.With.Recipe.Name)
	} else {
		msg = i18n.T(ctx, "plan.toast.moved", ch.Meal.Recipe.Name, shortDate(ctx, ch.Meal.Date))
	}
	s.planDone(w, r, back, toast(msg, "/plan/place", map[string]string{"placements": placements(ch.Undo), "back": back}))
}

// placeMeals is the Undo of a move or swap.
func (s *Server) placeMeals(w http.ResponseWriter, r *http.Request) {
	ps, err := parsePlacements(r.PostFormValue("placements"))
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if s.planError(w, r, s.plan.Place(ctx, sc, ps)) {
		return
	}
	s.planUndone(w, r, localPath(r.PostFormValue("back"), s.weekBack(ctx, sc, ps[0].Date)))
}

func (s *Server) setMealServings(w http.ResponseWriter, r *http.Request) {
	mealID, sc, ok := s.mealTarget(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	servings := atoi(r.PostFormValue("servings"))
	old, err := s.plan.SetServings(ctx, sc, mealID, servings)
	if s.planError(w, r, err) {
		return
	}
	m, err := s.plan.Get(ctx, sc, mealID)
	if s.planError(w, r, err) {
		return
	}
	back := localPath(r.PostFormValue("back"), s.weekBack(ctx, sc, m.Date))
	msg := i18n.T(ctx, "plan.toast.servings", m.Recipe.Name, servingsText(ctx, servings))
	s.planDone(w, r, back, toast(msg, "/plan/meals/"+mealID.String()+"/servings",
		map[string]string{"servings": strconv.Itoa(old), "back": back}))
}

func (s *Server) replaceMeal(w http.ResponseWriter, r *http.Request) {
	mealID, sc, ok := s.mealTarget(w, r)
	if !ok {
		return
	}
	f, err := readFields(r)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	recipeID, err := uuid.Parse(f.get("recipe"))
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	old, err := s.plan.Replace(ctx, sc, mealID, recipeID)
	if s.planError(w, r, err) {
		return
	}
	m, err := s.plan.Get(ctx, sc, mealID)
	if s.planError(w, r, err) {
		return
	}
	back := localPath(f.get("back"), s.weekBack(ctx, sc, m.Date))
	s.planDone(w, r, back, toast(i18n.T(ctx, "plan.toast.replaced", m.Recipe.Name), "/plan/meals/"+mealID.String()+"/replace",
		map[string]string{"recipe": old.String(), "back": back}))
}

func (s *Server) removeMeal(w http.ResponseWriter, r *http.Request) {
	mealID, sc, ok := s.mealTarget(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	m, err := s.plan.Remove(ctx, sc, mealID)
	if s.planError(w, r, err) {
		return
	}
	back := localPath(r.PostFormValue("back"), s.weekBack(ctx, sc, m.Date))
	s.planDone(w, r, back, toast(i18n.T(ctx, "plan.toast.removed", m.Recipe.Name, shortDate(ctx, m.Date)),
		"/plan/meals/"+mealID.String()+"/restore", map[string]string{"back": back}))
}

func (s *Server) restoreMeal(w http.ResponseWriter, r *http.Request) {
	mealID, sc, ok := s.mealTarget(w, r)
	if !ok {
		return
	}
	if s.planError(w, r, s.plan.Restore(r.Context(), sc, mealID)) {
		return
	}
	s.planUndone(w, r, localPath(r.PostFormValue("back"), "/plan"))
}

// mealsField reads the meals of a bulk action: repeated "meal" values or
// a payload array. ok is false when there are none or one is malformed.
func mealsField(f fields) ([]uuid.UUID, bool) {
	ids, ok := fieldIDs(f.all("meal"))
	return ids, ok && len(ids) > 0
}

// moveMeals puts several meals on a day, after its meals: selected meals
// dropped on a day.
func (s *Server) moveMeals(w http.ResponseWriter, r *http.Request) {
	f, err := readFields(r)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	ids, ok := mealsField(f)
	if !ok {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	to := dateParam(f.get("date"))
	mv, err := s.plan.MoveAll(ctx, sc, ids, to)
	if s.planError(w, r, err) {
		return
	}
	back := localPath(f.get("back"), s.weekBack(ctx, sc, to))
	if len(mv.Undo) == 0 {
		s.planDone(w, r, back, clearToast)
		return
	}
	msg := i18n.T(ctx, "plan.toast.moved", joinNames(ctx, mealNames(mv.Meals)), shortDate(ctx, to))
	s.planDone(w, r, back, toast(msg, "/plan/place", map[string]string{"placements": placements(mv.Undo), "back": back}))
}

// removeMeals takes several meals off the plan: the selection removed
// with the Delete key or the selection bar.
func (s *Server) removeMeals(w http.ResponseWriter, r *http.Request) {
	f, err := readFields(r)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	ids, ok := mealsField(f)
	if !ok {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	gone, err := s.plan.RemoveAll(ctx, sc, ids)
	if s.planError(w, r, err) {
		return
	}
	back := localPath(f.get("back"), s.weekBack(ctx, sc, gone[0].Date))
	names := joinNames(ctx, mealNames(gone))
	msg := i18n.T(ctx, "plan.toast.removedMany", names)
	if sameDay(gone) {
		msg = i18n.T(ctx, "plan.toast.removed", names, shortDate(ctx, gone[0].Date))
	}
	removed := make([]uuid.UUID, len(gone))
	for i, m := range gone {
		removed[i] = m.ID
	}
	s.planDone(w, r, back, toast(msg, "/plan/restore", map[string]string{"meals": joinIDs(removed), "back": back}))
}

// sameDay reports whether the meals are all on one day.
func sameDay(ms []plan.Meal) bool {
	for _, m := range ms {
		if m.Date != ms[0].Date {
			return false
		}
	}
	return true
}

// restoreMeals is the Undo of removeMeals.
func (s *Server) restoreMeals(w http.ResponseWriter, r *http.Request) {
	ids, ok := idList(r.PostFormValue("meals"))
	if !ok || len(ids) == 0 {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if s.planError(w, r, s.plan.RestoreAll(ctx, sc, ids)) {
		return
	}
	s.planUndone(w, r, localPath(r.PostFormValue("back"), "/plan"))
}

// ---- Cook events ----

// markCooked records "I made this" for today.
func (s *Server) markCooked(w http.ResponseWriter, r *http.Request) {
	recipeID, slug, sc, ok := s.recipeTarget(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	event, err := s.plan.MarkCooked(ctx, sc, recipeID, identity(r).UserID)
	if errors.Is(err, plan.ErrNotFound) {
		s.recipeGone(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	back := localPath(r.PostFormValue("back"), ui.RecipeHref(slug))
	if !isDatastar(r) {
		onPage, err := s.isRecipePage(ctx, sc, back, recipeID)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		if onPage {
			s.setCookedFlash(w, r, recipeID, event)
		}
	}
	s.planDone(w, r, back, patchTempl(ui.ToastRegion(cookedToast(ctx, recipeID, event, back))))
}

func (s *Server) undoCooked(w http.ResponseWriter, r *http.Request) {
	recipeID, slug, sc, ok := s.recipeTarget(w, r)
	if !ok {
		return
	}
	eventID, err := uuid.Parse(r.PathValue("event"))
	if err != nil {
		s.notFound(w, r)
		return
	}
	if s.planError(w, r, s.plan.UndoCooked(r.Context(), sc, recipeID, eventID)) {
		return
	}
	s.planUndone(w, r, localPath(r.PostFormValue("back"), ui.RecipeHref(slug)))
}

// ---- Library ----

// tonightCard is the library's "Suggested for tonight", or nil.
func (s *Server) tonightCard(ctx context.Context, sc store.Scoped) (*ui.TonightCard, error) {
	t, err := s.plan.Tonight(ctx, sc)
	if err != nil {
		return nil, err
	}
	switch {
	case t.Meal != nil:
		r := t.Meal.Recipe
		return &ui.TonightCard{RecipeID: r.ID.String(), RecipeSlug: r.Slug, Name: r.Name, Photo: r.Photo, Planned: true,
			Meta: minutes(ctx, r.TotalMin)}, nil
	case t.Suggestion != nil:
		r := t.Suggestion.Recipe
		return &ui.TonightCard{RecipeID: r.ID.String(), RecipeSlug: r.Slug, Name: r.Name, Photo: r.Photo,
			Meta: joinNonEmpty(" · ", minutes(ctx, r.TotalMin), lastCooked(ctx, s.now(), t.Suggestion.LastCookedAt))}, nil
	}
	return nil, nil
}

func joinNonEmpty(sep string, parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}
