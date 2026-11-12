// Package jobs runs periodic maintenance outside requests. It works across
// households through store.DB.System.
package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"golang.org/x/text/language"

	"github.com/raphi011/mise/internal/ai"
	"github.com/raphi011/mise/internal/groceries"
	"github.com/raphi011/mise/internal/images"
	"github.com/raphi011/mise/internal/store"
)

// DefaultInterval is how often Run sweeps.
const DefaultInterval = time.Hour

// Sweeper hard-deletes rows whose Undo window has passed: list entries
// removed or cleared, deleted shopping lists (with their entries), removed
// planned meals and deleted recipes (with their ingredients, steps, tags,
// favourites, cook history and planned meals). It
// also deletes password-reset tokens that expired or were used, and old
// recipe imports.
type Sweeper struct {
	db       *store.DB
	log      *slog.Logger
	now      func() time.Time
	interval time.Duration
	images   *images.Store
}

// ImageGrace is how long an image no recipe references is kept: an upload
// waits in the editor until the recipe is saved.
const ImageGrace = 24 * time.Hour

// WithImages makes sweeps also delete stored images that no recipe
// references (deleted recipes in their Undo window included) once they are
// older than ImageGrace.
func (s *Sweeper) WithImages(st *images.Store) *Sweeper {
	s.images = st
	return s
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
	Meals   int64
	Recipes int64
	Images  int64
	// Resets are expired or used password-reset tokens.
	Resets int64
	// Imports are recipe imports that finished more than
	// ai.ReadyRetention ago, and canceled ones.
	Imports int64
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
		res, err = q.Exec(ctx, `DELETE FROM planned_meals WHERE deleted_at < ?`, cutoff)
		if err != nil {
			return err
		}
		if out.Meals, err = res.RowsAffected(); err != nil {
			return err
		}
		// List entries keep their item but lose the recipe tag; the foreign
		// key is RESTRICT.
		// Planned meals of the recipe go with it (ON DELETE CASCADE).
		if _, err := q.Exec(ctx, `UPDATE list_entries SET recipe_id = NULL
			WHERE recipe_id IN (SELECT id FROM recipes WHERE deleted_at < ?)`, cutoff); err != nil {
			return err
		}
		res, err = q.Exec(ctx, `DELETE FROM recipes WHERE deleted_at < ?`, cutoff)
		if err != nil {
			return err
		}
		if out.Recipes, err = res.RowsAffected(); err != nil {
			return err
		}
		// A used token is worthless; an expired one too.
		res, err = q.Exec(ctx, `DELETE FROM password_resets WHERE used_at IS NOT NULL OR expires_at < ?`, store.T(s.now()))
		if err != nil {
			return err
		}
		if out.Resets, err = res.RowsAffected(); err != nil {
			return err
		}
		// Saved, discarded and canceled imports are deleted right away;
		// ready and failed ones wait for review a while.
		res, err = q.Exec(ctx, `DELETE FROM import_jobs WHERE status = 'canceled'
			OR (status IN ('ready', 'failed') AND finished_at < ?)`, store.T(s.now().Add(-ai.ReadyRetention)))
		if err != nil {
			return err
		}
		out.Imports, err = res.RowsAffected()
		return err
	})
	if err != nil || s.images == nil {
		return out, err
	}
	out.Images, err = s.sweepImages(ctx)
	return out, err
}

func (s *Sweeper) sweepImages(ctx context.Context) (int64, error) {
	households, err := s.images.Households()
	if err != nil {
		return 0, err
	}
	cutoff := s.now().Add(-ImageGrace)
	var n int64
	for _, hh := range households {
		stored, err := s.images.List(hh)
		if err != nil {
			return n, err
		}
		var used []string
		if err := s.db.System().Read().Select(ctx, &used,
			`SELECT DISTINCT photo FROM recipes WHERE household_id = ? AND photo IS NOT NULL`, hh); err != nil {
			return n, err
		}
		inUse := make(map[string]bool, len(used))
		for _, h := range used {
			inUse[h] = true
		}
		for _, img := range stored {
			if inUse[img.Hash] || img.ModTime.After(cutoff) {
				continue
			}
			if err := s.images.Remove(hh, img.Hash); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
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
				slog.Int64("entries", swept.Entries), slog.Int64("lists", swept.Lists), slog.Int64("meals", swept.Meals),
				slog.Int64("recipes", swept.Recipes), slog.Int64("images", swept.Images), slog.Int64("imports", swept.Imports))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// LinkCatalog links every household's categories and items to the default
// ingredient catalogue, in the household's language. It covers households
// created before the catalogue and items they added since; it is idempotent
// and changes no names, categories or icons.
func LinkCatalog(ctx context.Context, db *store.DB, g *groceries.Service, defaults func(lang string) groceries.Defaults) error {
	var households []struct {
		ID              uuid.UUID
		DefaultLanguage string
	}
	if err := db.System().Read().Select(ctx, &households, `SELECT id, default_language FROM households`); err != nil {
		return err
	}
	for _, h := range households {
		if err := g.LinkCatalog(ctx, db.ScopeFor(h.ID), defaults(h.DefaultLanguage)); err != nil {
			return fmt.Errorf("link catalogue for household %s: %w", h.ID, err)
		}
	}
	return nil
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

// SplitIngredientAmounts gives recipe ingredients whose amount text did not
// parse as a whole ("600 g gemischt") a quantity, unit and note, so scaling
// and nutrition can use them. Rows that already have a quantity or a note
// are left alone, and text that still has no number stays as it is, so it
// is idempotent. It returns the number of rows changed.
func SplitIngredientAmounts(ctx context.Context, db *store.DB) (int, error) {
	var rows []struct {
		ID         uuid.UUID
		AmountText string
		Language   string
	}
	if err := db.System().Read().Select(ctx, &rows, `SELECT ri.id, ri.amount_text, h.default_language AS language
		FROM recipe_ingredients ri JOIN households h ON h.id = ri.household_id
		WHERE ri.quantity IS NULL AND ri.amount_text <> '' AND ri.note = ''`); err != nil {
		return 0, err
	}
	changed := 0
	err := db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
		for _, r := range rows {
			lang, _ := language.Parse(r.Language)
			a, note := groceries.ParseIngredientAmount(r.AmountText, lang)
			if a.Quantity == nil {
				continue
			}
			unit := &a.Unit
			if a.Unit == "" {
				unit = nil
			}
			if _, err := q.Exec(ctx, `UPDATE recipe_ingredients SET quantity = ?, unit = ?, amount_text = ?, note = ?
				WHERE id = ? AND quantity IS NULL`, a.Quantity, unit, a.Text, note, r.ID); err != nil {
				return err
			}
			changed++
		}
		return nil
	})
	return changed, err
}
