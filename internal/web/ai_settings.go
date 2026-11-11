package web

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/raphi011/mise/internal/ai"
	"github.com/raphi011/mise/internal/i18n"
	"github.com/raphi011/mise/internal/ui"
)

// ---- AI import (Settings › AI import, admins) ----

func (s *Server) aiSettingsPage(w http.ResponseWriter, r *http.Request) {
	s.renderAISettings(w, r, http.StatusOK, ui.Form{}, "")
}

// renderAISettings renders the page with form, or with the stored settings
// when form is empty. testErr is the failed connection test's message.
func (s *Server) renderAISettings(w http.ResponseWriter, r *http.Request, status int, form ui.Form, testErr string) {
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
	v := ui.AISettingsView{Form: form, HasKey: st.HasKey, KeyUnreadable: st.KeyUnreadable, TestError: testErr}
	v.Status, v.StatusClass = connectionStatus(st)
	if status == http.StatusOK && testErr == "" {
		switch r.URL.Query().Get("done") {
		case "saved":
			v.Notice = i18n.T(ctx, "notice.saved")
		case "tested":
			v.Notice = i18n.T(ctx, "ai.settings.testOK")
		}
	}
	for _, p := range ai.Providers {
		v.Providers = append(v.Providers, ui.Option{Value: string(p), Label: i18n.T(ctx, providerLabel(p))})
	}
	p := s.page(r, i18n.T(ctx, "ai.settings.title"), ui.NavSettings)
	p.BackHref, p.BackLabel, p.Settings = "/settings#library", i18n.T(ctx, "settings.back"), "library"
	s.render(w, r, status, ui.AISettings(p, v))
}

func (s *Server) saveAISettings(w http.ResponseWriter, r *http.Request) {
	if s.storeAISettings(w, r) {
		seeOther(w, r, "/settings/ai?done=saved")
	}
}

// testAISettings saves the form, so the test uses what the admin sees,
// then sends one short request with it.
func (s *Server) testAISettings(w http.ResponseWriter, r *http.Request) {
	if !s.storeAISettings(w, r) {
		return
	}
	ctx := r.Context()
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	tctx, cancel := context.WithTimeout(ctx, aiTestTimeout)
	defer cancel()
	err = s.ai.TestConnection(tctx, sc)
	if err == nil {
		seeOther(w, r, "/settings/ai?done=tested")
		return
	}
	if ctx.Err() != nil {
		return
	}
	var pe *ai.ProviderError
	switch {
	case errors.As(err, &pe):
		s.renderAISettings(w, r, http.StatusOK, ui.Form{}, pe.Message)
	case errors.Is(err, context.DeadlineExceeded):
		s.renderAISettings(w, r, http.StatusOK, ui.Form{}, i18n.T(ctx, "ai.settings.testTimeout"))
	case errors.Is(err, ai.ErrUnreadable):
		s.renderAISettings(w, r, http.StatusOK, ui.Form{}, i18n.T(ctx, "ai.settings.keyUnreadable"))
	default:
		s.serverError(w, r, err)
	}
}

// aiTestTimeout bounds a connection test; a cold local model can take a
// while to load.
const aiTestTimeout = 60 * time.Second

// storeAISettings saves the submitted settings. It reports false when it
// has answered the request itself (invalid form, error).
func (s *Server) storeAISettings(w http.ResponseWriter, r *http.Request) bool {
	ctx := r.Context()
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return false
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
		s.renderAISettings(w, r, http.StatusUnprocessableEntity, f, "")
		return false
	}
	if err != nil {
		s.serverError(w, r, err)
		return false
	}
	return true
}

// connectionStatus is the label beside "Model connection".
func connectionStatus(st ai.Settings) (i18n.Key, string) {
	switch {
	case !st.Enabled:
		return "ai.status.off", "conn-status--off"
	case st.TestOK == nil:
		return "ai.status.untested", "conn-status--untested"
	case *st.TestOK:
		return "ai.status.connected", "conn-status--ok"
	}
	return "ai.status.failed", "conn-status--failed"
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
