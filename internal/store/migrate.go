package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"
)

//go:embed migrations/sqlite/*.sql migrations/postgres/*.sql
var migrationsFS embed.FS

func (db *DB) migrator() (*goose.Provider, error) {
	var (
		dialect goose.Dialect
		dir     string
	)
	switch db.dialect {
	case SQLite:
		dialect, dir = goose.DialectSQLite3, "migrations/sqlite"
	case Postgres:
		dialect, dir = goose.DialectPostgres, "migrations/postgres"
	default:
		return nil, fmt.Errorf("store: no migrations for %s", db.dialect)
	}
	sub, err := fs.Sub(migrationsFS, dir)
	if err != nil {
		return nil, err
	}
	return goose.NewProvider(dialect, db.w, sub)
}

// Migrate applies all pending migrations and returns how many ran. After it
// succeeds, Migrated reports true.
func (db *DB) Migrate(ctx context.Context) (int, error) {
	p, err := db.migrator()
	if err != nil {
		return 0, err
	}
	res, err := p.Up(ctx)
	if err != nil {
		return len(res), fmt.Errorf("store: migrate: %w", err)
	}
	db.migrated.Store(true)
	return len(res), nil
}

// Migrated reports whether Migrate has succeeded on this DB, i.e. the schema
// matches the migrations embedded in this binary. It is cheap enough for
// readiness probes, unlike MigrationsPending.
func (db *DB) Migrated() bool { return db.migrated.Load() }

// MigrationsPending reports whether migrations are waiting to be applied. It
// queries the version table through the writer; for tests and tooling, not
// for probes.
func (db *DB) MigrationsPending(ctx context.Context) (bool, error) {
	p, err := db.migrator()
	if err != nil {
		return false, err
	}
	return p.HasPending(ctx)
}

// HasSchema reports whether migrations have ever run on this database, i.e.
// whether goose's version table exists. A false result on a database a tool
// expects to hold data means it was pointed at the wrong one.
func (db *DB) HasSchema(ctx context.Context) (bool, error) {
	var q string
	switch db.dialect {
	case SQLite:
		q = `SELECT EXISTS (SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = '` + goose.DefaultTablename + `')`
	case Postgres:
		q = `SELECT to_regclass('` + goose.DefaultTablename + `') IS NOT NULL`
	default:
		return false, fmt.Errorf("store: no schema check for %s", db.dialect)
	}
	var ok bool
	if err := db.r.QueryRowContext(ctx, q).Scan(&ok); err != nil {
		return false, fmt.Errorf("store: check schema: %w", err)
	}
	return ok, nil
}
