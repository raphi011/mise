// Package sso signs users in with an OpenID Connect provider: the
// authorization code flow with PKCE, and ID token verification. It knows
// nothing about accounts; auth links the verified identity to one.
package sso

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// ErrUnavailable means the provider's discovery document could not be
// loaded; a later attempt retries.
var ErrUnavailable = errors.New("sso: provider unavailable")

// requestTimeout bounds each call to the provider.
const requestTimeout = 15 * time.Second

// Config is the provider and the client registered with it.
type Config struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	// HTTPClient talks to the provider; nil means one with requestTimeout.
	HTTPClient *http.Client
}

// Client signs users in with one provider. Discovery happens on first use
// and is retried until it succeeds, so the server starts while the
// provider is down.
type Client struct {
	cfg  Config
	http *http.Client

	mu       sync.Mutex
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
}

// New returns a Client; it does not contact the provider.
func New(cfg Config) *Client {
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: requestTimeout}
	}
	return &Client{cfg: cfg, http: hc}
}

// Issuer returns the provider's issuer URL.
func (c *Client) Issuer() string { return c.cfg.Issuer }

func (c *Client) discover(ctx context.Context) (*oidc.Provider, *oidc.IDTokenVerifier, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.provider != nil {
		return c.provider, c.verifier, nil
	}
	ctx, cancel := context.WithTimeout(oidc.ClientContext(ctx, c.http), requestTimeout)
	defer cancel()
	p, err := oidc.NewProvider(ctx, c.cfg.Issuer)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	c.provider, c.verifier = p, p.Verifier(&oidc.Config{ClientID: c.cfg.ClientID})
	return c.provider, c.verifier, nil
}

func (c *Client) oauth2(p *oidc.Provider, redirectURL string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     c.cfg.ClientID,
		ClientSecret: c.cfg.ClientSecret,
		Endpoint:     p.Endpoint(),
		RedirectURL:  redirectURL,
		Scopes:       []string{oidc.ScopeOpenID, "email", "profile"},
	}
}

// Flow is one sign-in attempt's secrets, kept in the browser between
// AuthURL and Exchange.
type Flow struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
}

// NewFlow returns fresh random secrets.
func NewFlow() Flow {
	return Flow{State: random(), Nonce: random(), Verifier: oauth2.GenerateVerifier()}
}

func random() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// AuthURL returns the provider URL that starts the sign-in.
func (c *Client) AuthURL(ctx context.Context, redirectURL string, f Flow) (string, error) {
	p, _, err := c.discover(ctx)
	if err != nil {
		return "", err
	}
	return c.oauth2(p, redirectURL).AuthCodeURL(f.State, oidc.Nonce(f.Nonce), oauth2.S256ChallengeOption(f.Verifier)), nil
}

// Identity is the verified ID token's subject and email.
type Identity struct {
	Issuer        string
	Subject       string
	Email         string
	EmailVerified bool
}

// Exchange redeems the code returned to the callback and verifies the ID
// token: signature, issuer, audience, expiry and the flow's nonce. The
// caller has already checked the returned state against f.State.
func (c *Client) Exchange(ctx context.Context, redirectURL string, f Flow, code string) (Identity, error) {
	p, verifier, err := c.discover(ctx)
	if err != nil {
		return Identity{}, err
	}
	ctx, cancel := context.WithTimeout(context.WithValue(ctx, oauth2.HTTPClient, c.http), requestTimeout)
	defer cancel()
	tok, err := c.oauth2(p, redirectURL).Exchange(ctx, code, oauth2.VerifierOption(f.Verifier))
	if err != nil {
		return Identity{}, fmt.Errorf("sso: exchange code: %w", err)
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok || raw == "" {
		return Identity{}, errors.New("sso: token response has no ID token")
	}
	idt, err := verifier.Verify(ctx, raw)
	if err != nil {
		return Identity{}, fmt.Errorf("sso: verify ID token: %w", err)
	}
	if idt.Nonce != f.Nonce {
		return Identity{}, errors.New("sso: ID token nonce does not match")
	}
	var claims struct {
		Email string `json:"email"`
		// EmailVerified is any so a provider sending it as a string reads as
		// unverified rather than failing the sign-in.
		EmailVerified any `json:"email_verified"`
	}
	if err := idt.Claims(&claims); err != nil {
		return Identity{}, fmt.Errorf("sso: ID token claims: %w", err)
	}
	return Identity{
		Issuer:        idt.Issuer,
		Subject:       idt.Subject,
		Email:         claims.Email,
		EmailVerified: claims.EmailVerified == true,
	}, nil
}
