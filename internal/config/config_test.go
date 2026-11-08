package config

import (
	"log/slog"
	"net/netip"
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
	want := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("192.168.1.5/32")}
	if len(cfg.TrustedProxies) != 2 || cfg.TrustedProxies[0] != want[0] || cfg.TrustedProxies[1] != want[1] {
		t.Errorf("TrustedProxies = %v", cfg.TrustedProxies)
	}
}

func TestLoadInvalid(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"relative base url": {"MISE_BASE_URL": "mise.local"},
		"bad proxy":         {"MISE_TRUSTED_PROXIES": "not-an-ip"},
		"bad log format":    {"MISE_LOG_FORMAT": "xml"},
		"bad log level":     {"MISE_LOG_LEVEL": "loud"},
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
