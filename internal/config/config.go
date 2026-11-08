// Package config loads server configuration from flags and MISE_* environment
// variables. Flags take precedence over the environment.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
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
}

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
	return cfg, errors.Join(errs...)
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
