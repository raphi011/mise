// Package config loads server configuration from flags and MISE_* environment
// variables. Flags take precedence over the environment.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/mail"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config is the resolved server configuration.
type Config struct {
	// DB is the database URL: sqlite://path or postgres://...
	DB string
	// DataDir holds images, the SQLite file and generated secrets.
	DataDir string
	// Listen is the HTTP listen address.
	Listen string
	// BaseURL is the public URL. Optional until first-run setup stores one.
	BaseURL *url.URL
	// ExtraBaseURLs are further public URLs mise is reached at, e.g. a
	// tailnet name next to the public one. Single sign-on returns to the
	// one the request came in on; links in emails and shares use BaseURL.
	ExtraBaseURLs []*url.URL
	// TrustedProxies are the networks whose X-Forwarded-For is trusted.
	TrustedProxies []netip.Prefix
	// LogFormat is "json" or "text".
	LogFormat string
	// LogLevel is the minimum log level.
	LogLevel slog.Level
	// SMTP configures outgoing email; without a host there is none.
	SMTP SMTP
	// SecretKey is the base64 AES-256 key for stored API keys; empty means
	// the key generated into the data directory.
	SecretKey string
	// OIDC configures single sign-on; without an issuer there is none.
	OIDC OIDC
}

// OIDC is the OpenID Connect provider users may sign in with, besides
// their password.
type OIDC struct {
	// Issuer is the provider's issuer URL, exactly as its discovery
	// document states it.
	Issuer       string
	ClientID     string
	ClientSecret string
	// Name labels the sign-in button, e.g. "Pocket ID".
	Name string
}

// Enabled reports whether single sign-on is configured.
func (o OIDC) Enabled() bool { return o.Issuer != "" }

// SMTP TLS modes.
const (
	TLSStartTLS = "starttls"
	TLSImplicit = "tls"
	TLSNone     = "none"
)

// SMTP is the mail server invites and password resets are sent through.
type SMTP struct {
	Host string
	Port int
	// TLS is TLSStartTLS, TLSImplicit or TLSNone.
	TLS string
	// SkipVerify accepts any server certificate, for a relay on a trusted
	// network that presents a self-signed one.
	SkipVerify bool
	Username   string
	Password   string
	// From is the sender, e.g. "mise <mise@example.com>".
	From *mail.Address
}

// Enabled reports whether email is configured.
func (s SMTP) Enabled() bool { return s.Host != "" }

// Load parses args (without the program name). Values not given as flags
// are taken from getenv, then from built-in defaults.
//
// Flag defaults are always the literal built-in defaults: environment values
// are applied after parsing, so usage output (-h, bad flags) never shows
// them. MISE_DB in particular may carry a password.
func Load(args []string, getenv func(string) string) (Config, error) {
	return load(args, getenv, os.Stderr)
}

// load is Load with usage and flag errors written to out.
func load(args []string, getenv func(string) string, out io.Writer) (Config, error) {
	fs := flag.NewFlagSet("mise-server", flag.ContinueOnError)
	fs.SetOutput(out)
	envVars := map[string]string{} // flag name -> environment variable
	str := func(name, envVar, def, usage string) *string {
		envVars[name] = envVar
		return fs.String(name, def, usage+" ("+envVar+")")
	}
	dataDir := str("data-dir", "MISE_DATA_DIR", "./data", "data directory")
	db := str("db", "MISE_DB", "", "database URL, sqlite://path or postgres://...; default sqlite in data dir")
	listen := str("listen", "MISE_LISTEN", ":8080", "listen address")
	baseURL := str("base-url", "MISE_BASE_URL", "", "public base URL")
	extraBaseURLs := str("extra-base-urls", "MISE_EXTRA_BASE_URLS", "", "comma-separated further public URLs mise is reached at, e.g. a tailnet name")
	proxies := str("trusted-proxies", "MISE_TRUSTED_PROXIES", "", "comma-separated CIDRs of trusted reverse proxies")
	logFormat := str("log-format", "MISE_LOG_FORMAT", "json", "log format, json or text")
	logLevel := str("log-level", "MISE_LOG_LEVEL", "info", "log level")
	smtpHost := str("smtp-host", "MISE_SMTP_HOST", "", "SMTP server for invite and password-reset emails; unset disables email")
	smtpPort := str("smtp-port", "MISE_SMTP_PORT", "587", "SMTP port")
	smtpTLS := str("smtp-tls", "MISE_SMTP_TLS", TLSStartTLS, "SMTP TLS mode: starttls, tls (implicit) or none")
	smtpSkipVerify := str("smtp-tls-skip-verify", "MISE_SMTP_TLS_SKIP_VERIFY", "false", "accept any SMTP server certificate")
	smtpUser := str("smtp-username", "MISE_SMTP_USERNAME", "", "SMTP username")
	smtpPass := str("smtp-password", "MISE_SMTP_PASSWORD", "", "SMTP password")
	secretKey := str("secret-key", "MISE_SECRET_KEY", "", "base64 32-byte key for stored API keys; default generated into the data directory")
	smtpFrom := str("smtp-from", "MISE_SMTP_FROM", "", "sender address, e.g. 'mise <mise@example.com>'")
	oidcIssuer := str("oidc-issuer", "MISE_OIDC_ISSUER", "", "OpenID Connect issuer URL; unset disables single sign-on")
	oidcClientID := str("oidc-client-id", "MISE_OIDC_CLIENT_ID", "", "OpenID Connect client ID")
	oidcSecret := str("oidc-client-secret", "MISE_OIDC_CLIENT_SECRET", "", "OpenID Connect client secret")
	oidcSecretFile := str("oidc-client-secret-file", "MISE_OIDC_CLIENT_SECRET_FILE", "", "file holding the OpenID Connect client secret")
	oidcName := str("oidc-name", "MISE_OIDC_NAME", "SSO", "provider name on the sign-in button")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if fs.NArg() > 0 {
		return Config{}, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}

	// Flags win over the environment: only fill in flags not given.
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	for name, envVar := range envVars {
		if set[name] {
			continue
		}
		if v := getenv(envVar); v != "" {
			if err := fs.Set(name, v); err != nil {
				return Config{}, fmt.Errorf("%s: %w", envVar, err)
			}
		}
	}

	cfg := Config{
		DataDir:   *dataDir,
		DB:        *db,
		Listen:    *listen,
		LogFormat: *logFormat,
		SecretKey: *secretKey,
	}
	if cfg.DB == "" {
		cfg.DB = "sqlite://" + filepath.Join(cfg.DataDir, "mise.db")
	}

	var errs []error
	if *baseURL != "" {
		u, err := ParseBaseURL(*baseURL)
		if err != nil {
			errs = append(errs, err)
		} else {
			cfg.BaseURL = u
		}
	}
	extras, err := parseExtraBaseURLs(*extraBaseURLs, cfg.BaseURL)
	errs = append(errs, err)
	cfg.ExtraBaseURLs = extras
	for p := range strings.SplitSeq(*proxies, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		prefix, err := parsePrefix(p)
		if err != nil {
			errs = append(errs, fmt.Errorf("trusted proxy %q: %w", p, err))
			continue
		}
		cfg.TrustedProxies = append(cfg.TrustedProxies, prefix)
	}
	if cfg.LogFormat != "json" && cfg.LogFormat != "text" {
		errs = append(errs, fmt.Errorf("log format %q: must be json or text", cfg.LogFormat))
	}
	if err := cfg.LogLevel.UnmarshalText([]byte(*logLevel)); err != nil {
		errs = append(errs, fmt.Errorf("log level: %w", err))
	}
	if *smtpHost != "" {
		smtp, err := parseSMTP(*smtpHost, *smtpPort, *smtpTLS, *smtpSkipVerify, *smtpUser, *smtpPass, *smtpFrom)
		errs = append(errs, err)
		cfg.SMTP = smtp
	}
	if *oidcIssuer != "" || *oidcClientID != "" || *oidcSecret != "" || *oidcSecretFile != "" {
		o, err := parseOIDC(*oidcIssuer, *oidcClientID, *oidcSecret, *oidcSecretFile, *oidcName)
		errs = append(errs, err)
		cfg.OIDC = o
	}
	return cfg, errors.Join(errs...)
}

// parseExtraBaseURLs parses a comma-separated list of further public URLs.
// Requests are matched to them by host, so each host appears once, and
// not as base's.
func parseExtraBaseURLs(list string, base *url.URL) ([]*url.URL, error) {
	var urls []*url.URL
	var errs []error
	hosts := map[string]bool{}
	if base != nil {
		hosts[strings.ToLower(base.Host)] = true
	}
	for raw := range strings.SplitSeq(list, ",") {
		if raw = strings.TrimSpace(raw); raw == "" {
			continue
		}
		u, err := ParseBaseURL(raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("extra %w", err))
			continue
		}
		host := strings.ToLower(u.Host)
		if hosts[host] {
			errs = append(errs, fmt.Errorf("extra base URL %q: host %s is listed twice", raw, u.Host))
			continue
		}
		hosts[host] = true
		urls = append(urls, u)
	}
	return urls, errors.Join(errs...)
}

// parseOIDC validates the single sign-on settings given any of them.
func parseOIDC(issuer, clientID, secret, secretFile, name string) (OIDC, error) {
	var errs []error
	if secret != "" && secretFile != "" {
		errs = append(errs, errors.New("OIDC client secret: set the secret or the secret file, not both"))
	}
	if secretFile != "" {
		b, err := os.ReadFile(secretFile)
		if err != nil {
			errs = append(errs, fmt.Errorf("OIDC client secret file: %w", err))
		}
		secret = strings.TrimSpace(string(b))
	}
	if issuer == "" || clientID == "" || secret == "" {
		errs = append(errs, errors.New("OIDC: set the issuer, client ID and client secret together"))
	}
	if issuer != "" {
		u, err := url.Parse(issuer)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
			errs = append(errs, fmt.Errorf("OIDC issuer %q: must be an absolute http(s) URL without query", issuer))
		}
	}
	name = strings.TrimSpace(name)
	if name == "" {
		errs = append(errs, errors.New("OIDC name: must not be blank"))
	}
	o := OIDC{Issuer: issuer, ClientID: clientID, ClientSecret: secret, Name: name}
	if err := errors.Join(errs...); err != nil {
		return OIDC{}, err
	}
	return o, nil
}

// parseSMTP validates the SMTP settings given a host.
func parseSMTP(host, port, tls, skipVerify, user, pass, from string) (SMTP, error) {
	s := SMTP{Host: host, TLS: tls, Username: user, Password: pass}
	var errs []error
	skip, err := strconv.ParseBool(skipVerify)
	switch {
	case err != nil:
		errs = append(errs, fmt.Errorf("SMTP TLS skip verify %q: must be true or false", skipVerify))
	case skip && tls == TLSNone:
		errs = append(errs, errors.New("SMTP TLS skip verify: needs TLS mode starttls or tls"))
	}
	s.SkipVerify = skip
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		errs = append(errs, fmt.Errorf("SMTP port %q: must be 1 to 65535", port))
	}
	s.Port = n
	switch tls {
	case TLSStartTLS, TLSImplicit, TLSNone:
	default:
		errs = append(errs, fmt.Errorf("SMTP TLS %q: must be starttls, tls or none", tls))
	}
	if (user == "") != (pass == "") {
		errs = append(errs, errors.New("SMTP username and password: set both or neither"))
	}
	switch a, err := mail.ParseAddress(from); {
	case from == "":
		errs = append(errs, errors.New("SMTP from: required with an SMTP host"))
	case err != nil:
		errs = append(errs, fmt.Errorf("SMTP from %q: %w", from, err))
	default:
		s.From = a
	}
	return s, errors.Join(errs...)
}

// parsePrefix accepts a CIDR or a single address.
func parsePrefix(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		return p.Masked(), err
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()), nil
}

// ErrBaseURLPath is returned by ParseBaseURL for a URL with a path. Every
// route, asset and cookie lives at the root, so mise needs a host of its
// own (a subdomain works) rather than a path under a shared one.
var ErrBaseURLPath = errors.New("must not have a path: serve mise at the root of its own host, e.g. https://mise.example.com")

// ParseBaseURL validates and normalises a public base URL: absolute http(s)
// with a host, no credentials, query, fragment or path (a lone trailing
// slash is dropped).
func ParseBaseURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return nil, fmt.Errorf("base URL: %w", err)
	case u.Scheme != "http" && u.Scheme != "https", u.Host == "", u.Opaque != "":
		return nil, fmt.Errorf("base URL %q: must be an absolute http(s) URL", raw)
	case u.User != nil, u.RawQuery != "", u.ForceQuery, u.Fragment != "":
		return nil, fmt.Errorf("base URL %q: must not have credentials, a query or a fragment", raw)
	case u.Path != "" && u.Path != "/":
		return nil, fmt.Errorf("base URL %q: %w", raw, ErrBaseURLPath)
	}
	u.Path, u.RawPath = "", ""
	return u, nil
}
