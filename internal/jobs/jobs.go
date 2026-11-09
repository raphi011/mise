// Package jobs runs periodic maintenance outside requests. It works across
// households through store.DB.System.
package jobs

import (
	"context"
	"log/slog"
	"time"

	"github.com/raphi011/mise/internal/groceries"
	"github.com/raphi011/mise/internal/store"
)

// DefaultInterval is how often Run sweeps.
const DefaultInterval = time.Hour

// Sweeper hard-deletes rows whose Undo window has passed: list entries
// removed or cleared, and deleted shopping lists (with their entries)
// (docs/plan.md, Undo).
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
		out.Lists, err = res.RowsAffected()
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
		case swept.Entries > 0 || swept.Lists > 0:
			s.log.InfoContext(ctx, "swept soft-deleted rows",
				slog.Int64("entries", swept.Entries), slog.Int64("lists", swept.Lists))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
