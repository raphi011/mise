package auth

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/store"
)

// Server setting keys.
const (
	settingBaseURL = "base_url"
	// settingSetupAt is inserted first in the setup transaction. Its primary
	// key serialises concurrent setups on Postgres, where two transactions
	// could otherwise both see zero households.
	settingSetupAt = "setup_completed_at"
)

// NeedsSetup reports whether no household exists yet.
func (s *Service) NeedsSetup(ctx context.Context) (bool, error) {
	if s.setupDone.Load() {
		return false, nil
	}
	var n int
	if err := s.db.System().Read().QueryRow(ctx, `SELECT count(*) FROM households`).Scan(&n); err != nil {
		return false, err
	}
	if n > 0 {
		s.setupDone.Store(true)
	}
	return n == 0, nil
}

// SetupInput is the first-run setup form.
type SetupInput struct {
	HouseholdName string
	Timezone      string
	WeekStart     string
	// Language is the household's default UI language.
	Language string
	BaseURL  string

	Name      string
	Username  string
	Email     string
	Password  string
	UserAgent string
}

// SetupResult is what a successful setup produced.
type SetupResult struct {
	// Token is the admin's new session token.
	Token string
	// BaseURL is the normalised base URL that was stored.
	BaseURL *url.URL
}

// ParseBaseURL validates and normalises a public base URL: absolute http(s),
// no query or fragment, no trailing slash.
func ParseBaseURL(raw string) (*url.URL, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, false
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	u.RawPath = ""
	return u, true
}

func (in *SetupInput) normalise() {
	in.HouseholdName = clean(in.HouseholdName)
	in.Timezone = clean(in.Timezone)
	in.Name = clean(in.Name)
	in.Username = clean(in.Username)
	in.Email = clean(in.Email)
}

// Setup creates the first household, its admin and the stored base URL in
// one transaction and signs the admin in. Once any household exists it
// returns ErrSetupDone, also when two setups race.
func (s *Service) Setup(ctx context.Context, in SetupInput) (SetupResult, error) {
	in.normalise()
	errs := FieldErrors{}
	checkName(errs, "household", in.HouseholdName)
	checkTimezone(errs, "timezone", in.Timezone)
	checkChoice(errs, "week_start", in.WeekStart, []string{"mon", "sun"})
	base, ok := ParseBaseURL(in.BaseURL)
	if !ok {
		errs["base_url"] = "form.error.baseURL"
	}
	checkName(errs, "name", in.Name)
	checkUsername(errs, "username", in.Username)
	checkEmail(errs, "email", in.Email)
	checkNewPassword(errs, "password", in.Password)
	if err := errs.orNil(); err != nil {
		return SetupResult{}, err
	}
	if !checkLanguage(in.Language) {
		in.Language = Languages()[0]
	}

	if done, err := s.NeedsSetup(ctx); err != nil {
		return SetupResult{}, err
	} else if !done {
		return SetupResult{}, ErrSetupDone
	}

	hash, err := HashPassword(in.Password)
	if err != nil {
		return SetupResult{}, err
	}

	var res SetupResult
	err = s.db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
		now := s.clock()
		r, err := q.Exec(ctx, `INSERT INTO server_settings (key, value, created_at, updated_at) VALUES (?, ?, ?, ?)
			ON CONFLICT (key) DO NOTHING`, settingSetupAt, now.Format(time.RFC3339), now, now)
		if err != nil {
			return err
		}
		if n, err := r.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return ErrSetupDone
		}
		var households int
		if err := q.QueryRow(ctx, `SELECT count(*) FROM households`).Scan(&households); err != nil {
			return err
		}
		if households > 0 {
			return ErrSetupDone
		}

		householdID, userID := store.NewID(), store.NewID()
		if _, err := q.Exec(ctx, `INSERT INTO households (id, name, timezone, week_start, default_language, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, householdID, in.HouseholdName, in.Timezone, in.WeekStart, in.Language, now, now); err != nil {
			return err
		}
		if err := insertUser(ctx, q, newUser{
			ID: userID, Username: in.Username, Name: in.Name, Email: in.Email,
			PasswordHash: hash, AvatarColor: AvatarColors[0], At: now,
		}); err != nil {
			return err
		}
		if err := insertMembership(ctx, q, householdID, userID, RoleAdmin, now); err != nil {
			return err
		}
		if s.onSetup != nil {
			if err := s.onSetup(ctx, q, s.db.ScopeFor(householdID), in.Language); err != nil {
				return err
			}
		}
		if err := putSetting(ctx, q, settingBaseURL, base.String(), now); err != nil {
			return err
		}
		res.Token, err = s.createSession(ctx, q, userID, householdID, in.UserAgent)
		return err
	})
	if err != nil {
		return SetupResult{}, err
	}
	s.setupDone.Store(true)
	res.BaseURL = base
	return res, nil
}

func checkLanguage(lang string) bool { return slices.Contains(Languages(), lang) }

type newUser struct {
	ID           uuid.UUID
	Username     string
	Name         string
	Email        string
	PasswordHash string
	AvatarColor  string
	At           store.Time
}

// insertUser inserts a user and returns FieldErrors for a taken username.
func insertUser(ctx context.Context, q store.Querier, u newUser) error {
	r, err := q.Exec(ctx, `INSERT INTO users (id, username, username_key, name, email, password_hash, avatar_color, theme, language, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'system', NULL, ?, ?)
		ON CONFLICT (username_key) DO NOTHING`,
		u.ID, u.Username, UsernameKey(u.Username), u.Name, nullable(u.Email), u.PasswordHash, u.AvatarColor, u.At, u.At)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return FieldErrors{"username": "form.error.usernameTaken"}
	}
	return nil
}

func insertMembership(ctx context.Context, q store.Querier, householdID, userID uuid.UUID, role Role, at store.Time) error {
	_, err := q.Exec(ctx, `INSERT INTO memberships (household_id, user_id, role, removed_at, created_at, updated_at)
		VALUES (?, ?, ?, NULL, ?, ?)`, householdID, userID, role, at, at)
	return err
}

func putSetting(ctx context.Context, q store.Querier, key, value string, at store.Time) error {
	_, err := q.Exec(ctx, `INSERT INTO server_settings (key, value, created_at, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, key, value, at, at)
	return err
}

// StoredBaseURL returns the base URL saved during setup, or nil if there is
// none.
func (s *Service) StoredBaseURL(ctx context.Context) (*url.URL, error) {
	var v string
	err := s.db.System().Read().Get(ctx, &v, `SELECT value FROM server_settings WHERE key = ?`, settingBaseURL)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	u, ok := ParseBaseURL(v)
	if !ok {
		return nil, errors.New("auth: stored base URL is invalid: " + v)
	}
	return u, nil
}
