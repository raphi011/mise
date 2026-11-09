// Package store owns database connections, dialect differences and the
// household boundary.
//
// Domain code gets database access only through a Scoped value, built from
// the request context with DB.Scope. Code without a request uses
// DB.ScopeFor; access to global tables (users, sessions, invites by token)
// goes through DB.System. Both are restricted by lint to a few packages.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/georgysavva/scany/v2/sqlscan"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite" // registers the "sqlite" driver

	"github.com/raphi011/mise/internal/tenant"
)

// ErrNoHousehold is returned by Scope when the context carries no household.
var ErrNoHousehold = errors.New("store: no household in context")

// ErrNestedTx is returned by Tx when called from inside another Tx. On
// SQLite the inner transaction would wait forever for the single writer
// connection held by the outer one; on Postgres it would silently commit
// independently. Pass the outer Querier down instead.
var ErrNestedTx = errors.New("store: nested transaction")

// DB holds the connection pools: writes go through w and reads through r,
// whose connections are read-only on both engines. On SQLite w is a single
// connection.
type DB struct {
	dialect  Dialect
	w        *sql.DB
	r        *sql.DB
	migrated atomic.Bool
}

// Open connects to the database named by dsn: sqlite://path or
// postgres://... . It does not run migrations.
func Open(ctx context.Context, dsn string) (*DB, error) {
	switch {
	case strings.HasPrefix(dsn, "sqlite://"):
		return openSQLite(ctx, strings.TrimPrefix(dsn, "sqlite://"))
	case strings.HasPrefix(dsn, "postgres://"), strings.HasPrefix(dsn, "postgresql://"):
		return openPostgres(ctx, dsn)
	default:
		return nil, fmt.Errorf("store: unsupported database URL %q (want sqlite:// or postgres://)", redact(dsn))
	}
}

// ErrNoDatabase is returned by OpenExisting when the SQLite file does not
// exist.
var ErrNoDatabase = errors.New("store: database does not exist")

// OpenExisting is Open for tools that must find an existing database, such
// as backup: on SQLite it fails with ErrNoDatabase instead of creating the
// file and its directory. It does not check the schema; see HasSchema.
func OpenExisting(ctx context.Context, dsn string) (*DB, error) {
	if path, ok := strings.CutPrefix(dsn, "sqlite://"); ok && path != "" {
		info, err := os.Stat(path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return nil, fmt.Errorf("%w: %s", ErrNoDatabase, path)
		case err != nil:
			return nil, fmt.Errorf("store: %w", err)
		case !info.Mode().IsRegular():
			return nil, fmt.Errorf("store: %s is not a regular file", path)
		}
	}
	return Open(ctx, dsn)
}

// sqlitePragmas are applied to every connection via the DSN.
var sqlitePragmas = []string{
	"journal_mode(WAL)",
	"foreign_keys(1)",
	"busy_timeout(5000)",
	"synchronous(NORMAL)",
}

func sqliteDSN(path string, readOnly bool) string {
	q := url.Values{}
	for _, p := range sqlitePragmas {
		q.Add("_pragma", p)
	}
	if readOnly {
		q.Add("_pragma", "query_only(1)")
	} else {
		// Take the write lock at BEGIN so a read transaction never has to
		// upgrade (which fails with SQLITE_BUSY instead of waiting).
		q.Set("_txlock", "immediate")
	}
	return "file:" + path + "?" + q.Encode()
}

func openSQLite(ctx context.Context, path string) (*DB, error) {
	if path == "" {
		return nil, errors.New("store: empty sqlite path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("store: create database directory: %w", err)
	}

	w, err := sql.Open("sqlite", sqliteDSN(path, false))
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	w.SetConnMaxIdleTime(0)
	// Open the writer first so the file exists and WAL mode is set before
	// read-only connections attach.
	if err := w.PingContext(ctx); err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("store: open sqlite: %w", err)
	}

	r, err := sql.Open("sqlite", sqliteDSN(path, true))
	if err != nil {
		_ = w.Close()
		return nil, err
	}
	readers := max(4, runtime.NumCPU())
	r.SetMaxOpenConns(readers)
	r.SetMaxIdleConns(readers)
	if err := r.PingContext(ctx); err != nil {
		_ = w.Close()
		_ = r.Close()
		return nil, fmt.Errorf("store: open sqlite reader: %w", err)
	}
	return &DB{dialect: SQLite, w: w, r: r}, nil
}

func openPostgres(ctx context.Context, dsn string) (*DB, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		// pgx errors may echo the DSN, password included.
		return nil, fmt.Errorf("store: invalid postgres URL %q", redact(dsn))
	}

	w := stdlib.OpenDB(*cfg)
	w.SetMaxOpenConns(10)
	w.SetConnMaxIdleTime(5 * time.Minute)
	if err := w.PingContext(ctx); err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("store: open postgres: %w", err)
	}

	// The read pool rejects writes, like SQLite's query_only reader.
	rcfg := cfg.Copy()
	rcfg.RuntimeParams["default_transaction_read_only"] = "on"
	r := stdlib.OpenDB(*rcfg)
	r.SetMaxOpenConns(20)
	r.SetMaxIdleConns(20)
	r.SetConnMaxIdleTime(5 * time.Minute)
	if err := r.PingContext(ctx); err != nil {
		_ = w.Close()
		_ = r.Close()
		return nil, fmt.Errorf("store: open postgres reader: %w", err)
	}
	return &DB{dialect: Postgres, w: w, r: r}, nil
}

// redact hides credentials in a database URL for error messages.
func redact(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil || u.User == nil {
		return dsn
	}
	return u.Redacted()
}

// Dialect reports the database engine.
func (db *DB) Dialect() Dialect { return db.dialect }

// Close closes all pools.
func (db *DB) Close() error {
	return errors.Join(db.w.Close(), db.r.Close())
}

// Ping checks that the database is reachable through the read pool. It does
// not touch the writer: on SQLite that is a single connection, and a
// readiness probe must not queue behind writes. Open has already verified
// the writer.
func (db *DB) Ping(ctx context.Context) error {
	return db.r.PingContext(ctx)
}

// Scope returns database access limited to the household in ctx.
func (db *DB) Scope(ctx context.Context) (Scoped, error) {
	id, ok := tenant.HouseholdID(ctx)
	if !ok {
		return Scoped{}, ErrNoHousehold
	}
	return Scoped{db: db, householdID: id}, nil
}

// ScopeFor returns database access limited to the given household, for code
// that runs outside a request (sweeps, AI jobs, live fan-out). Restricted by
// lint; prefer Scope.
func (db *DB) ScopeFor(householdID uuid.UUID) Scoped {
	if householdID == uuid.Nil {
		panic("store: ScopeFor with nil household")
	}
	return Scoped{db: db, householdID: householdID}
}

// System returns unscoped access for global tables (households, users,
// sessions, invites by token) and migrations. Restricted by lint.
func (db *DB) System() System { return System{db: db} }

// Scoped is database access limited to one household. Every query run through
// it must filter on HouseholdID().
type Scoped struct {
	db          *DB
	householdID uuid.UUID
}

// HouseholdID is the household every query must filter on.
func (s Scoped) HouseholdID() uuid.UUID { return s.householdID }

// Read returns read-only access on the read pool.
func (s Scoped) Read() Reader { return s.db.read() }

// Tx runs fn in a write transaction. fn must use the ctx it is given; Tx
// returns ErrNestedTx when called with it.
func (s Scoped) Tx(ctx context.Context, fn func(ctx context.Context, q Querier) error) error {
	return s.db.tx(ctx, fn)
}

// LockHousehold serialises read-then-write changes within the household,
// such as last-admin checks or rewriting positions. Call it first inside a
// Tx. SQLite transactions already hold the single write lock from BEGIN; on
// Postgres the household row is locked until the transaction ends.
func (s Scoped) LockHousehold(ctx context.Context, q Querier) error {
	query := `SELECT id FROM households WHERE id = ?`
	if q.Dialect() == Postgres {
		query += ` FOR UPDATE`
	}
	var id uuid.UUID
	return q.Get(ctx, &id, query, s.householdID)
}

// System is unscoped database access.
type System struct{ db *DB }

// Read returns read-only access on the read pool.
func (s System) Read() Reader { return s.db.read() }

// Tx runs fn in a write transaction. fn must use the ctx it is given; Tx
// returns ErrNestedTx when called with it.
func (s System) Tx(ctx context.Context, fn func(ctx context.Context, q Querier) error) error {
	return s.db.tx(ctx, fn)
}

func (db *DB) read() Reader { return Reader{q: db.r, d: db.dialect} }

// inTxKey marks a context passed into a Tx callback.
type inTxKey struct{}

func (db *DB) tx(ctx context.Context, fn func(context.Context, Querier) error) (err error) {
	if ctx.Value(inTxKey{}) != nil {
		return ErrNestedTx
	}
	tx, err := db.w.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if err = fn(context.WithValue(ctx, inTxKey{}, true), Querier{Reader{q: tx, d: db.dialect}}); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	return nil
}

type sqlQuerier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Reader runs queries written with ? placeholders on either engine. A
// Reader from Read is backed by read-only connections: statements that
// write fail on both engines.
type Reader struct {
	q sqlQuerier
	d Dialect
}

// Querier is a Reader that can also execute statements, inside a Tx.
type Querier struct{ Reader }

// Exec runs a statement.
func (q Querier) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return q.q.ExecContext(ctx, q.d.rebind(query), args...)
}

// Dialect reports the database engine, for the few dialect-specific queries.
func (q Reader) Dialect() Dialect { return q.d }

// Query runs a query returning rows.
func (q Reader) Query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return q.q.QueryContext(ctx, q.d.rebind(query), args...)
}

// QueryRow runs a query returning at most one row.
func (q Reader) QueryRow(ctx context.Context, query string, args ...any) *sql.Row {
	return q.q.QueryRowContext(ctx, q.d.rebind(query), args...)
}

// Select scans all rows into dst, a pointer to a slice of structs or
// scalars. Struct fields map to columns by `db` tag or snake_case name.
func (q Reader) Select(ctx context.Context, dst any, query string, args ...any) error {
	return sqlscan.Select(ctx, rebinding{q}, dst, query, args...)
}

// Get scans exactly one row into dst. It returns sql.ErrNoRows if there is
// none.
func (q Reader) Get(ctx context.Context, dst any, query string, args ...any) error {
	err := sqlscan.Get(ctx, rebinding{q}, dst, query, args...)
	if sqlscan.NotFound(err) {
		return sql.ErrNoRows
	}
	return err
}

// rebinding adapts Reader to sqlscan.Querier.
type rebinding struct{ q Reader }

func (r rebinding) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return r.q.Query(ctx, query, args...)
}
