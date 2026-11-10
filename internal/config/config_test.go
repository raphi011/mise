package config

import (
	"errors"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(nil, envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DB != "sqlite://data/mise.db" {
		t.Errorf("DB = %q", cfg.DB)
	}
	if cfg.Listen != ":8080" || cfg.LogFormat != "json" || cfg.LogLevel != slog.LevelInfo {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
	if cfg.BaseURL != nil {
		t.Errorf("BaseURL = %v, want nil", cfg.BaseURL)
	}
}

func TestLoadEnvAndFlags(t *testing.T) {
	env := envMap(map[string]string{
		"MISE_DATA_DIR":        "/srv/mise",
		"MISE_BASE_URL":        "https://mise.example.com/",
		"MISE_EXTRA_BASE_URLS": " https://mise.tail1234.ts.net/, ,http://mise.lan:8080",
		"MISE_TRUSTED_PROXIES": "10.0.0.0/8, 192.168.1.5",
		"MISE_LISTEN":          ":9000",
	})
	cfg, err := Load([]string{"-listen", ":7000"}, env)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DB != "sqlite:///srv/mise/mise.db" {
		t.Errorf("DB = %q", cfg.DB)
	}
	if cfg.Listen != ":7000" {
		t.Errorf("flag should override env, Listen = %q", cfg.Listen)
	}
	if got := cfg.BaseURL.String(); got != "https://mise.example.com" {
		t.Errorf("BaseURL = %q", got)
	}
	var extras []string
	for _, u := range cfg.ExtraBaseURLs {
		extras = append(extras, u.String())
	}
	if got := strings.Join(extras, " "); got != "https://mise.tail1234.ts.net http://mise.lan:8080" {
		t.Errorf("ExtraBaseURLs = %q", got)
	}
	want := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("192.168.1.5/32")}
	if len(cfg.TrustedProxies) != 2 || cfg.TrustedProxies[0] != want[0] || cfg.TrustedProxies[1] != want[1] {
		t.Errorf("TrustedProxies = %v", cfg.TrustedProxies)
	}
}

func TestLoadInvalid(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"relative base url": {"MISE_BASE_URL": "mise.local"},
		"base url path":     {"MISE_BASE_URL": "https://example.com/mise"},
		"base url query":    {"MISE_BASE_URL": "https://mise.example.com/?a=1"},
		"base url userinfo": {"MISE_BASE_URL": "https://u:p@mise.example.com"},
		"extra url path":    {"MISE_EXTRA_BASE_URLS": "https://mise.example.net/mise"},
		"extra url twice":   {"MISE_EXTRA_BASE_URLS": "https://mise.example.net, http://MISE.example.net"},
		"extra url is base": {"MISE_BASE_URL": "https://mise.example.com", "MISE_EXTRA_BASE_URLS": "http://mise.example.com"},
		"bad proxy":         {"MISE_TRUSTED_PROXIES": "not-an-ip"},
		"bad log format":    {"MISE_LOG_FORMAT": "xml"},
		"bad log level":     {"MISE_LOG_LEVEL": "loud"},
		"smtp without from": {"MISE_SMTP_HOST": "mail.example.com"},
		"smtp bad port":     {"MISE_SMTP_HOST": "mail.example.com", "MISE_SMTP_FROM": "a@example.com", "MISE_SMTP_PORT": "0"},
		"smtp bad tls":      {"MISE_SMTP_HOST": "mail.example.com", "MISE_SMTP_FROM": "a@example.com", "MISE_SMTP_TLS": "ssl"},
		"smtp user only":    {"MISE_SMTP_HOST": "mail.example.com", "MISE_SMTP_FROM": "a@example.com", "MISE_SMTP_USERNAME": "u"},
		"smtp bad from":     {"MISE_SMTP_HOST": "mail.example.com", "MISE_SMTP_FROM": "not an address"},
		"smtp bad skip":     {"MISE_SMTP_HOST": "mail.example.com", "MISE_SMTP_FROM": "a@example.com", "MISE_SMTP_TLS_SKIP_VERIFY": "maybe"},
		"smtp skip no tls":  {"MISE_SMTP_HOST": "mail.example.com", "MISE_SMTP_FROM": "a@example.com", "MISE_SMTP_TLS": "none", "MISE_SMTP_TLS_SKIP_VERIFY": "true"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(nil, envMap(env)); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestUsageHidesEnvValues(t *testing.T) {
	env := envMap(map[string]string{"MISE_DB": "postgres://u:secretpw@db/mise"})
	for _, args := range [][]string{{"-h"}, {"-no-such-flag"}} {
		var buf strings.Builder
		_, err := load(args, env, &buf)
		if err == nil {
			t.Fatalf("%v: expected error", args)
		}
		if !strings.Contains(buf.String(), "MISE_DB") {
			t.Fatalf("%v: usage not written:\n%s", args, buf.String())
		}
		if strings.Contains(buf.String(), "secretpw") {
			t.Errorf("%v: usage leaks env value:\n%s", args, buf.String())
		}
	}
}

func TestLoadEnvDB(t *testing.T) {
	env := envMap(map[string]string{"MISE_DB": "postgres://env/db"})
	cfg, err := Load(nil, env)
	if err != nil || cfg.DB != "postgres://env/db" {
		t.Errorf("DB = %q, err = %v", cfg.DB, err)
	}
	cfg, err = Load([]string{"-db", "postgres://flag/db"}, env)
	if err != nil || cfg.DB != "postgres://flag/db" {
		t.Errorf("flag should override env, DB = %q, err = %v", cfg.DB, err)
	}
}

func TestLoadSMTP(t *testing.T) {
	cfg, err := Load(nil, envMap(nil))
	if err != nil || cfg.SMTP.Enabled() {
		t.Fatalf("SMTP enabled without a host: %+v, %v", cfg.SMTP, err)
	}
	env := envMap(map[string]string{
		"MISE_SMTP_HOST":     "mail.example.com",
		"MISE_SMTP_FROM":     "mise <mise@example.com>",
		"MISE_SMTP_USERNAME": "mise",
		"MISE_SMTP_PASSWORD": "secret",
	})
	cfg, err = Load([]string{"-smtp-port", "465", "-smtp-tls", "tls"}, env)
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.SMTP
	if !s.Enabled() || s.Host != "mail.example.com" || s.Port != 465 || s.TLS != TLSImplicit ||
		s.Username != "mise" || s.Password != "secret" || s.From.Address != "mise@example.com" || s.From.Name != "mise" {
		t.Errorf("SMTP = %+v", s)
	}
	cfg, err = Load(nil, envMap(map[string]string{"MISE_SMTP_HOST": "mail", "MISE_SMTP_FROM": "a@example.com"}))
	if err != nil || cfg.SMTP.Port != 587 || cfg.SMTP.TLS != TLSStartTLS || cfg.SMTP.SkipVerify {
		t.Errorf("SMTP defaults = %+v, %v", cfg.SMTP, err)
	}
	cfg, err = Load(nil, envMap(map[string]string{
		"MISE_SMTP_HOST": "mail", "MISE_SMTP_FROM": "a@example.com", "MISE_SMTP_TLS_SKIP_VERIFY": "true",
	}))
	if err != nil || !cfg.SMTP.SkipVerify {
		t.Errorf("SMTP skip verify = %+v, %v", cfg.SMTP, err)
	}
}

func TestLoadSecretKey(t *testing.T) {
	cfg, err := Load(nil, envMap(map[string]string{"MISE_SECRET_KEY": "a2V5"}))
	if err != nil || cfg.SecretKey != "a2V5" {
		t.Errorf("SecretKey = %q, %v", cfg.SecretKey, err)
	}
}

func TestLoadOIDC(t *testing.T) {
	cfg, err := Load(nil, envMap(nil))
	if err != nil || cfg.OIDC.Enabled() {
		t.Fatalf("OIDC enabled without config: %+v, %v", cfg.OIDC, err)
	}
	cfg, err = Load(nil, envMap(map[string]string{
		"MISE_OIDC_ISSUER":        "https://id.example.com",
		"MISE_OIDC_CLIENT_ID":     "mise",
		"MISE_OIDC_CLIENT_SECRET": "s3cret",
	}))
	if err != nil {
		t.Fatal(err)
	}
	o := cfg.OIDC
	if !o.Enabled() || o.Issuer != "https://id.example.com" || o.ClientID != "mise" || o.ClientSecret != "s3cret" || o.Name != "SSO" {
		t.Errorf("OIDC = %+v", o)
	}

	file := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(file, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load([]string{"-oidc-name", "Pocket ID"}, envMap(map[string]string{
		"MISE_OIDC_ISSUER":             "https://id.example.com",
		"MISE_OIDC_CLIENT_ID":          "mise",
		"MISE_OIDC_CLIENT_SECRET_FILE": file,
	}))
	if err != nil || cfg.OIDC.ClientSecret != "from-file" || cfg.OIDC.Name != "Pocket ID" {
		t.Errorf("OIDC from file = %+v, %v", cfg.OIDC, err)
	}
}

func TestLoadOIDCInvalid(t *testing.T) {
	full := func(extra map[string]string) map[string]string {
		m := map[string]string{
			"MISE_OIDC_ISSUER":        "https://id.example.com",
			"MISE_OIDC_CLIENT_ID":     "mise",
			"MISE_OIDC_CLIENT_SECRET": "s3cret",
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	for name, env := range map[string]map[string]string{
		"issuer only":       {"MISE_OIDC_ISSUER": "https://id.example.com"},
		"no issuer":         {"MISE_OIDC_CLIENT_ID": "mise", "MISE_OIDC_CLIENT_SECRET": "s3cret"},
		"no secret":         {"MISE_OIDC_ISSUER": "https://id.example.com", "MISE_OIDC_CLIENT_ID": "mise"},
		"secret and file":   full(map[string]string{"MISE_OIDC_CLIENT_SECRET_FILE": "/etc/hostname"}),
		"missing file":      {"MISE_OIDC_ISSUER": "https://id.example.com", "MISE_OIDC_CLIENT_ID": "mise", "MISE_OIDC_CLIENT_SECRET_FILE": "/nonexistent/secret"},
		"relative issuer":   full(map[string]string{"MISE_OIDC_ISSUER": "id.example.com"}),
		"issuer with query": full(map[string]string{"MISE_OIDC_ISSUER": "https://id.example.com/?x=1"}),
		"blank name":        full(map[string]string{"MISE_OIDC_NAME": "   "}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(nil, envMap(env)); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestParseBaseURL(t *testing.T) {
	for raw, want := range map[string]string{
		"https://mise.example.com":       "https://mise.example.com",
		"https://mise.example.com/":      "https://mise.example.com",
		" http://localhost:8080 ":        "http://localhost:8080",
		"https://mise.example.com:8443/": "https://mise.example.com:8443",
	} {
		u, err := ParseBaseURL(raw)
		if err != nil || u.String() != want {
			t.Errorf("ParseBaseURL(%q) = %v, %v; want %s", raw, u, err, want)
		}
	}
	for _, raw := range []string{"https://example.com/mise", "https://example.com/mise/", "https://example.com//"} {
		if _, err := ParseBaseURL(raw); !errors.Is(err, ErrBaseURLPath) {
			t.Errorf("ParseBaseURL(%q) = %v, want ErrBaseURLPath", raw, err)
		}
	}
	for _, raw := range []string{"", "mise.local", "ftp://mise.example.com", "https://mise.example.com#x", "https://mise.example.com?", "mailto:a@b"} {
		if _, err := ParseBaseURL(raw); err == nil {
			t.Errorf("ParseBaseURL(%q) succeeded", raw)
		}
	}
}
