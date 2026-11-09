package ai_test

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/ai"
)

func newBox(t *testing.T) *ai.Box {
	t.Helper()
	key, err := ai.LoadKey("", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, err := ai.NewBox(key)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBoxRoundTrip(t *testing.T) {
	b := newBox(t)
	hh := uuid.New()
	sealed := b.Seal(hh, "sk-secret")
	if strings.Contains(sealed, "sk-secret") || !strings.HasPrefix(sealed, "v1:") {
		t.Fatalf("sealed = %q", sealed)
	}
	if b.Seal(hh, "sk-secret") == sealed {
		t.Error("two seals of the same value are equal: nonce reused")
	}
	got, err := b.Open(hh, sealed)
	if err != nil || got != "sk-secret" {
		t.Fatalf("Open = %q, %v", got, err)
	}
}

func TestBoxRejectsOtherHouseholdAndKey(t *testing.T) {
	b := newBox(t)
	hh := uuid.New()
	sealed := b.Seal(hh, "sk-secret")
	if _, err := b.Open(uuid.New(), sealed); !errors.Is(err, ai.ErrUnreadable) {
		t.Errorf("other household: %v", err)
	}
	if _, err := newBox(t).Open(hh, sealed); !errors.Is(err, ai.ErrUnreadable) {
		t.Errorf("other key: %v", err)
	}
	for _, bad := range []string{"", "sk-secret", "v1:%%%", "v1:" + base64.StdEncoding.EncodeToString([]byte("x"))} {
		if _, err := b.Open(hh, bad); !errors.Is(err, ai.ErrUnreadable) {
			t.Errorf("Open(%q) = %v", bad, err)
		}
	}
}

func TestLoadKey(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	k1, err := ai.LoadKey("", dir)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, ai.KeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("key file mode %v", info.Mode().Perm())
	}
	k2, err := ai.LoadKey("", dir)
	if err != nil || string(k1) != string(k2) {
		t.Fatalf("second load = %v, differs: %v", err, string(k1) != string(k2))
	}

	env := base64.StdEncoding.EncodeToString(make([]byte, 32))
	k3, err := ai.LoadKey(env, dir)
	if err != nil || len(k3) != 32 || string(k3) == string(k1) {
		t.Fatalf("env key = %v, %v", k3, err)
	}
	if _, err := ai.LoadKey("c2hvcnQ=", dir); err == nil {
		t.Error("short env key accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, ai.KeyFile), []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ai.LoadKey("", dir); err == nil {
		t.Error("junk key file accepted")
	}
}
