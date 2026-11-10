package ai

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/groceries"
	"github.com/raphi011/mise/internal/recipes"
	"github.com/raphi011/mise/internal/store"
)

// Review errors.
var (
	// ErrNotReady: the import has no draft to review (still running, or
	// failed).
	ErrNotReady = errors.New("ai: import not ready")
	// ErrNoPhoto: the draft names no photo.
	ErrNoPhoto = errors.New("ai: the draft has no photo")
)

// DefaultServings is what a saved draft that names no servings gets, like
// a new recipe in the editor.
const DefaultServings = 2

// ItemPick is the item the user chose for a review row: an existing item,
// or the name of a new one.
type ItemPick struct {
	ItemID *uuid.UUID
	Name   string
}

// PickItem sets the item of one ingredient row of a ready import's draft.
// Picking an existing item, or naming one the household has, marks the row
// matched; another name makes it a new item.
func (s *Service) PickItem(ctx context.Context, sc store.Scoped, user, id uuid.UUID, row int, pick ItemPick) error {
	var item *groceries.Item
	if pick.ItemID != nil {
		found, err := s.groceries.ItemsByID(ctx, sc, []uuid.UUID{*pick.ItemID})
		if err != nil {
			return err
		}
		it, ok := found[*pick.ItemID]
		if !ok {
			return FieldErrors{"item": "import.error.itemGone"}
		}
		item = &it
	} else {
		pick.Name = groceries.CleanName(pick.Name)
		if pick.Name == "" {
			return FieldErrors{"name": "form.error.required"}
		}
		if len([]rune(pick.Name)) > maxItemName {
			return FieldErrors{"name": "form.error.tooLong"}
		}
		found, err := s.groceries.SearchItems(ctx, sc, pick.Name, 1)
		if err != nil {
			return err
		}
		if len(found) > 0 && found[0].Match == groceries.MatchExact {
			item = &found[0].Item
		}
	}
	err := sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		var raw *string
		err := q.Get(ctx, &raw, `SELECT draft FROM import_jobs WHERE household_id = ? AND user_id = ? AND id = ? AND status = 'ready'`,
			sc.HouseholdID(), user, id)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && raw == nil) {
			return ErrNotReady
		}
		if err != nil {
			return err
		}
		var d Draft
		if err := json.Unmarshal([]byte(*raw), &d); err != nil {
			return fmt.Errorf("ai: job %s draft: %w", id, err)
		}
		if row < 0 || row >= len(d.Ingredients) {
			return ErrJobNotFound
		}
		ing := &d.Ingredients[row]
		if item != nil {
			itemID := item.ID
			ing.ItemID, ing.ItemName, ing.Match, ing.Category = &itemID, item.Name, MatchExact, ""
		} else {
			if groceries.NameKey(pick.Name) != groceries.NameKey(ing.ItemName) || ing.Match != MatchNew {
				// The proposed category was for the proposed item.
				ing.Category = ""
			}
			ing.ItemID, ing.ItemName, ing.Match = nil, pick.Name, MatchNew
		}
		b, err := json.Marshal(d)
		if err != nil {
			return err
		}
		_, err = q.Exec(ctx, `UPDATE import_jobs SET draft = ?, updated_at = ? WHERE household_id = ? AND id = ?`,
			string(b), store.T(s.now()), sc.HouseholdID(), id)
		return err
	})
	if err != nil {
		return err
	}
	s.publishJob(user, id)
	return nil
}

// ImportPhoto fetches a ready draft's photo through the safe fetcher, for
// the review screen and for saving.
func (s *Service) ImportPhoto(ctx context.Context, sc store.Scoped, user, id uuid.UUID) (Fetched, error) {
	job, err := s.Job(ctx, sc, user, id)
	if err != nil {
		return Fetched{}, err
	}
	if job.Status != JobReady || job.Draft == nil {
		return Fetched{}, ErrNotReady
	}
	if job.Draft.ImageURL == "" {
		return Fetched{}, ErrNoPhoto
	}
	return s.fetcher.Fetch(ctx, job.Draft.ImageURL, KindImage)
}

// SaveImport saves a ready import's draft as a recipe by user, with the
// new items it proposes and photo (an image hash, or empty), and deletes
// the job in the same transaction. A second save of the same import
// finds no job (ErrJobNotFound) and saves nothing.
func (s *Service) SaveImport(ctx context.Context, sc store.Scoped, user, id uuid.UUID, photo string) (uuid.UUID, error) {
	job, err := s.Job(ctx, sc, user, id)
	if err != nil {
		return uuid.Nil, err
	}
	if job.Status != JobReady || job.Draft == nil {
		return uuid.Nil, ErrNotReady
	}
	in, err := s.RecipeInput(ctx, sc, *job.Draft)
	if err != nil {
		return uuid.Nil, err
	}
	in.Photo = photo
	recipeID, err := s.recipes.CreateWith(ctx, sc, user, in, func(ctx context.Context, q store.Querier) error {
		res, err := q.Exec(ctx, `DELETE FROM import_jobs WHERE household_id = ? AND user_id = ? AND id = ? AND status = 'ready'`,
			sc.HouseholdID(), user, id)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			return errors.Join(ErrJobNotFound, err)
		}
		return nil
	})
	if err != nil {
		return uuid.Nil, err
	}
	s.publishJob(user, id)
	return recipeID, nil
}

// SaveEdited saves a recipe the editor opened from an import and deletes
// the import, if it is still there, in the same transaction.
func (s *Service) SaveEdited(ctx context.Context, sc store.Scoped, user, id uuid.UUID, in recipes.Input) (uuid.UUID, error) {
	recipeID, err := s.recipes.CreateWith(ctx, sc, user, in, func(ctx context.Context, q store.Querier) error {
		_, err := q.Exec(ctx, `DELETE FROM import_jobs WHERE household_id = ? AND user_id = ? AND id = ? AND status = 'ready'`,
			sc.HouseholdID(), user, id)
		return err
	})
	if err != nil {
		return uuid.Nil, err
	}
	s.publishJob(user, id)
	return recipeID, nil
}

// RecipeInput turns a draft into a recipe. Rows whose item was deleted
// since the import name the item again, so saving recreates it.
func (s *Service) RecipeInput(ctx context.Context, sc store.Scoped, d Draft) (recipes.Input, error) {
	in := recipes.Input{
		Name: d.Name, Description: d.Description, Servings: d.Servings, SourceURL: d.SourceURL, Tags: d.Tags,
		Ingredients: []recipes.IngredientInput{}, Steps: []recipes.StepInput{},
	}
	if in.Servings == 0 {
		in.Servings = DefaultServings
	}
	if d.PrepMin > 0 {
		in.PrepMin = &d.PrepMin
	}
	if d.CookMin > 0 {
		in.CookMin = &d.CookMin
	}
	var ids []uuid.UUID
	for _, ing := range d.Ingredients {
		if ing.ItemID != nil {
			ids = append(ids, *ing.ItemID)
		}
	}
	have, err := s.groceries.ItemsByID(ctx, sc, ids)
	if err != nil {
		return recipes.Input{}, err
	}
	for _, ing := range d.Ingredients {
		row := recipes.IngredientInput{Name: ing.ItemName, Amount: ing.Amount, Category: ing.Category}
		if ing.ItemID != nil {
			if _, ok := have[*ing.ItemID]; ok {
				itemID := *ing.ItemID
				row.ItemID = &itemID
			}
		}
		in.Ingredients = append(in.Ingredients, row)
	}
	for _, st := range d.Steps {
		step := recipes.StepInput{Text: st.Text}
		if st.TimerSeconds > 0 {
			t := st.TimerSeconds
			step.TimerSeconds = &t
		}
		in.Steps = append(in.Steps, step)
	}
	return in, nil
}
