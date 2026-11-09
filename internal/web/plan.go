package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

// addHref opens the add-to-plan sheet.
func addHref(recipeID uuid.UUID, date store.Date, back string) string {
	q := url.Values{"recipe": {recipeID.String()}}
	if !date.IsZero() {
		q.Set("date", date.String())
	}
	if back != "" {
		q.Set("back", back)
	}
	return withParams("/plan/add", q)
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
		Label: fullDate(ctx, d.Date), Today: d.Date == today, Past: d.Date.Before(today), InMonth: true,
		AddHref:  pickHref(d.Date, back),
		MoreHref: monthHref(d.Date.Year, d.Date.Month, d.Date),
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
	v := ui.PlanWeekView{
		Week: w.Start.String(), PrevHref: weekHref(w.Start.AddDays(-7)), NextHref: weekHref(w.Start.AddDays(7)),
		TodayHref: "/plan", Current: w.Start == thisWeek,
		MonthHref: monthHref(date.Year, date.Month, store.Date{}),
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
			Last: lastCooked(ctx, now, sg.LastCookedAt), AddHref: addHref(sg.Recipe.ID, store.Date{}, back),
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

func (f fields) all(name string) []string {
	if f.payload != nil {
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
	recipe   uuid.UUID
	date     store.Date
	week     store.Date
	servings int
	picked   map[uuid.UUID]bool
	back     string
	form     ui.Form
}

// addPlanView builds the add sheet. It returns recipes.ErrNotFound when
// the recipe is gone.
func (s *Server) addPlanView(ctx context.Context, sc store.Scoped, id *auth.Identity, st addPlanState) (ui.AddPlanView, error) {
	rec, err := s.recipes.Get(ctx, sc, st.recipe, id.UserID)
	if err != nil {
		return ui.AddPlanView{}, err
	}
	ls, err := s.recipes.ListStatus(ctx, sc, rec.Ingredients)
	if err != nil {
		return ui.AddPlanView{}, err
	}
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
	v := ui.AddPlanView{
		RecipeID: rec.ID.String(), Name: rec.Name, Meta: minutes(ctx, rec.TotalMin()), Days: picker,
		Servings: clampServings(st.servings, rec.Servings), Base: rec.Servings, ListName: ls.List.Name,
		Back: localPath(st.back, ui.RecipeHref(rec.ID.String())), Form: st.form,
	}
	var already []string
	for _, ing := range rec.Ingredients {
		if ls.On[ing.ItemID] {
			already = append(already, ing.ItemName)
			continue
		}
		v.Shop = append(v.Shop, ui.ShopChip{ID: ing.ID.String(), Name: ing.ItemName, Checked: st.picked[ing.ID]})
	}
	switch {
	case len(already) > 0:
		v.Already = i18n.T(ctx, "plan.addTo.already", joinNames(ctx, already))
	case len(v.Shop) > 0:
		v.Already = i18n.T(ctx, "plan.addTo.none")
	}
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

// addPlanState reads the add sheet's query: its first load, or a reload
// without JavaScript (stepper, week buttons) carrying what was chosen.
func addStateFromQuery(r *http.Request) (addPlanState, bool) {
	q := r.URL.Query()
	recipeID, err := uuid.Parse(q.Get("recipe"))
	if err != nil {
		return addPlanState{}, false
	}
	st := addPlanState{
		recipe: recipeID, date: dateParam(q.Get("date")), week: dateParam(q.Get("week")),
		servings: atoi(q.Get("servings")), back: q.Get("back"), picked: map[uuid.UUID]bool{},
	}
	if n := atoi(q.Get("set_servings")); n > 0 {
		st.servings = n
	}
	for _, raw := range q["ingredient"] {
		if id, err := uuid.Parse(raw); err == nil {
			st.picked[id] = true
		}
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
	s.sendPatches(w, r, patchSignals(map[string]any{"plandate": v.Days.Date}),
		patchTempl(ui.AddPlanDays(v)), patchTempl(ui.AddPlanSubmit(v)))
}

// addMeal plans a recipe: the add sheet's form, or a suggestion dropped
// on a day.
func (s *Server) addMeal(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
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
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	id := identity(r)
	in := plan.AddInput{
		RecipeID: recipeID, Date: dateParam(f.get("date")), Servings: f.int("servings"), By: id.UserID, Lang: i18n.Locale(ctx),
	}
	picked := map[uuid.UUID]bool{}
	for _, raw := range f.all("ingredient") {
		ingID, err := uuid.Parse(raw)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
		in.Ingredients = append(in.Ingredients, ingID)
		picked[ingID] = true
	}
	added, err := s.plan.Add(ctx, sc, in)
	var fe plan.FieldErrors
	if errors.As(err, &fe) && !isDatastar(r) {
		form := ui.Form{Errors: map[string]string{}}
		for field, key := range fe {
			form.Errors[field] = i18n.T(ctx, key)
		}
		if _, ok := fe["date"]; ok {
			form.Alert = i18n.T(ctx, "plan.addTo.pickDay")
		}
		s.renderAddPlan(w, r, http.StatusUnprocessableEntity, sc, addPlanState{
			recipe: recipeID, date: in.Date, servings: in.Servings, picked: picked, back: f.get("back"), form: form,
		})
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
	msg := i18n.T(ctx, "plan.toast.planned", added.Meal.Recipe.Name, shortDate(ctx, added.Meal.Date))
	switch n := len(added.Shopping.Added); n {
	case 0:
	case 1:
		msg += " · " + i18n.T(ctx, "plan.toast.onListOne", added.Shopping.List.Name)
	default:
		msg += " · " + i18n.T(ctx, "plan.toast.onList", n, added.Shopping.List.Name)
	}
	entries := make([]string, len(added.Shopping.Added))
	for i, e := range added.Shopping.Added {
		entries[i] = e.ID.String()
	}
	undo := map[string]string{}
	if len(entries) > 0 {
		undo["entries"] = strings.Join(entries, ",")
	}
	back := localPath(f.get("back"), weekHref(cal.WeekOf(added.Meal.Date)))
	s.planDone(w, r, back, toast(msg, "/plan/meals/"+added.Meal.ID.String()+"/unplan", undo))
}

// unplanMeal is the Undo of planning a meal: it removes the meal and the
// entries planning it put on the list.
func (s *Server) unplanMeal(w http.ResponseWriter, r *http.Request) {
	mealID, sc, ok := s.mealTarget(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	m, err := s.plan.Remove(ctx, sc, mealID)
	if s.planError(w, r, err) {
		return
	}
	if raw := r.PostFormValue("entries"); raw != "" {
		for _, e := range strings.Split(raw, ",") {
			entryID, err := uuid.Parse(e)
			if err != nil {
				http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
				return
			}
			// An entry already checked off or removed stays as it is.
			if _, err := s.groceries.Remove(ctx, sc, entryID); err != nil && !errors.Is(err, groceries.ErrNotFound) {
				s.serverError(w, r, err)
				return
			}
		}
	}
	s.planDone(w, r, localPath(r.PostFormValue("back"), s.weekBack(ctx, sc, m.Date)), clearToast)
}

// ---- Recipe picker ----

func (s *Server) pickView(ctx context.Context, sc store.Scoped, id *auth.Identity, date store.Date, q, back string) (ui.PlanPickView, error) {
	cal, err := s.plan.Calendar(ctx, sc)
	if err != nil {
		return ui.PlanPickView{}, err
	}
	if date.IsZero() {
		date = cal.Today(s.now())
	}
	back = localPath(back, weekHref(cal.WeekOf(date)))
	v := ui.PlanPickView{Date: date.String(), DayLabel: fullDate(ctx, date), Query: q, Back: back}
	cards, err := s.pickCards(ctx, sc, id, q)
	if err != nil {
		return ui.PlanPickView{}, err
	}
	if q == "" {
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
				Href: addHref(sg.Recipe.ID, date, back),
			})
		}
	}
	for _, c := range cards {
		v.Recipes = append(v.Recipes, ui.PickRecipe{
			ID: c.ID.String(), Name: c.Name, Photo: c.Photo, Meta: cardMeta(ctx, c), Href: addHref(c.ID, date, back),
		})
	}
	if q != "" {
		v.Status = plural(ctx, len(v.Recipes), "recipes.resultsOne", "recipes.results")
	}
	return v, nil
}

// pickCards lists every recipe by name, or the ones matching q.
func (s *Server) pickCards(ctx context.Context, sc store.Scoped, id *auth.Identity, q string) ([]recipes.Card, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return s.recipes.Library(ctx, sc, id.UserID, recipes.Filter{Sort: recipes.SortName})
	}
	res, err := s.recipes.Search(ctx, sc, id.UserID, q)
	if err != nil {
		return nil, err
	}
	cards := make([]recipes.Card, 0, len(res.Names)+len(res.Ingredients))
	for _, n := range res.Names {
		cards = append(cards, n.Card)
	}
	for _, m := range res.Ingredients {
		cards = append(cards, m.Card)
	}
	return cards, nil
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
	v, err := s.pickView(ctx, sc, identity(r), dateParam(q.Get("date")), q.Get("q"), q.Get("back"))
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
	v, err := s.pickView(ctx, sc, identity(r), dateParam(q.Get("date")), sig.Q, q.Get("back"))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.sendPatches(w, r, patchTempl(ui.PlanPickResults(v)))
}

// ---- Meal menu ----

// mealMenuState is what the meal menu shows.
type mealMenuState struct {
	view     string
	week     store.Date
	moveTo   store.Date
	servings int
	query    string
	back     string
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
		ID: m.ID.String(), RecipeID: m.Recipe.ID.String(), Name: m.Recipe.Name, Photo: m.Recipe.Photo,
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
		cards, err := s.pickCards(ctx, sc, id, st.query)
		if err != nil {
			return ui.MealMenuView{}, err
		}
		for _, c := range cards {
			if c.ID == m.Recipe.ID {
				continue
			}
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
		servings: atoi(q.Get("servings")), query: q.Get("q"), back: q.Get("back"),
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
		s.sendPatches(w, r,
			patchSignals(map[string]any{"movedate": v.Move.Date, "mealservings": v.Servings, "swapq": v.Query}),
			patchTempl(ui.MealMenuBody(v, true)))
	case "results":
		s.sendPatches(w, r, patchTempl(ui.MealSwapResults(v)))
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
	s.planDone(w, r, localPath(r.PostFormValue("back"), s.weekBack(ctx, sc, ps[0].Date)), clearToast)
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
	s.planDone(w, r, localPath(r.PostFormValue("back"), "/plan"), clearToast)
}

// ---- Cook events ----

// markCooked records "I made this" for today.
func (s *Server) markCooked(w http.ResponseWriter, r *http.Request) {
	recipeID, sc, ok := s.recipeTarget(w, r)
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
	back := localPath(r.PostFormValue("back"), ui.RecipeHref(recipeID.String()))
	s.planDone(w, r, back, toast(i18n.T(ctx, "plan.toast.cooked"),
		"/recipes/"+recipeID.String()+"/cooked/"+event.String()+"/undo", map[string]string{"back": back}))
}

func (s *Server) undoCooked(w http.ResponseWriter, r *http.Request) {
	recipeID, sc, ok := s.recipeTarget(w, r)
	if !ok {
		return
	}
	eventID, err := uuid.Parse(r.PathValue("event"))
	if err != nil {
		s.notFound(w, r)
		return
	}
	if s.planError(w, r, s.plan.UndoCooked(r.Context(), sc, eventID)) {
		return
	}
	s.planDone(w, r, localPath(r.PostFormValue("back"), ui.RecipeHref(recipeID.String())), clearToast)
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
		return &ui.TonightCard{RecipeID: r.ID.String(), Name: r.Name, Photo: r.Photo,
			Meta: joinNonEmpty(" · ", minutes(ctx, r.TotalMin), i18n.T(ctx, "plan.tonight.planned"))}, nil
	case t.Suggestion != nil:
		r := t.Suggestion.Recipe
		return &ui.TonightCard{RecipeID: r.ID.String(), Name: r.Name, Photo: r.Photo,
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
