package store

import (
	"context"
	"errors"
	"fmt"
)

// ErrNotSQLite is returned by VacuumInto on Postgres, whose backups are
// pg_dump's job (docs/operations.md).
var ErrNotSQLite = errors.New("store: not a SQLite database")

// VacuumInto writes a consistent, compacted copy of the SQLite database to
// path, which must not exist. It runs on the writer connection (the readers
// are query_only) but only reads, so it is safe while a server is running
// against the same file.
func (db *DB) VacuumInto(ctx context.Context, path string) error {
	if db.dialect != SQLite {
		return ErrNotSQLite
	}
	if _, err := db.w.ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
		return fmt.Errorf("store: vacuum into: %w", err)
	}
	return nil
}
