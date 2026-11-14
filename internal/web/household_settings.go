package web

import (
	"context"
	"errors"
	"net/http"

	"github.com/raphi011/mise/internal/auth"
	"github.com/raphi011/mise/internal/discover"
	"github.com/raphi011/mise/internal/i18n"
	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/ui"
)

// ---- Household (Settings › Household, admins) ----

const householdSettingsPath = "/settings/household"

func (s *Server) householdSettingsPage(w http.ResponseWriter, r *http.Request) {
	s.renderHouseholdSettings(w, r, http.StatusOK, ui.Form{}, nil)
}

// renderHouseholdSettings renders the page with form and the sites in on,
// or with the stored settings when form is empty.
func (s *Server) renderHouseholdSettings(w http.ResponseWriter, r *http.Request, status int, form ui.Form, on map[string]bool) {
	ctx := r.Context()
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	v, err := s.householdSettingsView(ctx, sc, form, on)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if status == http.StatusOK {
		v.Notice = noticeFor(r)
	}
	p := s.page(r, i18n.T(ctx, "household.settings.title"), ui.NavSettings)
	p.BackHref, p.BackLabel, p.Settings = "/settings", i18n.T(ctx, "settings.back"), "household"
	s.render(w, r, status, ui.HouseholdSettings(p, v))
}

func (s *Server) householdSettingsView(ctx context.Context, sc store.Scoped, form ui.Form, on map[string]bool) (ui.HouseholdSettingsView, error) {
	sites, err := s.discover.Settings(ctx, sc)
	if err != nil {
		return ui.HouseholdSettingsView{}, err
	}
	if form.Values == nil {
		h, err := s.auth.Household(ctx, sc)
		if err != nil {
			return ui.HouseholdSettingsView{}, err
		}
		form = ui.Form{Values: map[string]string{
			"household": h.Name, "timezone": h.Timezone, "week_start": h.WeekStart, "language": h.Language,
		}}
	}
	v := ui.HouseholdSettingsView{Form: form}
	for _, tag := range i18n.Supported {
		v.Languages = append(v.Languages, ui.Option{Value: tag.String(), Label: i18n.T(i18n.WithLocale(ctx, tag), "language.self")})
	}
	v.SiteGroups = siteGroups(ctx, sites, on)
	return v, nil
}

// siteGroups groups the sites by language, in site order, each marked on
// as stored or, after a failed save, as submitted (on non-nil).
func siteGroups(ctx context.Context, sites []discover.SiteSetting, on map[string]bool) []ui.SiteGroup {
	var groups []ui.SiteGroup
	index := map[string]int{}
	for _, st := range sites {
		code := siteLanguage(st.Site)
		i, ok := index[code]
		if !ok {
			i = len(groups)
			index[code] = i
			groups = append(groups, ui.SiteGroup{Lang: code, Name: languageName(ctx, st.Site.Language())})
		}
		enabled := st.Enabled
		if on != nil {
			enabled = on[st.Site.Name()]
		}
		groups[i].Sites = append(groups[i].Sites, ui.SiteToggle{Name: st.Site.Name(), On: enabled})
	}
	return groups
}

func (s *Server) saveHouseholdSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	f := formOf(r, "household", "timezone", "week_start", "language")
	if err := r.ParseForm(); err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	sites := r.PostForm["sites"]
	err = s.auth.UpdateHousehold(ctx, sc, identity(r).UserID, auth.HouseholdSettings{
		Name: f.Value("household"), Timezone: f.Value("timezone"), WeekStart: f.Value("week_start"), Language: f.Value("language"),
	})
	if f, ok := withErrors(ctx, f, err); ok {
		on := map[string]bool{}
		for _, name := range sites {
			on[name] = true
		}
		s.renderHouseholdSettings(w, r, http.StatusUnprocessableEntity, f, on)
		return
	}
	if errors.Is(err, auth.ErrForbidden) {
		s.adminError(w, r, err)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	// The sites are saved after the language, so a site's choice is kept
	// against the new language's default.
	if err := s.discover.SaveSettings(ctx, sc, sites); err != nil {
		s.serverError(w, r, err)
		return
	}
	seeOther(w, r, householdSettingsPath+"?done=saved")
}
