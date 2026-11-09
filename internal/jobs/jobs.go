// Package jobs runs periodic maintenance outside requests. It works across
// households through store.DB.System.
package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/groceries"
	"github.com/raphi011/mise/internal/store"
)

// DefaultInterval is how often Run sweeps.
const DefaultInterval = time.Hour

// Sweeper hard-deletes rows whose Undo window has passed: list entries
// removed or cleared, deleted shopping lists (with their entries) and
// deleted recipes (with their ingredients, steps, tags, favourites and cook
// history) (docs/plan.md, Undo).
type Sweeper struct {
	db       *store.DB
	log      *slog.Logger
	now      func() time.Time
	interval time.Duration
}

// NewSweeper returns a Sweeper. now defaults to time.Now and interval to
// DefaultInterval.
func NewSweeper(db *store.DB, log *slog.Logger, now func() time.Time, interval time.Duration) *Sweeper {
	if now == nil {
		now = time.Now
	}
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Sweeper{db: db, log: log, now: now, interval: interval}
}

// Swept counts what one sweep deleted.
type Swept struct {
	Entries int64
	Lists   int64
	Recipes int64
}

// Sweep deletes soft-deleted rows older than groceries.UndoWindow.
func (s *Sweeper) Sweep(ctx context.Context) (Swept, error) {
	cutoff := store.T(s.now().Add(-groceries.UndoWindow))
	var out Swept
	err := s.db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
		res, err := q.Exec(ctx, `DELETE FROM list_entries WHERE deleted_at < ?`, cutoff)
		if err != nil {
			return err
		}
		if out.Entries, err = res.RowsAffected(); err != nil {
			return err
		}
		// Entries of deleted lists go with them (ON DELETE CASCADE).
		res, err = q.Exec(ctx, `DELETE FROM shopping_lists WHERE deleted_at < ?`, cutoff)
		if err != nil {
			return err
		}
		if out.Lists, err = res.RowsAffected(); err != nil {
			return err
		}
		// List entries keep their item but lose the recipe tag; the foreign
		// key is RESTRICT, see docs/plan.md on composite foreign keys.
		if _, err := q.Exec(ctx, `UPDATE list_entries SET recipe_id = NULL
			WHERE recipe_id IN (SELECT id FROM recipes WHERE deleted_at < ?)`, cutoff); err != nil {
			return err
		}
		res, err = q.Exec(ctx, `DELETE FROM recipes WHERE deleted_at < ?`, cutoff)
		if err != nil {
			return err
		}
		out.Recipes, err = res.RowsAffected()
		return err
	})
	return out, err
}

// Run sweeps now and then every interval until ctx ends. Failures are
// logged and retried at the next tick.
func (s *Sweeper) Run(ctx context.Context) {
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		swept, err := s.Sweep(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			s.log.ErrorContext(ctx, "sweep soft-deleted rows", slog.Any("err", err))
		case swept != (Swept{}):
			s.log.InfoContext(ctx, "swept soft-deleted rows",
				slog.Int64("entries", swept.Entries), slog.Int64("lists", swept.Lists),
				slog.Int64("recipes", swept.Recipes))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// SeedGroceries gives every household without shopping lists its grocery
// defaults, in its default language. Setup seeds new households in its own
// transaction; this covers households created before Groceries existed.
// It is idempotent.
func SeedGroceries(ctx context.Context, db *store.DB, g *groceries.Service, defaults func(lang string) groceries.Defaults) error {
	var households []struct {
		ID              uuid.UUID
		DefaultLanguage string
	}
	if err := db.System().Read().Select(ctx, &households, `SELECT h.id, h.default_language FROM households h
		WHERE NOT EXISTS (SELECT 1 FROM shopping_lists l WHERE l.household_id = h.id)`); err != nil {
		return err
	}
	for _, h := range households {
		if _, err := g.Start(ctx, db.ScopeFor(h.ID), defaults(h.DefaultLanguage)); err != nil {
			return fmt.Errorf("seed groceries for household %s: %w", h.ID, err)
		}
	}
	return nil
}
