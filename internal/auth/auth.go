// Package auth owns accounts: first-run setup, passwords, sessions, invites,
// household membership and roles, and the HTTP middleware that turns a
// session cookie into a household-scoped request context.
//
// Global tables (users, sessions, invites looked up by token,
// server_settings) are accessed through store.DB.System. Operations on one
// household's members and invites take a store.Scoped built from the request
// and filter every query on its household.
package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"time"
	_ "time/tzdata" // timezone validation must work in minimal containers

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/i18n"
	"github.com/raphi011/mise/internal/store"
)

// Durations of the account lifecycle.
const (
	// SessionTTL is how long a session lives after it was last seen.
	SessionTTL = 30 * 24 * time.Hour
	// SessionTouchInterval limits how often last_seen_at and expires_at are
	// written for one session, to spare SQLite's single writer.
	SessionTouchInterval = 5 * time.Minute
	// InviteTTL is how long an invite link stays valid.
	InviteTTL = 7 * 24 * time.Hour
)

// Errors returned by Service methods. Validation problems are returned as
// FieldErrors instead.
var (
	// ErrInvalidCredentials means the username or password is wrong, or the
	// account has no active membership. The cases are not distinguished.
	ErrInvalidCredentials = errors.New("auth: invalid username or password")
	// ErrNoSession means the session token is unknown, expired or belongs to
	// a removed member.
	ErrNoSession = errors.New("auth: no valid session")
	// ErrSetupDone means first-run setup was attempted after a household
	// exists.
	ErrSetupDone = errors.New("auth: setup already completed")
	// ErrInviteInvalid means the invite token is unknown, expired, revoked or
	// already used.
	ErrInviteInvalid = errors.New("auth: invite invalid or expired")
	// ErrLastAdmin means the change would leave the household without an
	// admin.
	ErrLastAdmin = errors.New("auth: household needs at least one admin")
	// ErrSelf means an admin action was aimed at the acting admin, where the
	// account page is the right place (password) or it is not allowed
	// (removal).
	ErrSelf = errors.New("auth: not allowed on your own account")
	// ErrSharedAccount means the account also belongs to another household.
	// Its password is account-wide, so one household's admin may not set it.
	ErrSharedAccount = errors.New("auth: account belongs to another household too")
	// ErrNotFound means the member or invite does not exist in this
	// household.
	ErrNotFound = errors.New("auth: not found")
	// ErrForbidden means the acting user lacks the admin role.
	ErrForbidden = errors.New("auth: admin role required")
)

// FieldErrors maps form field names to the message describing what is wrong
// with them. It is returned by methods that validate input.
type FieldErrors map[string]i18n.Key

func (e FieldErrors) Error() string {
	fields := make([]string, 0, len(e))
	for f := range e {
		fields = append(fields, f)
	}
	slices.Sort(fields)
	return "auth: invalid " + strings.Join(fields, ", ")
}

// Fields returns the messages by field.
func (e FieldErrors) Fields() map[string]i18n.Key { return e }

// orNil returns e, or nil when it is empty, so callers can return it as an
// error directly.
func (e FieldErrors) orNil() error {
	if len(e) == 0 {
		return nil
	}
	return e
}

// Role is a member's role in a household.
type Role string

// Roles.
const (
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
)

// Valid reports whether r is a known role.
func (r Role) Valid() bool { return r == RoleAdmin || r == RoleMember }

// AvatarColors is the fixed palette users pick their avatar colour from.
// The UI maps each name to a token pair for light and dark mode.
var AvatarColors = []string{"amber", "sky", "sand", "herb", "coral", "plum"}

// Themes a user can choose; "system" follows the OS preference.
var Themes = []string{"system", "light", "dark"}

// Config configures a Service.
type Config struct {
	// Now returns the current time; defaults to time.Now. Tests use it to
	// move the clock.
	Now func() time.Time
	// Log receives errors the middleware cannot return to a caller.
	Log *slog.Logger
	// SecureCookie reports whether the session cookie for r must carry the
	// Secure attribute (base URL or request is https).
	SecureCookie func(r *http.Request) bool
	// OnSetup, if set, runs inside the first-run setup transaction after the
	// household and its admin exist, to create the household's initial data
	// (e.g. grocery defaults) in its default language. An error aborts setup.
	OnSetup func(ctx context.Context, q store.Querier, sc store.Scoped, language string) error
}

// Service implements accounts on top of the store.
type Service struct {
	db     *store.DB
	now    func() time.Time
	log    *slog.Logger
	secure func(*http.Request) bool
	// onSetup is Config.OnSetup.
	onSetup func(ctx context.Context, q store.Querier, sc store.Scoped, language string) error
	// setupDone caches that a household exists; households are never
	// deleted, so once true it stays true.
	setupDone atomic.Bool
}

// New returns a Service using db.
func New(db *store.DB, cfg Config) *Service {
	s := &Service{db: db, now: cfg.Now, log: cfg.Log, secure: cfg.SecureCookie, onSetup: cfg.OnSetup}
	if s.now == nil {
		s.now = time.Now
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.secure == nil {
		s.secure = func(r *http.Request) bool { return r.TLS != nil }
	}
	return s
}

// Now returns the service's current time, so callers showing expiry use the
// same clock as the checks.
func (s *Service) Now() time.Time { return s.now() }

func (s *Service) clock() store.Time { return store.T(s.now()) }

// Identity is the signed-in user in their active household, as loaded from
// the session for every request.
type Identity struct {
	SessionID   string
	UserID      uuid.UUID
	HouseholdID uuid.UUID
	Role        Role

	Username    string
	Name        string
	Email       string
	AvatarColor string
	Theme       string
	// Language is the user's UI language, or empty to use the household
	// default.
	Language string

	HouseholdName     string
	HouseholdLanguage string
	Timezone          string
	WeekStart         string
}

// IsAdmin reports whether the user is an admin of the active household.
func (id *Identity) IsAdmin() bool { return id.Role == RoleAdmin }
