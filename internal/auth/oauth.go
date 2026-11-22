package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"net"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/store"
)

// OAuth 2.1 lets MCP clients act for a member without the member copying
// an API token: the client registers itself (RFC 7591), sends the member's
// browser to the consent page, and trades the code it gets back, with its
// PKCE verifier, for an access and a refresh token. A grant acts as its
// member in the household it was approved in, with the scope the member
// picked, like an API token.

// Token prefixes, so secret scanners and people can tell them apart from
// API tokens.
const (
	AccessTokenPrefix  = TokenPrefix + "at_"
	RefreshTokenPrefix = TokenPrefix + "rt_"
)

// Grant lifetimes.
const (
	// CodeTTL is how long an authorization code can be exchanged.
	CodeTTL = 10 * time.Minute
	// AccessTTL is how long an access token lasts.
	AccessTTL = time.Hour
	// RefreshTTL is how long a grant lasts without being refreshed; every
	// refresh restarts it.
	RefreshTTL = 90 * 24 * time.Hour
	// UnusedClientTTL is how long a registered client nobody approved is
	// kept.
	UnusedClientTTL = 24 * time.Hour
)

const (
	maxClientNameLen   = 100
	maxRedirectURIs    = 10
	maxRedirectURILen  = 2000
	maxGrantsPerMember = 50
)

// OAuth errors.
var (
	// ErrOAuthGrant is OAuth's invalid_grant: the code or refresh token is
	// unknown, expired, used, revoked or was issued to another client, or
	// the PKCE verifier or redirect URI does not match.
	ErrOAuthGrant = errors.New("auth: invalid OAuth grant")
	// ErrOAuthClient means the client_id is unknown.
	ErrOAuthClient = errors.New("auth: unknown OAuth client")
	// ErrOAuthRedirect means the redirect URI is not one the client
	// registered.
	ErrOAuthRedirect = errors.New("auth: redirect URI not registered")
	// ErrTooManyGrants means the member has approved too many clients.
	ErrTooManyGrants = errors.New("auth: too many OAuth grants")
)

// ClientMetadataError rejects a client registration. Code is RFC 7591's
// error code (invalid_redirect_uri or invalid_client_metadata).
type ClientMetadataError struct {
	Code        string
	Description string
}

func (e *ClientMetadataError) Error() string { return "auth: " + e.Code + ": " + e.Description }

// OAuthClient is a registered client.
type OAuthClient struct {
	ID           uuid.UUID
	Name         string
	RedirectURIs []string
}

// RegisterClient registers a public client: one that authenticates with
// PKCE alone, without a secret. Redirect URIs must be https, http on a
// loopback address, or a private-use scheme (cursor://...). An empty name
// becomes "MCP client".
func (s *Service) RegisterClient(ctx context.Context, name string, redirectURIs []string) (OAuthClient, error) {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		name = "MCP client"
	}
	if utf8.RuneCountInString(name) > maxClientNameLen {
		name = string([]rune(name)[:maxClientNameLen])
	}
	if len(redirectURIs) == 0 || len(redirectURIs) > maxRedirectURIs {
		return OAuthClient{}, &ClientMetadataError{"invalid_redirect_uri", "Register between 1 and 10 redirect URIs."}
	}
	for _, u := range redirectURIs {
		if err := checkRedirectURI(u); err != nil {
			return OAuthClient{}, err
		}
	}
	c := OAuthClient{ID: store.NewID(), Name: name, RedirectURIs: redirectURIs}
	now := s.clock()
	err := s.db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
		_, err := q.Exec(ctx, `INSERT INTO oauth_clients (id, name, redirect_uris, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
			c.ID, c.Name, strings.Join(c.RedirectURIs, "\n"), now, now)
		return err
	})
	if err != nil {
		return OAuthClient{}, err
	}
	return c, nil
}

// blockedSchemes can run code or read local files when a browser follows
// them.
var blockedSchemes = map[string]bool{"javascript": true, "data": true, "file": true, "vbscript": true, "blob": true, "about": true}

func checkRedirectURI(raw string) error {
	bad := func(msg string) error { return &ClientMetadataError{"invalid_redirect_uri", msg} }
	if len(raw) > maxRedirectURILen || strings.ContainsAny(raw, "\r\n") {
		return bad("Redirect URIs must be single lines under 2000 characters.")
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() {
		return bad("Redirect URIs must be absolute URIs.")
	}
	if u.Fragment != "" || strings.Contains(raw, "#") {
		return bad("Redirect URIs must not have a fragment.")
	}
	switch scheme := strings.ToLower(u.Scheme); {
	case scheme == "https":
		if u.Host == "" {
			return bad("https redirect URIs need a host.")
		}
	case scheme == "http":
		if !isLoopback(u.Hostname()) {
			return bad("http redirect URIs must point to a loopback address; use https otherwise.")
		}
	case blockedSchemes[scheme]:
		return bad("This redirect URI scheme is not allowed.")
	}
	return nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// OAuthClientByID returns a registered client, or ErrOAuthClient.
func (s *Service) OAuthClientByID(ctx context.Context, id uuid.UUID) (OAuthClient, error) {
	var row struct {
		ID           uuid.UUID
		Name         string
		RedirectURIs string `db:"redirect_uris"`
	}
	err := s.db.System().Read().Get(ctx, &row, `SELECT id, name, redirect_uris FROM oauth_clients WHERE id = ?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return OAuthClient{}, ErrOAuthClient
	}
	if err != nil {
		return OAuthClient{}, err
	}
	return OAuthClient{ID: row.ID, Name: row.Name, RedirectURIs: strings.Split(row.RedirectURIs, "\n")}, nil
}

// RedirectAllowed reports whether uri is one of the client's redirect
// URIs. Loopback http URIs match on any port, since native apps listen on
// whichever port is free (RFC 8252 §7.3); everything else must match
// exactly.
func (c OAuthClient) RedirectAllowed(uri string) bool {
	for _, r := range c.RedirectURIs {
		if r == uri {
			return true
		}
	}
	got, err := url.Parse(uri)
	if err != nil || got.Scheme != "http" || !isLoopback(got.Hostname()) {
		return false
	}
	for _, r := range c.RedirectURIs {
		want, err := url.Parse(r)
		if err == nil && want.Scheme == "http" && want.Hostname() == got.Hostname() &&
			want.EscapedPath() == got.EscapedPath() && want.RawQuery == got.RawQuery {
			return true
		}
	}
	return false
}

// AuthorizeInput is a member's consent to a client.
type AuthorizeInput struct {
	ClientID    uuid.UUID
	RedirectURI string
	// CodeChallenge is the PKCE S256 challenge.
	CodeChallenge string
	Scope         TokenScope
}

// ValidCodeChallenge reports whether c looks like an S256 challenge: the
// unpadded base64url encoding of a SHA-256 hash.
func ValidCodeChallenge(c string) bool {
	b, err := base64.RawURLEncoding.DecodeString(c)
	return err == nil && len(b) == sha256.Size
}

// Authorize records the signed-in member's consent and returns the
// authorization code for the client's redirect. It returns ErrOAuthClient,
// ErrOAuthRedirect, ErrOAuthGrant for a malformed challenge or scope, and
// ErrTooManyGrants.
func (s *Service) Authorize(ctx context.Context, id *Identity, in AuthorizeInput) (string, error) {
	client, err := s.OAuthClientByID(ctx, in.ClientID)
	if err != nil {
		return "", err
	}
	if !client.RedirectAllowed(in.RedirectURI) {
		return "", ErrOAuthRedirect
	}
	if !ValidCodeChallenge(in.CodeChallenge) || !in.Scope.Valid() {
		return "", ErrOAuthGrant
	}
	code := randomToken("")
	now := s.now()
	err = s.db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
		var n int
		if err := q.Get(ctx, &n, `SELECT count(*) FROM oauth_grants WHERE household_id = ? AND user_id = ?`, id.HouseholdID, id.UserID); err != nil {
			return err
		}
		if n >= maxGrantsPerMember {
			return ErrTooManyGrants
		}
		if _, err := q.Exec(ctx, `INSERT INTO oauth_grants (id, household_id, user_id, client_id, scope, code_hash, code_challenge,
				redirect_uri, code_expires_at, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			store.NewID(), id.HouseholdID, id.UserID, client.ID, string(in.Scope), hashToken(code), in.CodeChallenge,
			in.RedirectURI, store.T(now.Add(CodeTTL)), store.T(now), store.T(now)); err != nil {
			return err
		}
		_, err := q.Exec(ctx, `UPDATE oauth_clients SET last_used_at = ?, updated_at = ? WHERE id = ?`, store.T(now), store.T(now), client.ID)
		return err
	})
	if err != nil {
		return "", err
	}
	return code, nil
}

// TokenPair is what the token endpoint hands out.
type TokenPair struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    time.Duration
	Scope        TokenScope
}

// grantRow is a grant with its member's state.
type grantRow struct {
	ID               uuid.UUID
	ClientID         uuid.UUID
	Scope            TokenScope
	CodeChallenge    string
	RedirectURI      string
	CodeExpiresAt    store.Time
	CodeUsedAt       *store.Time
	RefreshHash      *string
	RefreshExpiresAt *store.Time
	RemovedAt        *store.Time
}

const grantColumns = `g.id, g.client_id, g.scope, g.code_challenge, g.redirect_uri, g.code_expires_at, g.code_used_at,
	g.refresh_hash, g.refresh_expires_at, m.removed_at
	FROM oauth_grants g JOIN memberships m ON m.household_id = g.household_id AND m.user_id = g.user_id`

// ExchangeCode trades an authorization code for tokens. The code works
// once: presenting it again revokes the grant, since someone else may have
// seen it (OAuth 2.1 §4.1.3). Every failure is ErrOAuthGrant.
func (s *Service) ExchangeCode(ctx context.Context, clientID uuid.UUID, code, redirectURI, verifier string) (TokenPair, error) {
	var pair TokenPair
	replayed := uuid.Nil
	err := s.db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
		var g grantRow
		err := q.Get(ctx, &g, `SELECT `+grantColumns+` WHERE g.code_hash = ?`, hashToken(code))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrOAuthGrant
		}
		if err != nil {
			return err
		}
		if g.CodeUsedAt != nil {
			replayed = g.ID
			return ErrOAuthGrant
		}
		now := s.now()
		if g.ClientID != clientID || g.RedirectURI != redirectURI || !now.Before(g.CodeExpiresAt.Time) ||
			g.RemovedAt != nil || !verifierMatches(verifier, g.CodeChallenge) {
			return ErrOAuthGrant
		}
		pair, err = s.issueTokens(ctx, q, g, now, `code_used_at = ?, `, store.T(now))
		return err
	})
	if replayed != uuid.Nil {
		if err := s.revokeGrant(ctx, replayed); err != nil {
			return TokenPair{}, err
		}
	}
	return pair, err
}

// RefreshOAuth trades a refresh token for a new pair; the old refresh
// token stops working. Presenting a replaced refresh token revokes the
// grant, since it was either stolen or the client is confused. Every
// failure is ErrOAuthGrant.
func (s *Service) RefreshOAuth(ctx context.Context, clientID uuid.UUID, refresh string) (TokenPair, error) {
	if !strings.HasPrefix(refresh, RefreshTokenPrefix) {
		return TokenPair{}, ErrOAuthGrant
	}
	h := hashToken(refresh)
	var pair TokenPair
	replayed := uuid.Nil
	err := s.db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
		var g grantRow
		err := q.Get(ctx, &g, `SELECT `+grantColumns+` WHERE g.refresh_hash = ? OR g.previous_refresh_hash = ?`, h, h)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrOAuthGrant
		}
		if err != nil {
			return err
		}
		if g.RefreshHash == nil || *g.RefreshHash != h {
			replayed = g.ID
			return ErrOAuthGrant
		}
		now := s.now()
		if g.ClientID != clientID || g.RefreshExpiresAt == nil || !now.Before(g.RefreshExpiresAt.Time) || g.RemovedAt != nil {
			return ErrOAuthGrant
		}
		pair, err = s.issueTokens(ctx, q, g, now, `previous_refresh_hash = refresh_hash, `)
		return err
	})
	if replayed != uuid.Nil {
		if err := s.revokeGrant(ctx, replayed); err != nil {
			return TokenPair{}, err
		}
	}
	return pair, err
}

// issueTokens gives grant g a new access and refresh token. set and args
// add assignments to the update.
func (s *Service) issueTokens(ctx context.Context, q store.Querier, g grantRow, now time.Time, set string, args ...any) (TokenPair, error) {
	pair := TokenPair{
		AccessToken: randomToken(AccessTokenPrefix), RefreshToken: randomToken(RefreshTokenPrefix),
		ExpiresIn: AccessTTL, Scope: g.Scope,
	}
	args = append(args, hashToken(pair.AccessToken), store.T(now.Add(AccessTTL)), hashToken(pair.RefreshToken),
		store.T(now.Add(RefreshTTL)), store.T(now), g.ID)
	// The current refresh hash in the condition makes a concurrent refresh
	// with the same token lose instead of forking the grant.
	cond := `refresh_hash IS NULL`
	if g.RefreshHash != nil {
		cond = `refresh_hash = ?`
		args = append(args, *g.RefreshHash)
	}
	res, err := q.Exec(ctx, `UPDATE oauth_grants SET `+set+`access_hash = ?, access_expires_at = ?, refresh_hash = ?,
		refresh_expires_at = ?, updated_at = ? WHERE id = ? AND `+cond, args...)
	if err != nil {
		return TokenPair{}, err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		if err == nil {
			err = ErrOAuthGrant
		}
		return TokenPair{}, err
	}
	return pair, nil
}

func (s *Service) revokeGrant(ctx context.Context, id uuid.UUID) error {
	return s.db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
		_, err := q.Exec(ctx, `DELETE FROM oauth_grants WHERE id = ?`, id)
		return err
	})
}

// RevokeOAuthToken ends the grant an access or refresh token belongs to,
// if the client holds it (RFC 7009). Unknown tokens are not an error.
func (s *Service) RevokeOAuthToken(ctx context.Context, clientID uuid.UUID, token string) error {
	h := hashToken(token)
	return s.db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
		_, err := q.Exec(ctx, `DELETE FROM oauth_grants WHERE client_id = ? AND (access_hash = ? OR refresh_hash = ?)`, clientID, h, h)
		return err
	})
}

// authenticateAccessToken resolves an OAuth access token like
// AuthenticateToken.
func (s *Service) authenticateAccessToken(ctx context.Context, token string) (*Identity, error) {
	var row struct {
		sessionRow
		GrantID    uuid.UUID
		Scope      TokenScope
		AccessExp  store.Time
		LastUsedAt *store.Time
	}
	err := s.db.System().Read().Get(ctx, &row, `
		SELECT g.id AS grant_id, g.user_id, g.household_id, g.scope, g.access_expires_at AS access_exp, g.last_used_at,
		       m.role, m.removed_at,
		       u.username, u.name, u.email, u.avatar_color, u.theme, u.language, u.units,
		       h.name AS household_name, h.default_language AS household_language, h.timezone, h.week_start
		FROM oauth_grants g
		JOIN memberships m ON m.household_id = g.household_id AND m.user_id = g.user_id
		JOIN users u ON u.id = g.user_id
		JOIN households h ON h.id = g.household_id
		WHERE g.access_hash = ?`, hashToken(token))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoToken
	}
	if err != nil {
		return nil, err
	}
	now := s.now()
	if row.RemovedAt != nil || !now.Before(row.AccessExp.Time) {
		return nil, ErrNoToken
	}
	if row.LastUsedAt == nil || now.Sub(row.LastUsedAt.Time) >= tokenTouchInterval {
		err := s.db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
			_, err := q.Exec(ctx, `UPDATE oauth_grants SET last_used_at = ? WHERE id = ?`, store.T(now), row.GrantID)
			return err
		})
		if err != nil {
			return nil, err
		}
	}
	id := row.identity()
	id.TokenID = row.GrantID
	id.APIScope = row.Scope
	return id, nil
}

// OAuthGrantInfo describes an approved client for the account page.
type OAuthGrantInfo struct {
	ID         uuid.UUID
	ClientName string
	Scope      TokenScope
	CreatedAt  time.Time
	LastUsedAt *time.Time
}

// OAuthGrants lists the clients the user approved in their household,
// newest first. Codes not yet exchanged are left out.
func (s *Service) OAuthGrants(ctx context.Context, id *Identity) ([]OAuthGrantInfo, error) {
	var rows []struct {
		ID         uuid.UUID
		ClientName string
		Scope      TokenScope
		CreatedAt  store.Time
		LastUsedAt *store.Time
	}
	err := s.db.System().Read().Select(ctx, &rows, `SELECT g.id, c.name AS client_name, g.scope, g.created_at, g.last_used_at
		FROM oauth_grants g JOIN oauth_clients c ON c.id = g.client_id
		WHERE g.household_id = ? AND g.user_id = ? AND g.code_used_at IS NOT NULL
		ORDER BY g.created_at DESC, g.id`, id.HouseholdID, id.UserID)
	if err != nil {
		return nil, err
	}
	out := make([]OAuthGrantInfo, 0, len(rows))
	for _, r := range rows {
		g := OAuthGrantInfo{ID: r.ID, ClientName: r.ClientName, Scope: r.Scope, CreatedAt: r.CreatedAt.Time}
		if r.LastUsedAt != nil {
			v := r.LastUsedAt.Time
			g.LastUsedAt = &v
		}
		out = append(out, g)
	}
	return out, nil
}

// RevokeOAuthGrant ends one of the user's grants. Unknown IDs and other
// users' grants return ErrNotFound.
func (s *Service) RevokeOAuthGrant(ctx context.Context, id *Identity, grantID uuid.UUID) error {
	return s.db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
		res, err := q.Exec(ctx, `DELETE FROM oauth_grants WHERE id = ? AND household_id = ? AND user_id = ?`,
			grantID, id.HouseholdID, id.UserID)
		if err != nil {
			return err
		}
		return mustAffect(res)
	})
}

// randomToken returns prefix and 32 random bytes, base64url-encoded.
func randomToken(prefix string) string {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // never fails; see crypto/rand.Read
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}

// verifierMatches checks a PKCE verifier against an S256 challenge.
func verifierMatches(verifier, challenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	return subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(challenge)) == 1
}

// dropTokens deletes the account's API tokens and OAuth grants in every
// household.
func dropTokens(ctx context.Context, q store.Querier, userID uuid.UUID) error {
	if _, err := q.Exec(ctx, `DELETE FROM api_tokens WHERE user_id = ?`, userID); err != nil {
		return err
	}
	_, err := q.Exec(ctx, `DELETE FROM oauth_grants WHERE user_id = ?`, userID)
	return err
}

// dropMemberTokens deletes a member's API tokens and OAuth grants in one
// household.
func dropMemberTokens(ctx context.Context, q store.Querier, householdID, userID uuid.UUID) error {
	if _, err := q.Exec(ctx, `DELETE FROM api_tokens WHERE household_id = ? AND user_id = ?`, householdID, userID); err != nil {
		return err
	}
	_, err := q.Exec(ctx, `DELETE FROM oauth_grants WHERE household_id = ? AND user_id = ?`, householdID, userID)
	return err
}
