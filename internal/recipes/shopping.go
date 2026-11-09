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

// ToggleIngredient puts one ingredient of a recipe on the default list,
// scaled to servings, or takes it off when on is false. Taking it off
// removes every entry of the item on the list that is not in the cart.
func (s *Service) ToggleIngredient(ctx context.Context, sc store.Scoped, by, recipeID, ingredientID uuid.UUID,
	servings int, on bool, lang language.Tag,
) (Toggled, error) {
	var ing struct {
		ItemID     uuid.UUID
		ItemName   string
		Quantity   *float64
		Unit       *string
		AmountText string
		Servings   int
	}
	err := sc.Read().Get(ctx, &ing, `SELECT ri.item_id, i.name AS item_name, ri.quantity, ri.unit, ri.amount_text, r.servings
		FROM recipe_ingredients ri
		JOIN recipes r ON r.household_id = ri.household_id AND r.id = ri.recipe_id
		JOIN items i ON i.household_id = ri.household_id AND i.id = ri.item_id
		WHERE ri.household_id = ? AND ri.recipe_id = ? AND ri.id = ? AND r.deleted_at IS NULL`,
		sc.HouseholdID(), recipeID, ingredientID)
	if err != nil {
		return Toggled{}, notFound(err)
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
	amount := Scale(groceries.Amount{Quantity: ing.Quantity, Unit: deref(ing.Unit), Text: ing.AmountText},
		ing.Servings, servings, lang)
	added, err := s.groceries.Add(ctx, sc, groceries.AddInput{
		ListID: list.ID, ItemID: &ing.ItemID, Amount: amount, AddedBy: by, RecipeID: &recipeID, Lang: lang,
	})
	if err != nil {
		return Toggled{}, err
	}
	out.Added = &added.Entry
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
