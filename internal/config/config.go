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
	// TrustedProxies are the networks whose X-Forwarded-For is trusted.
	TrustedProxies []netip.Prefix
	// LogFormat is "json" or "text".
	LogFormat string
	// LogLevel is the minimum log level.
	LogLevel slog.Level
	// SMTP configures outgoing email; without a host there is none.
	SMTP SMTP
}

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
	TLS      string
	Username string
	Password string
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
	proxies := str("trusted-proxies", "MISE_TRUSTED_PROXIES", "", "comma-separated CIDRs of trusted reverse proxies")
	logFormat := str("log-format", "MISE_LOG_FORMAT", "json", "log format, json or text")
	logLevel := str("log-level", "MISE_LOG_LEVEL", "info", "log level")
	smtpHost := str("smtp-host", "MISE_SMTP_HOST", "", "SMTP server for invite and password-reset emails; unset disables email")
	smtpPort := str("smtp-port", "MISE_SMTP_PORT", "587", "SMTP port")
	smtpTLS := str("smtp-tls", "MISE_SMTP_TLS", TLSStartTLS, "SMTP TLS mode: starttls, tls (implicit) or none")
	smtpUser := str("smtp-username", "MISE_SMTP_USERNAME", "", "SMTP username")
	smtpPass := str("smtp-password", "MISE_SMTP_PASSWORD", "", "SMTP password")
	smtpFrom := str("smtp-from", "MISE_SMTP_FROM", "", "sender address, e.g. 'mise <mise@example.com>'")
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
	}
	if cfg.DB == "" {
		cfg.DB = "sqlite://" + filepath.Join(cfg.DataDir, "mise.db")
	}

	var errs []error
	if *baseURL != "" {
		u, err := url.Parse(*baseURL)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("base URL: %w", err))
		case u.Scheme != "http" && u.Scheme != "https", u.Host == "":
			errs = append(errs, fmt.Errorf("base URL %q: must be an absolute http(s) URL", *baseURL))
		default:
			u.Path = strings.TrimSuffix(u.Path, "/")
			cfg.BaseURL = u
		}
	}
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
		smtp, err := parseSMTP(*smtpHost, *smtpPort, *smtpTLS, *smtpUser, *smtpPass, *smtpFrom)
		errs = append(errs, err)
		cfg.SMTP = smtp
	}
	return cfg, errors.Join(errs...)
}

// parseSMTP validates the SMTP settings given a host.
func parseSMTP(host, port, tls, user, pass, from string) (SMTP, error) {
	s := SMTP{Host: host, TLS: tls, Username: user, Password: pass}
	var errs []error
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
