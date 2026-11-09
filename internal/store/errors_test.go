package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

func TestIsUniqueViolation(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		insert := func() error {
			return db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
				_, err := q.Exec(ctx, `INSERT INTO server_settings (key, value, created_at, updated_at) VALUES ('k', 'v', ?, ?)`,
					store.Now(), store.Now())
				return err
			})
		}
		if err := insert(); err != nil {
			t.Fatal(err)
		}
		if err := insert(); !store.IsUniqueViolation(err) {
			t.Errorf("duplicate key: %v not a unique violation", err)
		}
		err := db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
			_, err := q.Exec(ctx, `INSERT INTO server_settings (key, value, created_at, updated_at) VALUES ('x', NULL, ?, ?)`,
				store.Now(), store.Now())
			return err
		})
		if err == nil || store.IsUniqueViolation(err) {
			t.Errorf("not-null violation reported as unique violation: %v", err)
		}
		if store.IsUniqueViolation(errors.New("other")) || store.IsUniqueViolation(nil) {
			t.Error("plain error reported as unique violation")
		}
	})
}
