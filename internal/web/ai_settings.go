package web

import (
	"net/http"

	"github.com/raphi011/mise/internal/ai"
	"github.com/raphi011/mise/internal/i18n"
	"github.com/raphi011/mise/internal/ui"
)

// ---- AI import (Settings › AI import, admins) ----

func (s *Server) aiSettingsPage(w http.ResponseWriter, r *http.Request) {
	s.renderAISettings(w, r, http.StatusOK, ui.Form{})
}

// renderAISettings renders the page with form, or with the stored settings
// when form is empty.
func (s *Server) renderAISettings(w http.ResponseWriter, r *http.Request, status int, form ui.Form) {
	ctx := r.Context()
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	st, err := s.ai.Settings(ctx, sc)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if form.Values == nil {
		form = ui.Form{Values: map[string]string{
			"provider": string(st.Provider),
			"endpoint": st.Endpoint,
			"model":    st.Model,
		}}
		set := func(name string, on bool) {
			if on {
				form.Values[name] = "on"
			}
		}
		set("enabled", st.Enabled)
		set("allow_fetch_page", st.Permissions.FetchPage)
		set("allow_search_items", st.Permissions.SearchItems)
		set("allow_create_item", st.Permissions.CreateItem)
	}
	v := ui.AISettingsView{Form: form, HasKey: st.HasKey, KeyUnreadable: st.KeyUnreadable}
	if r.URL.Query().Get("done") == "saved" && status == http.StatusOK {
		v.Notice = i18n.T(ctx, "notice.saved")
	}
	for _, p := range ai.Providers {
		v.Providers = append(v.Providers, ui.Option{Value: string(p), Label: i18n.T(ctx, providerLabel(p))})
	}
	p := s.page(r, i18n.T(ctx, "ai.settings.title"), ui.NavSettings)
	p.BackHref, p.BackLabel = "/settings#recipes", i18n.T(ctx, "settings.back")
	s.render(w, r, status, ui.AISettings(p, v))
}

func (s *Server) saveAISettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	f := formOf(r, "enabled", "provider", "endpoint", "model", "allow_fetch_page", "allow_search_items", "allow_create_item")
	on := func(name string) bool { return f.Value(name) != "" }
	err = s.ai.SaveSettings(ctx, sc, identity(r).UserID, ai.SettingsInput{
		Enabled:  on("enabled"),
		Provider: ai.Provider(f.Value("provider")),
		Endpoint: f.Value("endpoint"),
		Model:    f.Value("model"),
		APIKey:   r.PostFormValue("api_key"),
		Permissions: ai.Permissions{
			FetchPage:   on("allow_fetch_page"),
			SearchItems: on("allow_search_items"),
			CreateItem:  on("allow_create_item"),
		},
	})
	if f, ok := withErrors(ctx, f, err); ok {
		s.renderAISettings(w, r, http.StatusUnprocessableEntity, f)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	seeOther(w, r, "/settings/ai?done=saved")
}

// providerLabel is the settings page's name for a provider.
func providerLabel(p ai.Provider) i18n.Key {
	switch p {
	case ai.ProviderOpenAI:
		return "ai.provider.openai"
	case ai.ProviderOllama:
		return "ai.provider.ollama"
	}
	return "ai.provider.anthropic"
}
