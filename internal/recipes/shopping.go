package recipes

import (
	"context"
	"math"

	"github.com/google/uuid"
	"golang.org/x/text/language"

	"github.com/raphi011/mise/internal/groceries"
	"github.com/raphi011/mise/internal/store"
)

// ListStatus says which of a recipe's ingredients are on the default list
// (the household's first list), for the detail page's +/✓ toggles.
type ListStatus struct {
	List groceries.List
	// On holds the item IDs that have an entry on the list not in the cart.
	On map[uuid.UUID]bool
}

// Count returns how many of the ingredients are on the list, and how many
// can be: optional ingredients count only when they are on it.
func (st ListStatus) Count(ings []Ingredient) (on, total int) {
	for _, ing := range ings {
		switch {
		case st.On[ing.ItemID]:
			on++
			total++
		case !ing.Optional:
			total++
		}
	}
	return on, total
}

// ListStatus returns the default list and which of the ingredients are on
// it.
func (s *Service) ListStatus(ctx context.Context, sc store.Scoped, ings []Ingredient) (ListStatus, error) {
	list, err := s.groceries.FirstList(ctx, sc)
	if err != nil {
		return ListStatus{}, err
	}
	ids := make([]uuid.UUID, len(ings))
	for i, ing := range ings {
		ids[i] = ing.ItemID
	}
	on, err := s.groceries.OnList(ctx, sc, list.ID, ids)
	if err != nil {
		return ListStatus{}, err
	}
	return ListStatus{List: list, On: on}, nil
}

// Toggled describes what ToggleIngredient did, for its Undo toast.
type Toggled struct {
	List     groceries.List
	ItemName string
	// Added is the entry added or merged into; nil when the item was taken
	// off.
	Added *groceries.Entry
	// Removed are the entries taken off, for Undo with groceries Restore.
	Removed []uuid.UUID
}

// listIngredient is a recipe ingredient as it goes onto a list.
type listIngredient struct {
	ItemID     uuid.UUID
	ItemName   string
	Quantity   *float64
	Unit       *string
	AmountText string
	Servings   int
}

// amount is the ingredient's amount for servings (clamped to the recipe's
// own when out of range).
func (ing listIngredient) amount(servings int, lang language.Tag) groceries.Amount {
	if servings < 1 || servings > maxServings {
		servings = ing.Servings
	}
	return Scale(groceries.Amount{Quantity: ing.Quantity, Unit: deref(ing.Unit), Text: ing.AmountText},
		ing.Servings, servings, lang)
}

// getListIngredient reads one ingredient of a live recipe, or returns
// ErrNotFound.
func getListIngredient(ctx context.Context, q store.Reader, sc store.Scoped, recipeID, ingredientID uuid.UUID) (listIngredient, error) {
	var ing listIngredient
	err := q.Get(ctx, &ing, `SELECT ri.item_id, i.name AS item_name, ri.quantity, ri.unit, ri.amount_text, r.servings
		FROM recipe_ingredients ri
		JOIN recipes r ON r.household_id = ri.household_id AND r.id = ri.recipe_id
		JOIN items i ON i.household_id = ri.household_id AND i.id = ri.item_id
		WHERE ri.household_id = ? AND ri.recipe_id = ? AND ri.id = ? AND r.deleted_at IS NULL`,
		sc.HouseholdID(), recipeID, ingredientID)
	return ing, notFound(err)
}

// ToggleIngredient puts one ingredient of a recipe on the default list,
// scaled to servings, or takes it off when on is false. Putting it on
// leaves an item already on the list as it is, so a repeated tap adds
// nothing. Taking it off removes every entry of the item on the list that
// is not in the cart.
func (s *Service) ToggleIngredient(ctx context.Context, sc store.Scoped, by, recipeID, ingredientID uuid.UUID,
	servings int, on bool, lang language.Tag,
) (Toggled, error) {
	ing, err := getListIngredient(ctx, sc.Read(), sc, recipeID, ingredientID)
	if err != nil {
		return Toggled{}, err
	}
	list, err := s.groceries.FirstList(ctx, sc)
	if err != nil {
		return Toggled{}, err
	}
	out := Toggled{List: list, ItemName: ing.ItemName}
	if !on {
		out.Removed, err = s.groceries.RemoveItem(ctx, sc, list.ID, ing.ItemID)
		return out, err
	}
	added, err := s.groceries.Add(ctx, sc, groceries.AddInput{
		ListID: list.ID, ItemID: &ing.ItemID, Amount: ing.amount(servings, lang), AddedBy: by, RecipeID: &recipeID, Lang: lang,
		IfAbsent: true,
	})
	if err != nil {
		return Toggled{}, err
	}
	out.Added = &added.Entry
	return out, nil
}

// PutOn describes what PutOnListTx did.
type PutOn struct {
	List groceries.List
	// Added are the entries put on the list; Present names the items that
	// were already on it and stay as they are.
	Added   []groceries.Entry
	Present []string
}

// PutOnListTx puts ingredients of a recipe on the default list, scaled to
// servings, inside the caller's transaction (planning a meal with its
// shopping). Items already on the list stay as they are. It returns
// ErrNotFound when the recipe is gone or an ingredient is not the
// recipe's. It does not publish: when Added is not empty, the caller
// publishes live.ListChanged for List after commit.
func (s *Service) PutOnListTx(ctx context.Context, q store.Querier, sc store.Scoped, by, recipeID uuid.UUID,
	ingredientIDs []uuid.UUID, servings int, lang language.Tag,
) (PutOn, error) {
	list, err := s.groceries.FirstListTx(ctx, q, sc)
	if err != nil {
		return PutOn{}, err
	}
	out := PutOn{List: list}
	seen := map[uuid.UUID]bool{}
	for _, id := range ingredientIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		ing, err := getListIngredient(ctx, q.Reader, sc, recipeID, id)
		if err != nil {
			return PutOn{}, err
		}
		added, err := s.groceries.AddTx(ctx, q, sc, groceries.AddInput{
			ListID: list.ID, ItemID: &ing.ItemID, Amount: ing.amount(servings, lang), AddedBy: by, RecipeID: &recipeID,
			Lang: lang, IfAbsent: true,
		})
		if err != nil {
			return PutOn{}, err
		}
		if added.Present {
			out.Present = append(out.Present, ing.ItemName)
		} else {
			out.Added = append(out.Added, added.Entry)
		}
	}
	return out, nil
}

// Scale returns the amount for servings when the recipe makes base
// servings. Free-text amounts ("1 thumb") stay as they are.
func Scale(a groceries.Amount, base, servings int, lang language.Tag) groceries.Amount {
	if a.Quantity == nil || base <= 0 || servings <= 0 || base == servings {
		return a
	}
	q := roundQuantity(*a.Quantity * float64(servings) / float64(base))
	return groceries.Amount{Quantity: &q, Unit: a.Unit, Text: groceries.FormatAmount(q, a.Unit, lang)}
}

// roundQuantity rounds a scaled quantity to what a cook would measure:
// halves below 10, whole numbers from 10, tens from 100.
func roundQuantity(q float64) float64 {
	switch {
	case q >= 100:
		return math.Round(q/10) * 10
	case q >= 10:
		return math.Round(q)
	case q >= 1:
		return math.Round(q*2) / 2
	}
	// Small amounts keep two decimals (0.25 tsp), but never round to zero.
	return math.Max(math.Round(q*100)/100, 0.01)
}

// DisplayAmount returns the text to show for an amount: the formatted
// quantity when it has one, else what was typed.
func DisplayAmount(a groceries.Amount, lang language.Tag) string {
	if a.Quantity != nil {
		return groceries.FormatAmount(*a.Quantity, a.Unit, lang)
	}
	return a.Text
}
