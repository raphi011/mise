package ai_test

import (
	"context"
	"errors"
	"maps"
	"slices"
	"testing"

	"github.com/raphi011/mise/internal/ai"
	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

// fakeModel answers connection tests with err.
type fakeModel struct{ err error }

func (m fakeModel) Test(context.Context) error { return m.err }

func (m fakeModel) Chat(context.Context, ai.Request) (ai.Response, error) {
	return ai.Response{}, errors.New("fakeModel: no chat")
}

type settingsEnv struct {
	s     *ai.Service
	sc    store.Scoped
	hh    storetest.Household
	conns []ai.Connection
	err   error
}

func newSettingsEnv(t *testing.T, db *store.DB, name string) *settingsEnv {
	t.Helper()
	e := &settingsEnv{hh: storetest.NewHousehold(t, db, name)}
	e.sc = db.ScopeFor(e.hh.ID)
	e.s = ai.New(ai.Config{Box: newBox(t), Provider: func(c ai.Connection) (ai.Model, error) {
		e.conns = append(e.conns, c)
		return fakeModel{err: e.err}, nil
	}})
	return e
}

func (e *settingsEnv) save(t *testing.T, in ai.SettingsInput) ai.Settings {
	t.Helper()
	if err := e.s.SaveSettings(t.Context(), e.sc, e.hh.Admin, in); err != nil {
		t.Fatal(err)
	}
	st, err := e.s.Settings(t.Context(), e.sc)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestSettingsDefaultsAndSave(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		e := newSettingsEnv(t, db, "home")
		st, err := e.s.Settings(ctx, e.sc)
		if err != nil {
			t.Fatal(err)
		}
		if st.Enabled || st.Provider != ai.ProviderAnthropic || st.Model != ai.DefaultAnthropicModel || st.Permissions != ai.DefaultPermissions {
			t.Errorf("defaults %+v", st)
		}
		if _, err := e.s.Connection(ctx, e.sc); !errors.Is(err, ai.ErrDisabled) {
			t.Errorf("Connection without settings: %v", err)
		}

		st = e.save(t, ai.SettingsInput{Enabled: true, Provider: ai.ProviderAnthropic, Endpoint: "https://ignored", Model: " claude-opus-5-5 ",
			APIKey: "sk-ant-1", Permissions: ai.Permissions{CreateItem: true}})
		if !st.Enabled || st.Endpoint != "" || st.Model != "claude-opus-5-5" || !st.HasKey || st.KeyUnreadable ||
			st.Permissions != (ai.Permissions{CreateItem: true}) {
			t.Errorf("saved %+v", st)
		}
		c, err := e.s.Connection(ctx, e.sc)
		if err != nil || c.APIKey != "sk-ant-1" || c.Provider != ai.ProviderAnthropic {
			t.Fatalf("Connection = %+v, %v", c, err)
		}

		// A blank key keeps the stored one.
		e.save(t, ai.SettingsInput{Enabled: true, Provider: ai.ProviderAnthropic, Model: "claude-sonnet-5-5"})
		if c, _ := e.s.Connection(ctx, e.sc); c.APIKey != "sk-ant-1" || c.Model != "claude-sonnet-5-5" {
			t.Errorf("after blank key: %+v", c)
		}

		// Turning it off keeps the settings but refuses connections.
		e.save(t, ai.SettingsInput{Provider: ai.ProviderAnthropic, Model: "claude-sonnet-5-5"})
		if _, err := e.s.Connection(ctx, e.sc); !errors.Is(err, ai.ErrDisabled) {
			t.Errorf("Connection while off: %v", err)
		}
	})
}

// A key entered for one server must not be sent to another.
func TestSettingsDropKeyOnNewServer(t *testing.T) {
	e := newSettingsEnv(t, storetest.SQLite(t), "home")
	e.save(t, ai.SettingsInput{Enabled: true, Provider: ai.ProviderOpenAI, Endpoint: "https://api.example.com/v1/", Model: "m", APIKey: "sk-1"})
	st := e.save(t, ai.SettingsInput{Enabled: true, Provider: ai.ProviderOpenAI, Endpoint: "http://192.168.1.5:8000/v1", Model: "m"})
	if st.HasKey {
		t.Error("key kept after the endpoint changed")
	}
	e.save(t, ai.SettingsInput{Enabled: true, Provider: ai.ProviderOpenAI, Endpoint: "http://192.168.1.5:8000/v1", Model: "m", APIKey: "sk-2"})
	st = e.save(t, ai.SettingsInput{Enabled: true, Provider: ai.ProviderOllama, Model: "llama"})
	if st.HasKey || st.Endpoint != ai.DefaultOllamaEndpoint {
		t.Errorf("after switching to Ollama: %+v", st)
	}
	// Anthropic needs a key once enabled.
	err := e.s.SaveSettings(t.Context(), e.sc, e.hh.Admin, ai.SettingsInput{Enabled: true, Provider: ai.ProviderAnthropic, Model: "claude-opus-5-5"})
	var fe ai.FieldErrors
	if !errors.As(err, &fe) || fe["api_key"] == "" {
		t.Errorf("Anthropic without key: %v", err)
	}
}

func TestSettingsValidation(t *testing.T) {
	e := newSettingsEnv(t, storetest.SQLite(t), "home")
	long := string(make([]byte, 3000))
	for _, tc := range []struct {
		in   ai.SettingsInput
		want []string
	}{
		{ai.SettingsInput{Provider: "gemini", Model: "m"}, []string{"provider"}},
		{ai.SettingsInput{Provider: ai.ProviderOpenAI, Model: "m"}, []string{"endpoint"}},
		{ai.SettingsInput{Provider: ai.ProviderOpenAI, Endpoint: "ftp://x", Model: "m"}, []string{"endpoint"}},
		{ai.SettingsInput{Provider: ai.ProviderOpenAI, Endpoint: "https://u:p@x", Model: "m"}, []string{"endpoint"}},
		{ai.SettingsInput{Provider: ai.ProviderOllama, Endpoint: "http://x" + long}, []string{"endpoint", "model"}},
		{ai.SettingsInput{Provider: ai.ProviderAnthropic, Model: "m", APIKey: "two words"}, []string{"api_key"}},
	} {
		err := e.s.SaveSettings(t.Context(), e.sc, e.hh.Admin, tc.in)
		var fe ai.FieldErrors
		if !errors.As(err, &fe) {
			t.Errorf("%+v: %v, want field errors", tc.in, err)
			continue
		}
		if got := slices.Sorted(maps.Keys(fe)); !slices.Equal(got, tc.want) {
			t.Errorf("%+v: fields %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestConnectionTest(t *testing.T) {
	ctx := t.Context()
	e := newSettingsEnv(t, storetest.SQLite(t), "home")
	if err := e.s.TestConnection(ctx, e.sc); !errors.Is(err, ai.ErrDisabled) {
		t.Errorf("test without settings: %v", err)
	}
	e.save(t, ai.SettingsInput{Provider: ai.ProviderOpenAI, Endpoint: "https://api.example.com/v1", Model: "m", APIKey: "sk-1"})
	if err := e.s.TestConnection(ctx, e.sc); err != nil {
		t.Fatal(err)
	}
	if len(e.conns) != 1 || e.conns[0].APIKey != "sk-1" || e.conns[0].Endpoint != "https://api.example.com/v1" {
		t.Errorf("connection used %+v", e.conns)
	}
	st, _ := e.s.Settings(ctx, e.sc)
	if st.TestOK == nil || !*st.TestOK || st.TestedAt == nil {
		t.Errorf("after a good test: %+v", st)
	}
	// Changing only permissions keeps the result; changing the model clears it.
	st = e.save(t, ai.SettingsInput{Provider: ai.ProviderOpenAI, Endpoint: "https://api.example.com/v1", Model: "m", Permissions: ai.Permissions{FetchPage: true}})
	if st.TestOK == nil {
		t.Error("permission change cleared the test result")
	}
	st = e.save(t, ai.SettingsInput{Provider: ai.ProviderOpenAI, Endpoint: "https://api.example.com/v1", Model: "m2"})
	if st.TestOK != nil || st.TestedAt != nil {
		t.Errorf("model change kept the test result: %+v", st)
	}
	e.err = errors.New("401")
	if err := e.s.TestConnection(ctx, e.sc); err == nil {
		t.Fatal("failed test reported success")
	}
	if st, _ := e.s.Settings(ctx, e.sc); st.TestOK == nil || *st.TestOK {
		t.Errorf("after a failed test: %+v", st.TestOK)
	}
}

// A key sealed with another server's key (a restore without secret.key)
// is reported, not returned.
func TestSettingsUnreadableKey(t *testing.T) {
	db := storetest.SQLite(t)
	e := newSettingsEnv(t, db, "home")
	e.save(t, ai.SettingsInput{Enabled: true, Provider: ai.ProviderAnthropic, Model: "m", APIKey: "sk-1"})
	other := ai.New(ai.Config{Box: newBox(t)})
	st, err := other.Settings(t.Context(), e.sc)
	if err != nil || !st.HasKey || !st.KeyUnreadable {
		t.Errorf("Settings = %+v, %v", st, err)
	}
	if _, err := other.Connection(t.Context(), e.sc); !errors.Is(err, ai.ErrUnreadable) {
		t.Errorf("Connection = %v", err)
	}
}

func TestSettingsHouseholdIsolation(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		a, b := newSettingsEnv(t, db, "a"), newSettingsEnv(t, db, "b")
		a.save(t, ai.SettingsInput{Enabled: true, Provider: ai.ProviderAnthropic, Model: "m", APIKey: "sk-a"})
		st, err := b.s.Settings(t.Context(), b.sc)
		if err != nil || st.Enabled || st.HasKey {
			t.Errorf("b sees a's settings: %+v, %v", st, err)
		}
		if err := b.s.RecordTest(t.Context(), b.sc, true); err != nil {
			t.Fatal(err)
		}
		if st, _ := a.s.Settings(t.Context(), a.sc); st.TestOK != nil {
			t.Error("b's test result landed on a")
		}
	})
}
