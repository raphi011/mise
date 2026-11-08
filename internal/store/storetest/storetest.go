// Package storetest provides migrated databases on both engines for tests.
//
// SQLite runs on a temp file per test (an in-memory database would give every
// pooled connection its own empty database). Postgres uses the server at
// MISE_TEST_POSTGRES_URL, or a testcontainers instance when Docker is
// available, with a fresh database per test. Without either, Postgres tests
// are skipped locally and fail when CI is set.
package storetest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/raphi011/mise/internal/store"
)

// Each runs fn once per engine as a subtest, each with a freshly migrated
// database.
func Each(t *testing.T, fn func(t *testing.T, db *store.DB)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) { fn(t, SQLite(t)) })
	t.Run("postgres", func(t *testing.T) { fn(t, Postgres(t)) })
}

// SQLite returns a migrated SQLite database in a temp directory.
func SQLite(t *testing.T) *store.DB {
	t.Helper()
	return open(t, "sqlite://"+filepath.Join(t.TempDir(), "mise.db"))
}

// Postgres returns a migrated, empty Postgres database that is dropped when
// the test ends.
func Postgres(t *testing.T) *store.DB {
	t.Helper()
	base, err := postgresServer()
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("postgres unavailable in CI: %v", err)
		}
		t.Skipf("postgres unavailable (set MISE_TEST_POSTGRES_URL or start Docker): %v", err)
	}

	ctx := context.Background()
	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close() }()

	name := "mise_test_" + randomSuffix()
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() {
		admin, err := sql.Open("pgx", base)
		if err != nil {
			t.Errorf("drop test database: %v", err)
			return
		}
		defer func() { _ = admin.Close() }()
		if _, err := admin.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop test database: %v", err)
		}
	})

	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return open(t, u.String())
}

func open(t *testing.T, dsn string) *store.DB {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return db
}

var (
	pgOnce sync.Once
	pgURL  string
	pgErr  error
)

// postgresServer returns the admin URL of a Postgres server, starting a
// container on first use if no URL is configured.
func postgresServer() (string, error) {
	pgOnce.Do(func() {
		if u := os.Getenv("MISE_TEST_POSTGRES_URL"); u != "" {
			pgURL = u
			return
		}
		pgURL, pgErr = startContainer()
	})
	return pgURL, pgErr
}

func startContainer() (u string, err error) {
	// testcontainers panics when no Docker provider exists.
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("docker: %v", r)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := testcontainers.NewDockerClientWithOpts(ctx); err != nil {
		return "", fmt.Errorf("docker: %w", err)
	}
	ctr, err := tcpostgres.Run(ctx, "postgres:18-alpine",
		tcpostgres.WithDatabase("mise"),
		tcpostgres.WithUsername("mise"),
		tcpostgres.WithPassword("mise"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		return "", errors.Join(errors.New("start postgres container"), err)
	}
	// The container is removed by the testcontainers reaper when the test
	// process exits.
	return ctr.ConnectionString(ctx, "sslmode=disable")
}

func randomSuffix() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
