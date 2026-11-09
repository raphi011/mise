package store_test

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
	"github.com/raphi011/mise/internal/tenant"
)

func insertHousehold(t *testing.T, ctx context.Context, db *store.DB, name string, at store.Time) uuid.UUID {
	t.Helper()
	id := store.NewID()
	err := db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
		_, err := q.Exec(ctx, `INSERT INTO households (id, name, timezone, week_start, default_language, created_at, updated_at)
			VALUES (?, ?, 'Europe/Vienna', 'mon', 'en', ?, ?)`, id, name, at, at)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRoundTrip(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := context.Background()
		at := store.T(time.Date(2026, 11, 8, 18, 30, 0, 123456789, time.FixedZone("CET", 3600)))
		id := insertHousehold(t, ctx, db, "Home", at)

		var got struct {
			ID        uuid.UUID
			Name      string
			CreatedAt store.Time
		}
		if err := db.System().Read().Get(ctx, &got, `SELECT id, name, created_at FROM households WHERE id = ?`, id); err != nil {
			t.Fatal(err)
		}
		if got.ID != id || got.Name != "Home" {
			t.Errorf("got %+v", got)
		}
		if !got.CreatedAt.Equal(at.Time) || got.CreatedAt.Nanosecond() != 123456000 {
			t.Errorf("CreatedAt = %v, want %v (microsecond precision)", got.CreatedAt, at)
		}

		// Time cutoffs bound from Go compare correctly on both engines.
		var n int
		err := db.System().Read().QueryRow(ctx, `SELECT count(*) FROM households WHERE created_at > ?`,
			store.T(at.Add(-time.Microsecond))).Scan(&n)
		if err != nil || n != 1 {
			t.Errorf("before cutoff: n=%d err=%v", n, err)
		}
		err = db.System().Read().QueryRow(ctx, `SELECT count(*) FROM households WHERE created_at > ?`,
			store.T(at.Add(time.Microsecond))).Scan(&n)
		if err != nil || n != 0 {
			t.Errorf("after cutoff: n=%d err=%v", n, err)
		}

		if err := db.System().Read().Get(ctx, &got, `SELECT id, name, created_at FROM households WHERE id = ?`, store.NewID()); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("missing row: err = %v, want sql.ErrNoRows", err)
		}
	})
}

func TestNullableTime(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := context.Background()
		now := store.Now()
		hid := insertHousehold(t, ctx, db, "Home", now)
		uid := store.NewID()
		err := db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
			if _, err := q.Exec(ctx, `INSERT INTO users (id, username, username_key, name, password_hash, avatar_color, theme, created_at, updated_at)
				VALUES (?, 'anna', 'anna', 'Anna', 'x', 'amber', 'system', ?, ?)`, uid, now, now); err != nil {
				return err
			}
			var removed *store.Time
			_, err := q.Exec(ctx, `INSERT INTO memberships (household_id, user_id, role, removed_at, created_at, updated_at)
				VALUES (?, ?, 'admin', ?, ?, ?)`, hid, uid, removed, now, now)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		var removed *store.Time
		if err := db.System().Read().QueryRow(ctx, `SELECT removed_at FROM memberships WHERE user_id = ?`, uid).Scan(&removed); err != nil {
			t.Fatal(err)
		}
		if removed != nil {
			t.Errorf("removed_at = %v, want NULL", removed)
		}
	})
}

func TestTxRollback(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := context.Background()
		boom := errors.New("boom")
		err := db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
			now := store.Now()
			if _, err := q.Exec(ctx, `INSERT INTO households (id, name, timezone, week_start, default_language, created_at, updated_at)
				VALUES (?, 'x', 'UTC', 'mon', 'en', ?, ?)`, store.NewID(), now, now); err != nil {
				return err
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		var n int
		if err := db.System().Read().QueryRow(ctx, `SELECT count(*) FROM households`).Scan(&n); err != nil || n != 0 {
			t.Errorf("n=%d err=%v, want rollback", n, err)
		}
	})
}

func TestForeignKeysEnforced(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := context.Background()
		now := store.Now()
		err := db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
			_, err := q.Exec(ctx, `INSERT INTO memberships (household_id, user_id, role, created_at, updated_at)
				VALUES (?, ?, 'member', ?, ?)`, store.NewID(), store.NewID(), now, now)
			return err
		})
		if err == nil {
			t.Fatal("expected foreign key violation")
		}
	})
}

func TestScope(t *testing.T) {
	db := storetest.SQLite(t)
	if _, err := db.Scope(context.Background()); !errors.Is(err, store.ErrNoHousehold) {
		t.Errorf("Scope without household: err = %v", err)
	}
	id := store.NewID()
	s, err := db.Scope(tenant.WithHousehold(context.Background(), id))
	if err != nil {
		t.Fatal(err)
	}
	if s.HouseholdID() != id {
		t.Errorf("HouseholdID = %v, want %v", s.HouseholdID(), id)
	}
}

func TestMigrationsCurrent(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		pending, err := db.MigrationsPending(context.Background())
		if err != nil || pending {
			t.Errorf("pending=%v err=%v", pending, err)
		}
	})
}

func TestReadRejectsWrites(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := context.Background()
		now := store.Now()
		// Reader has no Exec; a write smuggled through a query must fail too.
		var id uuid.UUID
		err := db.System().Read().QueryRow(ctx, `INSERT INTO households (id, name, timezone, week_start, default_language, created_at, updated_at)
			VALUES (?, 'x', 'UTC', 'mon', 'en', ?, ?) RETURNING id`, store.NewID(), now, now).Scan(&id)
		if err == nil {
			t.Fatal("write through Read() succeeded")
		}
		var n int
		if err := db.System().Read().QueryRow(ctx, `SELECT count(*) FROM households`).Scan(&n); err != nil || n != 0 {
			t.Errorf("n=%d err=%v, want no rows", n, err)
		}
	})
}

func TestNestedTx(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var inner error
		err := db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
			inner = db.ScopeFor(store.NewID()).Tx(ctx, func(context.Context, store.Querier) error { return nil })
			return inner
		})
		if !errors.Is(inner, store.ErrNestedTx) || !errors.Is(err, store.ErrNestedTx) {
			t.Errorf("inner=%v outer=%v, want ErrNestedTx", inner, err)
		}
		// The outer transaction released the writer.
		if err := db.System().Tx(ctx, func(context.Context, store.Querier) error { return nil }); err != nil {
			t.Errorf("Tx after nested attempt: %v", err)
		}
	})
}

func TestMigrated(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite://"+t.TempDir()+"/mise.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if db.Migrated() {
		t.Error("Migrated before Migrate")
	}
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if !db.Migrated() {
		t.Error("not Migrated after Migrate")
	}
}

func TestOpenExisting(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "missing")
	path := filepath.Join(dir, "mise.db")
	if _, err := store.OpenExisting(ctx, "sqlite://"+path); !errors.Is(err, store.ErrNoDatabase) {
		t.Fatalf("OpenExisting on a missing file: %v, want ErrNoDatabase", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("OpenExisting created %s: %v", dir, err)
	}

	db, err := store.Open(ctx, "sqlite://"+path)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	db, err = store.OpenExisting(ctx, "sqlite://"+path)
	if err != nil {
		t.Fatalf("OpenExisting on an existing file: %v", err)
	}
	_ = db.Close()
}

func TestHasSchema(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite://"+t.TempDir()+"/mise.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if ok, err := db.HasSchema(ctx); err != nil || ok {
		t.Errorf("HasSchema on a new database = %v, %v", ok, err)
	}
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		if ok, err := db.HasSchema(ctx); err != nil || !ok {
			t.Errorf("HasSchema on a migrated database = %v, %v", ok, err)
		}
	})
}
