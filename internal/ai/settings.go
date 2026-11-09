// Package ai imports recipes from links and pasted text: per-household
// settings with an encrypted API key, a fetcher that refuses private
// addresses, the schema.org JSON-LD extractor, model providers, the tool
// loop and the import job runner (docs/plan.md, AI import).
//
// Every method takes a store.Scoped and filters every query on its
// household; the job runner builds one with store.ScopeFor.
package ai

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/i18n"
	"github.com/raphi011/mise/internal/store"
)

// Provider names a kind of model API.
type Provider string

// Providers.
const (
	ProviderAnthropic Provider = "anthropic"
	ProviderOpenAI    Provider = "openai"
	ProviderOllama    Provider = "ollama"
)

// Providers lists the providers in the order the settings page offers them.
var Providers = []Provider{ProviderAnthropic, ProviderOpenAI, ProviderOllama}

// Defaults for a household that has not saved settings, and per provider.
const (
	DefaultAnthropicModel = "claude-opus-5-5"
	DefaultOllamaEndpoint = "http://localhost:11434/v1"
)

// Field limits.
const (
	maxEndpoint = 2048
	maxModel    = 200
	maxAPIKey   = 1000
)

// FieldErrors maps form field names to the message describing what is wrong
// with them.
type FieldErrors map[string]i18n.Key

func (e FieldErrors) Error() string {
	fields := make([]string, 0, len(e))
	for f := range e {
		fields = append(fields, f)
	}
	slices.Sort(fields)
	return "ai: invalid " + strings.Join(fields, ", ")
}

// Fields returns the messages by field.
func (e FieldErrors) Fields() map[string]i18n.Key { return e }

// Permissions are the tool switches. create_recipe is always allowed.
type Permissions struct {
	FetchPage   bool
	SearchItems bool
	CreateItem  bool
}

// DefaultPermissions are the conservative defaults from the plan.
var DefaultPermissions = Permissions{FetchPage: true, SearchItems: true}

// Settings are a household's AI settings as the settings page shows them.
// The API key itself is never part of them.
type Settings struct {
	Enabled  bool
	Provider Provider
	Endpoint string
	Model    string
	// HasKey is set when a key is stored; KeyUnreadable when it is stored
	// but can't be decrypted with this server's key.
	HasKey        bool
	KeyUnreadable bool
	Permissions   Permissions
	// TestedAt and TestOK are the last connection test, cleared whenever
	// the connection changes.
	TestedAt *time.Time
	TestOK   *bool
}

// DefaultSettings are the settings of a household that never saved any.
func DefaultSettings() Settings {
	return Settings{Provider: ProviderAnthropic, Model: DefaultAnthropicModel, Permissions: DefaultPermissions}
}

// Connection is what a provider client needs, with the decrypted key.
type Connection struct {
	Provider Provider
	Endpoint string
	Model    string
	APIKey   string
}

// SettingsInput is a submitted settings form.
type SettingsInput struct {
	Enabled  bool
	Provider Provider
	Endpoint string
	Model    string
	// APIKey replaces the stored key when not empty; empty keeps it.
	APIKey      string
	Permissions Permissions
}

// Settings returns the household's settings, or DefaultSettings.
func (s *Service) Settings(ctx context.Context, sc store.Scoped) (Settings, error) {
	row, err := s.settingsRow(ctx, sc.Read(), sc)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultSettings(), nil
	}
	if err != nil {
		return Settings{}, err
	}
	st := row.settings()
	if row.APIKeyEnc != nil {
		if _, err := s.box.Open(sc.HouseholdID(), *row.APIKeyEnc); err != nil {
			st.KeyUnreadable = true
		}
	}
	return st, nil
}

// Connection returns the connection of an enabled household, with the key
// decrypted. ErrDisabled when AI is off, ErrUnreadable when the key can't
// be decrypted.
func (s *Service) Connection(ctx context.Context, sc store.Scoped) (Connection, error) {
	row, err := s.settingsRow(ctx, sc.Read(), sc)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !row.Enabled) {
		return Connection{}, ErrDisabled
	}
	if err != nil {
		return Connection{}, err
	}
	return s.connection(sc, row)
}

func (s *Service) connection(sc store.Scoped, row settingsRow) (Connection, error) {
	c := Connection{Provider: row.Provider, Endpoint: row.Endpoint, Model: row.Model}
	if row.APIKeyEnc != nil {
		key, err := s.box.Open(sc.HouseholdID(), *row.APIKeyEnc)
		if err != nil {
			return Connection{}, err
		}
		c.APIKey = key
	}
	return c, nil
}

// ErrDisabled means the household has not turned AI import on.
var ErrDisabled = errors.New("ai: AI import is off")

// SaveSettings validates and stores in. Changing the provider or endpoint
// drops a stored key unless a new one is given, so a key is never sent to
// a server it was not entered for. Any change to the connection clears the
// last test result.
func (s *Service) SaveSettings(ctx context.Context, sc store.Scoped, by uuid.UUID, in SettingsInput) error {
	in, err := normalise(in)
	if err != nil {
		return err
	}
	now := store.T(s.now())
	return sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		old, err := s.settingsRow(ctx, q.Reader, sc)
		exists := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		keyEnc := old.APIKeyEnc
		if exists && (old.Provider != in.Provider || old.Endpoint != in.Endpoint) {
			keyEnc = nil
		}
		if in.APIKey != "" {
			sealed := s.box.Seal(sc.HouseholdID(), in.APIKey)
			keyEnc = &sealed
		}
		if in.Enabled && in.Provider == ProviderAnthropic && keyEnc == nil {
			return FieldErrors{"api_key": "ai.error.keyRequired"}
		}
		testedAt, testOK := old.TestedAt, old.TestOK
		if !exists || old.Provider != in.Provider || old.Endpoint != in.Endpoint || old.Model != in.Model || in.APIKey != "" || !sameKey(old.APIKeyEnc, keyEnc) {
			testedAt, testOK = nil, nil
		}
		_, err = q.Exec(ctx, `
			INSERT INTO ai_settings (household_id, enabled, provider, endpoint, model, api_key_enc,
				allow_fetch_page, allow_search_items, allow_create_item, tested_at, test_ok, updated_by, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (household_id) DO UPDATE SET
				enabled = excluded.enabled, provider = excluded.provider, endpoint = excluded.endpoint,
				model = excluded.model, api_key_enc = excluded.api_key_enc,
				allow_fetch_page = excluded.allow_fetch_page, allow_search_items = excluded.allow_search_items,
				allow_create_item = excluded.allow_create_item, tested_at = excluded.tested_at,
				test_ok = excluded.test_ok, updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
			sc.HouseholdID(), in.Enabled, string(in.Provider), in.Endpoint, in.Model, keyEnc,
			in.Permissions.FetchPage, in.Permissions.SearchItems, in.Permissions.CreateItem,
			testedAt, testOK, by, now, now)
		if err != nil {
			return fmt.Errorf("ai: save settings: %w", err)
		}
		return nil
	})
}

func sameKey(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// RecordTest stores the result of a connection test.
func (s *Service) RecordTest(ctx context.Context, sc store.Scoped, ok bool) error {
	now := store.T(s.now())
	return sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		_, err := q.Exec(ctx, `UPDATE ai_settings SET tested_at = ?, test_ok = ?, updated_at = ? WHERE household_id = ?`,
			now, ok, now, sc.HouseholdID())
		if err != nil {
			return fmt.Errorf("ai: record test: %w", err)
		}
		return nil
	})
}

// TestConnection tries the saved connection with one tiny request and
// records the result. It works whether or not import is enabled.
func (s *Service) TestConnection(ctx context.Context, sc store.Scoped) error {
	row, err := s.settingsRow(ctx, sc.Read(), sc)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDisabled
	}
	if err != nil {
		return err
	}
	c, err := s.connection(sc, row)
	if err != nil {
		return err
	}
	p, err := s.newProvider(c)
	if err == nil {
		err = p.Test(ctx)
	}
	if rerr := s.RecordTest(ctx, sc, err == nil); rerr != nil {
		return rerr
	}
	return err
}

// normalise trims and validates in.
func normalise(in SettingsInput) (SettingsInput, error) {
	in.Endpoint = strings.TrimSpace(in.Endpoint)
	in.Model = strings.TrimSpace(in.Model)
	in.APIKey = strings.TrimSpace(in.APIKey)
	errs := FieldErrors{}
	switch in.Provider {
	case ProviderAnthropic:
		in.Endpoint = ""
	case ProviderOllama:
		if in.Endpoint == "" {
			in.Endpoint = DefaultOllamaEndpoint
		}
	case ProviderOpenAI:
		if in.Endpoint == "" {
			errs["endpoint"] = "ai.error.endpointRequired"
		}
	default:
		errs["provider"] = "ai.error.provider"
	}
	if in.Endpoint != "" {
		u, err := url.Parse(in.Endpoint)
		switch {
		case len(in.Endpoint) > maxEndpoint:
			errs["endpoint"] = "ai.error.endpointLong"
		case err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil:
			errs["endpoint"] = "ai.error.endpoint"
		default:
			in.Endpoint = strings.TrimSuffix(in.Endpoint, "/")
		}
	}
	switch {
	case in.Model == "":
		errs["model"] = "ai.error.modelRequired"
	case len(in.Model) > maxModel:
		errs["model"] = "ai.error.modelLong"
	}
	if len(in.APIKey) > maxAPIKey || strings.ContainsAny(in.APIKey, " \t\r\n") {
		errs["api_key"] = "ai.error.key"
	}
	if len(errs) > 0 {
		return in, errs
	}
	return in, nil
}

type settingsRow struct {
	Enabled          bool
	Provider         Provider
	Endpoint         string
	Model            string
	APIKeyEnc        *string
	AllowFetchPage   bool
	AllowSearchItems bool
	AllowCreateItem  bool
	TestedAt         *store.Time
	TestOK           *bool
}

func (r settingsRow) settings() Settings {
	st := Settings{
		Enabled:  r.Enabled,
		Provider: r.Provider,
		Endpoint: r.Endpoint,
		Model:    r.Model,
		HasKey:   r.APIKeyEnc != nil,
		Permissions: Permissions{
			FetchPage:   r.AllowFetchPage,
			SearchItems: r.AllowSearchItems,
			CreateItem:  r.AllowCreateItem,
		},
		TestOK: r.TestOK,
	}
	if r.TestedAt != nil {
		t := r.TestedAt.Time
		st.TestedAt = &t
	}
	return st
}

func (s *Service) settingsRow(ctx context.Context, q store.Reader, sc store.Scoped) (settingsRow, error) {
	var row settingsRow
	err := q.Get(ctx, &row, `
		SELECT enabled, provider, endpoint, model, api_key_enc, allow_fetch_page, allow_search_items,
			allow_create_item, tested_at, test_ok
		FROM ai_settings WHERE household_id = ?`, sc.HouseholdID())
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return row, fmt.Errorf("ai: settings: %w", err)
	}
	return row, err
}
