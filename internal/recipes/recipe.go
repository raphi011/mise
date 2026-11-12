package recipes

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"golang.org/x/text/language"

	"github.com/raphi011/mise/internal/groceries"
	"github.com/raphi011/mise/internal/live"
	"github.com/raphi011/mise/internal/store"
)

// Recipe is a recipe with everything its detail page shows.
type Recipe struct {
	ID          uuid.UUID
	Name        string
	Description string
	// Photo is the image's content hash (see internal/images), or empty.
	Photo     string
	PrepMin   *int
	CookMin   *int
	Servings  int
	SourceURL string
	Notes     string
	// NotesBy and NotesAt say who last changed the notes and when.
	NotesBy     *Person
	NotesAt     *time.Time
	CreatedBy   Person
	UpdatedBy   Person
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Tags        []Tag
	Ingredients []Ingredient
	Steps       []Step
	// Links are the recipes that go well with this one, both ways.
	Links []Link
	// Favourite is true when the viewer marked the recipe.
	Favourite    bool
	TimesCooked  int
	LastCookedAt *time.Time
	// Shared is true when the recipe has a public link (see share.go).
	Shared bool
}

// TotalMin is prep plus cook time, or nil when neither is known.
func (r Recipe) TotalMin() *int { return totalMin(r.PrepMin, r.CookMin) }

func totalMin(prep, cook *int) *int {
	if prep == nil && cook == nil {
		return nil
	}
	t := 0
	if prep != nil {
		t += *prep
	}
	if cook != nil {
		t += *cook
	}
	return &t
}

// Ingredient is one ingredient row of a recipe.
type Ingredient struct {
	ID       uuid.UUID
	ItemID   uuid.UUID
	ItemName string
	Amount   groceries.Amount
	// Note is what follows the amount ("gemischt" in "600 g gemischt").
	Note     string
	Optional bool
	// Icon is the icon key to show for the item, "" for none.
	Icon string
}

// Step is one step of the method.
type Step struct {
	ID   uuid.UUID
	Text string
	// TimerSeconds is the step's timer for cook mode, if any.
	TimerSeconds *int
}

// Input is a recipe as the editor submits it. Ingredients, steps and tags
// replace the recipe's current ones; rows that edit an existing ingredient
// or step keep their IDs.
type Input struct {
	Name        string
	Description string
	PrepMin     *int
	CookMin     *int
	Servings    int
	SourceURL   string
	Notes       string
	// Photo is an image content hash from internal/images, or empty.
	Photo string
	Tags  []string
	// Links are the recipes that go well with this one. The link goes both
	// ways; IDs that are not live recipes of the household are ignored.
	Links       []uuid.UUID
	Ingredients []IngredientInput
	// Steps are the method's steps in order; steps without text or timer
	// are dropped.
	Steps []StepInput
}

// StepInput is one step of the method as the editor submits it.
type StepInput struct {
	Text string
	// TimerSeconds is the step's cook-mode timer, or nil for none.
	TimerSeconds *int
}

// IngredientInput is one ingredient row. ItemID picks a catalogue item;
// when nil, Name names the item: an existing one with the same
// groceries.NameKey, or a new one.
type IngredientInput struct {
	// ID is the row's ID when it edits an existing ingredient of the
	// recipe, so the row keeps it across saves (detail pages that have not
	// refreshed still reference it). Zero, or an ID the recipe does not
	// have, adds a row.
	ID       uuid.UUID
	ItemID   *uuid.UUID
	Name     string
	Amount   groceries.Amount
	Note     string
	Optional bool
	// Category names the category a new item is filed under when saving
	// creates it (imports propose one); ignored for existing items and
	// categories the household does not have.
	Category string
}

// ParseIngredient reads an ingredient typed as one line, such as
// "2 tbsp soy sauce" or "2 EL Sojasoße", with the amount parser of the add
// field.
func ParseIngredient(line string, lang language.Tag) IngredientInput {
	a, name := groceries.ParseEntry(line, lang)
	return IngredientInput{Name: name, Amount: a}
}

// Field limits.
const (
	maxNameLen        = 120
	maxDescriptionLen = 500
	maxNotesLen       = 5000
	maxURLLen         = 2000
	maxStepLen        = 2000
	maxSteps          = 60
	maxIngredients    = 80
	maxTags           = 20
	maxTagLen         = 40
	maxItemNameLen    = 80
	maxAmountLen      = 40
	maxNoteLen        = 80
	maxMinutes        = 24 * 60
	maxTimerSeconds   = maxMinutes * 60
	maxServings       = 100
)

var photoHash = regexp.MustCompile(`^[0-9a-f]{64}$`)

func checkLen(errs FieldErrors, field, s string, limit int) {
	if utf8.RuneCountInString(s) > limit {
		errs[field] = "form.error.tooLong"
	}
}

// clean normalises the input in place and returns what is wrong with it.
func (in *Input) clean() error {
	errs := FieldErrors{}
	in.Name = strings.Join(strings.Fields(in.Name), " ")
	in.Description = strings.TrimSpace(in.Description)
	in.SourceURL = strings.TrimSpace(in.SourceURL)
	in.Notes = strings.TrimSpace(in.Notes)

	if in.Name == "" {
		errs["name"] = "form.error.required"
	}
	checkLen(errs, "name", in.Name, maxNameLen)
	checkLen(errs, "description", in.Description, maxDescriptionLen)
	checkLen(errs, "notes", in.Notes, maxNotesLen)
	for field, m := range map[string]*int{"prep_min": in.PrepMin, "cook_min": in.CookMin} {
		if m != nil && (*m < 0 || *m > maxMinutes) {
			errs[field] = "form.error.range"
		}
	}
	if in.Servings < 1 || in.Servings > maxServings {
		errs["servings"] = "form.error.range"
	}
	if in.SourceURL != "" {
		u, err := url.Parse(in.SourceURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(in.SourceURL) > maxURLLen {
			errs["source_url"] = "form.error.url"
		}
	}
	if in.Photo != "" && !photoHash.MatchString(in.Photo) {
		errs["photo"] = "form.error.choice"
	}

	tags := in.Tags[:0:0]
	seen := map[string]bool{}
	for _, t := range in.Tags {
		t = groceries.CleanName(t)
		key := groceries.NameKey(t)
		if t == "" || seen[key] {
			continue
		}
		seen[key] = true
		checkLen(errs, "tags", t, maxTagLen)
		tags = append(tags, t)
	}
	in.Tags = tags
	if len(in.Tags) > maxTags {
		errs["tags"] = "form.error.tooMany"
	}

	in.Links = cleanLinks(in.Links, uuid.Nil)
	if len(in.Links) > maxLinks {
		errs["links"] = "form.error.tooMany"
	}

	if len(in.Ingredients) > maxIngredients {
		errs["ingredients"] = "form.error.tooMany"
	}
	for i := range in.Ingredients {
		ing := &in.Ingredients[i]
		field := "ingredients." + strconv.Itoa(i)
		if ing.ItemID == nil {
			ing.Name = groceries.CleanName(ing.Name)
			if ing.Name == "" {
				errs[field] = "form.error.required"
			}
			checkLen(errs, field, ing.Name, maxItemNameLen)
		}
		checkLen(errs, field, ing.Amount.Text, maxAmountLen)
		ing.Note = strings.TrimSpace(ing.Note)
		checkLen(errs, field, ing.Note, maxNoteLen)
	}

	steps := in.Steps[:0:0]
	for _, st := range in.Steps {
		st.Text = strings.TrimSpace(st.Text)
		if st.Text != "" || st.TimerSeconds != nil {
			steps = append(steps, st)
		}
	}
	in.Steps = steps
	if len(in.Steps) > maxSteps {
		errs["steps"] = "form.error.tooMany"
	}
	for i, st := range in.Steps {
		field := "steps." + strconv.Itoa(i)
		if st.Text == "" {
			errs[field] = "form.error.required"
		}
		checkLen(errs, field, st.Text, maxStepLen)
		if t := st.TimerSeconds; t != nil && (*t < 1 || *t > maxTimerSeconds) {
			errs[field+".timer"] = "form.error.range"
		}
	}
	return errs.orNil()
}

// Validate reports what is wrong with in as FieldErrors, without saving
// it, so a form can show every problem at once. Ingredients that pick an
// item by ID are checked only when saved.
func Validate(in Input) error {
	in.Tags = slices.Clone(in.Tags)
	in.Links = slices.Clone(in.Links)
	in.Ingredients = slices.Clone(in.Ingredients)
	in.Steps = slices.Clone(in.Steps)
	return in.clean()
}

// Create adds a recipe written by the member by and returns its ID.
func (s *Service) Create(ctx context.Context, sc store.Scoped, by uuid.UUID, in Input) (uuid.UUID, error) {
	return s.CreateWith(ctx, sc, by, in, nil)
}

// CreateWith is Create that also runs also, when not nil, inside the
// recipe's transaction; an error from it rolls the recipe back. Saving an
// import deletes the import job this way.
func (s *Service) CreateWith(ctx context.Context, sc store.Scoped, by uuid.UUID, in Input, also func(context.Context, store.Querier) error) (uuid.UUID, error) {
	if err := in.clean(); err != nil {
		return uuid.Nil, err
	}
	id := store.NewID()
	var newItems bool
	var linked []uuid.UUID
	err := sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		now := s.clock()
		var notesBy *uuid.UUID
		var notesAt *store.Time
		if in.Notes != "" {
			notesBy, notesAt = &by, &now
		}
		if _, err := q.Exec(ctx, `INSERT INTO recipes (id, household_id, name, name_key, description, photo, prep_min, cook_min,
				servings, source_url, notes, notes_by, notes_at, created_by, updated_by, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, sc.HouseholdID(), in.Name, groceries.NameKey(in.Name), in.Description, nullable(in.Photo), in.PrepMin, in.CookMin,
			in.Servings, nullable(in.SourceURL), in.Notes, notesBy, notesAt, by, by, now, now); err != nil {
			return err
		}
		var err error
		if newItems, err = s.writeChildren(ctx, q, sc, id, in, now, false); err != nil {
			return err
		}
		if linked, err = writeLinks(ctx, q, sc, id, in.Links, now); err != nil {
			return err
		}
		if also != nil {
			return also(ctx, q)
		}
		return nil
	})
	if err != nil {
		return uuid.Nil, err
	}
	events := append(linkEvents(linked), recipesChanged)
	if newItems {
		events = append(events, catalogChanged)
	}
	s.publish(sc, events...)
	return id, nil
}

// Update replaces a recipe's fields, ingredients, steps and tags. The notes
// keep their author unless their text changed.
func (s *Service) Update(ctx context.Context, sc store.Scoped, id, by uuid.UUID, in Input) error {
	if err := in.clean(); err != nil {
		return err
	}
	var newItems bool
	var linked []uuid.UUID
	err := sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		if err := lockRecipe(ctx, q, sc, id); err != nil {
			return err
		}
		hh := sc.HouseholdID()
		now := s.clock()
		// Changed notes record who wrote them; cleared notes have no author.
		var notesBy *uuid.UUID
		var notesAt *store.Time
		if in.Notes != "" {
			notesBy, notesAt = &by, &now
		}
		if _, err := q.Exec(ctx, `UPDATE recipes SET name = ?, name_key = ?, description = ?, photo = ?, prep_min = ?,
				cook_min = ?, servings = ?, source_url = ?,
				notes_by = CASE WHEN notes = ? THEN notes_by ELSE ? END,
				notes_at = CASE WHEN notes = ? THEN notes_at ELSE ? END,
				notes = ?, updated_by = ?, updated_at = ?
			WHERE household_id = ? AND id = ?`,
			in.Name, groceries.NameKey(in.Name), in.Description, nullable(in.Photo), in.PrepMin,
			in.CookMin, in.Servings, nullable(in.SourceURL),
			in.Notes, notesBy, in.Notes, notesAt,
			in.Notes, by, now, hh, id); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, `DELETE FROM recipe_tags WHERE household_id = ? AND recipe_id = ?`, hh, id); err != nil {
			return err
		}
		var err error
		if newItems, err = s.writeChildren(ctx, q, sc, id, in, now, true); err != nil {
			return err
		}
		linked, err = writeLinks(ctx, q, sc, id, cleanLinks(in.Links, id), now)
		return err
	})
	if err != nil {
		return err
	}
	events := append(linkEvents(linked), recipeChanged(id), recipesChanged)
	if newItems {
		events = append(events, catalogChanged)
	}
	s.publish(sc, events...)
	return nil
}

// writeChildren writes the ingredients, steps and tags of a recipe. With
// existing, the recipe already has ingredients and steps: rows that match
// one are updated in place, the rest inserted, and the ones left over
// deleted. Tags must already be gone. newItems reports grocery items it
// created.
func (s *Service) writeChildren(ctx context.Context, q store.Querier, sc store.Scoped, id uuid.UUID, in Input, now store.Time, existing bool) (newItems bool, err error) {
	if newItems, err = s.writeIngredients(ctx, q, sc, id, in.Ingredients, now, existing); err != nil {
		return false, err
	}
	if err := writeSteps(ctx, q, sc, id, in.Steps, now, existing); err != nil {
		return false, err
	}
	hh := sc.HouseholdID()
	for _, name := range in.Tags {
		tagID, err := ensureTag(ctx, q, sc, name, now)
		if err != nil {
			return false, err
		}
		if _, err := q.Exec(ctx, `INSERT INTO recipe_tags (household_id, recipe_id, tag_id, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?)`, hh, id, tagID, now, now); err != nil {
			return false, err
		}
	}
	return newItems, nil
}

// childIDs returns the IDs of a recipe's rows in table, in order.
func childIDs(ctx context.Context, q store.Querier, sc store.Scoped, table string, recipe uuid.UUID) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := q.Select(ctx, &ids, `SELECT id FROM `+table+` WHERE household_id = ? AND recipe_id = ? ORDER BY position, id`,
		sc.HouseholdID(), recipe)
	return ids, err
}

// deleteChildren deletes the rows of table whose IDs are left in stale.
func deleteChildren(ctx context.Context, q store.Querier, sc store.Scoped, table string, recipe uuid.UUID, stale map[uuid.UUID]bool) error {
	for rowID := range stale {
		if _, err := q.Exec(ctx, `DELETE FROM `+table+` WHERE household_id = ? AND recipe_id = ? AND id = ?`,
			sc.HouseholdID(), recipe, rowID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) writeIngredients(ctx context.Context, q store.Querier, sc store.Scoped, id uuid.UUID, ings []IngredientInput, now store.Time, existing bool) (newItems bool, err error) {
	hh := sc.HouseholdID()
	stale := map[uuid.UUID]bool{}
	if existing {
		ids, err := childIDs(ctx, q, sc, "recipe_ingredients", id)
		if err != nil {
			return false, err
		}
		for _, rowID := range ids {
			stale[rowID] = true
		}
	}
	for i, ing := range ings {
		var itemID uuid.UUID
		if ing.ItemID != nil {
			if err := q.Get(ctx, &itemID, `SELECT id FROM items WHERE household_id = ? AND id = ?`, hh, *ing.ItemID); err != nil {
				if err = notFound(err); errors.Is(err, ErrNotFound) {
					return false, FieldErrors{"ingredients." + strconv.Itoa(i): "form.error.choice"}
				}
				return false, err
			}
		} else {
			var created bool
			if itemID, created, err = groceries.EnsureItemIn(ctx, q, sc, ing.Name, ing.Category, now); err != nil {
				return false, err
			}
			newItems = newItems || created
		}
		// A row keeps its ID once; a repeated or unknown ID adds a row.
		if stale[ing.ID] {
			delete(stale, ing.ID)
			if _, err := q.Exec(ctx, `UPDATE recipe_ingredients SET item_id = ?, quantity = ?, unit = ?, amount_text = ?,
					note = ?, optional = ?, position = ?, updated_at = ?
				WHERE household_id = ? AND recipe_id = ? AND id = ?`,
				itemID, ing.Amount.Quantity, nullable(ing.Amount.Unit), ing.Amount.Text,
				ing.Note, ing.Optional, i, now, hh, id, ing.ID); err != nil {
				return false, err
			}
			continue
		}
		if _, err := q.Exec(ctx, `INSERT INTO recipe_ingredients (id, household_id, recipe_id, item_id, quantity, unit,
				amount_text, note, optional, position, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			store.NewID(), hh, id, itemID, ing.Amount.Quantity, nullable(ing.Amount.Unit),
			ing.Amount.Text, ing.Note, ing.Optional, i, now, now); err != nil {
			return false, err
		}
	}
	return newItems, deleteChildren(ctx, q, sc, "recipe_ingredients", id, stale)
}

// writeSteps keeps the ID of every step whose text is unchanged, wherever
// it moved, and updates its position and timer; the editor posts steps as
// plain text, so an edited step is a new one.
func writeSteps(ctx context.Context, q store.Querier, sc store.Scoped, id uuid.UUID, steps []StepInput, now store.Time, existing bool) error {
	hh := sc.HouseholdID()
	type oldStep struct {
		ID           uuid.UUID
		Position     int
		TimerSeconds *int
	}
	byText := map[string][]oldStep{}
	stale := map[uuid.UUID]bool{}
	if existing {
		var old []struct {
			oldStep
			Text string
		}
		if err := q.Select(ctx, &old, `SELECT id, position, timer_seconds, text FROM steps
			WHERE household_id = ? AND recipe_id = ? ORDER BY position, id`, hh, id); err != nil {
			return err
		}
		for _, st := range old {
			byText[st.Text] = append(byText[st.Text], st.oldStep)
			stale[st.ID] = true
		}
	}
	for i, st := range steps {
		if olds := byText[st.Text]; len(olds) > 0 {
			o := olds[0]
			byText[st.Text] = olds[1:]
			delete(stale, o.ID)
			if o.Position == i && equalInt(o.TimerSeconds, st.TimerSeconds) {
				continue
			}
			if _, err := q.Exec(ctx, `UPDATE steps SET position = ?, timer_seconds = ?, updated_at = ?
				WHERE household_id = ? AND recipe_id = ? AND id = ?`,
				i, st.TimerSeconds, now, hh, id, o.ID); err != nil {
				return err
			}
			continue
		}
		if _, err := q.Exec(ctx, `INSERT INTO steps (id, household_id, recipe_id, position, text, timer_seconds, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, store.NewID(), hh, id, i, st.Text, st.TimerSeconds, now, now); err != nil {
			return err
		}
	}
	return deleteChildren(ctx, q, sc, "steps", id, stale)
}

// equalInt reports whether two optional numbers are equal.
func equalInt(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

type recipeRow struct {
	ID           uuid.UUID
	Name         string
	Description  string
	Photo        *string
	PrepMin      *int
	CookMin      *int
	Servings     int
	SourceURL    *string
	Notes        string
	NotesBy      *uuid.UUID
	NotesByName  *string
	NotesByColor *string
	NotesAt      *store.Time
	CreatedBy    uuid.UUID
	CreatedName  string
	CreatedColor string
	UpdatedBy    uuid.UUID
	UpdatedName  string
	UpdatedColor string
	CreatedAt    store.Time
	UpdatedAt    store.Time
	Favourite    bool
	TimesCooked  int
	LastCookedAt *store.Time
	Shared       bool
}

// Get returns a recipe as the member viewer sees it, or ErrNotFound.
func (s *Service) Get(ctx context.Context, sc store.Scoped, id, viewer uuid.UUID) (Recipe, error) {
	q := sc.Read()
	hh := sc.HouseholdID()
	var r recipeRow
	err := q.Get(ctx, &r, `SELECT r.id, r.name, r.description, r.photo, r.prep_min, r.cook_min, r.servings, r.source_url,
			r.notes, r.notes_by, nu.name AS notes_by_name, nu.avatar_color AS notes_by_color, r.notes_at,
			r.created_by, cu.name AS created_name, cu.avatar_color AS created_color,
			r.updated_by, uu.name AS updated_name, uu.avatar_color AS updated_color,
			r.created_at, r.updated_at,
			EXISTS (SELECT 1 FROM favourites f WHERE f.household_id = r.household_id AND f.recipe_id = r.id AND f.user_id = ?) AS favourite,
			(SELECT count(*) FROM cook_events ce WHERE ce.household_id = r.household_id AND ce.recipe_id = r.id) AS times_cooked,
			(SELECT max(ce.cooked_at) FROM cook_events ce WHERE ce.household_id = r.household_id AND ce.recipe_id = r.id) AS last_cooked_at,
			EXISTS (SELECT 1 FROM recipe_shares sh WHERE sh.household_id = r.household_id AND sh.recipe_id = r.id) AS shared
		FROM recipes r
		JOIN users cu ON cu.id = r.created_by
		JOIN users uu ON uu.id = r.updated_by
		LEFT JOIN users nu ON nu.id = r.notes_by
		WHERE r.household_id = ? AND r.id = ? AND r.deleted_at IS NULL AND r.draft = ?`, viewer, hh, id, false)
	if err != nil {
		return Recipe{}, notFound(err)
	}
	out := Recipe{
		ID: r.ID, Name: r.Name, Description: r.Description, Photo: deref(r.Photo),
		PrepMin: r.PrepMin, CookMin: r.CookMin, Servings: r.Servings, SourceURL: deref(r.SourceURL),
		Notes: r.Notes, NotesAt: timePtr(r.NotesAt),
		CreatedBy: Person{ID: r.CreatedBy, Name: r.CreatedName, Color: r.CreatedColor},
		UpdatedBy: Person{ID: r.UpdatedBy, Name: r.UpdatedName, Color: r.UpdatedColor},
		CreatedAt: r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time,
		Favourite: r.Favourite, TimesCooked: r.TimesCooked, LastCookedAt: timePtr(r.LastCookedAt),
		Shared: r.Shared,
	}
	if r.NotesBy != nil {
		out.NotesBy = &Person{ID: *r.NotesBy, Name: deref(r.NotesByName), Color: deref(r.NotesByColor)}
	}

	if err := loadMethod(ctx, q, hh, id, &out); err != nil {
		return Recipe{}, err
	}
	if err := q.Select(ctx, &out.Tags, `SELECT t.id, t.name, t.pinned, 0 AS count FROM tags t
		JOIN recipe_tags rt ON rt.household_id = t.household_id AND rt.tag_id = t.id
		WHERE t.household_id = ? AND rt.recipe_id = ? ORDER BY t.name_key`, hh, id); err != nil {
		return Recipe{}, err
	}
	if out.Links, err = links(ctx, q, sc, id); err != nil {
		return Recipe{}, err
	}
	return out, nil
}

// loadMethod reads a recipe's ingredients and steps into out.
func loadMethod(ctx context.Context, q store.Reader, hh, id uuid.UUID, out *Recipe) error {
	var ings []struct {
		ID         uuid.UUID
		ItemID     uuid.UUID
		ItemName   string
		Quantity   *float64
		Unit       *string
		AmountText string
		Note       string
		Optional   bool
		Icon       *string
		CatalogKey *string
	}
	if err := q.Select(ctx, &ings, `SELECT ri.id, ri.item_id, i.name AS item_name, i.icon, i.catalog_key, ri.quantity, ri.unit, ri.amount_text, ri.note, ri.optional
		FROM recipe_ingredients ri JOIN items i ON i.household_id = ri.household_id AND i.id = ri.item_id
		WHERE ri.household_id = ? AND ri.recipe_id = ? ORDER BY ri.position`, hh, id); err != nil {
		return err
	}
	for _, ri := range ings {
		out.Ingredients = append(out.Ingredients, Ingredient{
			ID: ri.ID, ItemID: ri.ItemID, ItemName: ri.ItemName, Note: ri.Note, Optional: ri.Optional, Icon: groceries.ShowIcon(ri.Icon, ri.CatalogKey),
			Amount: groceries.Amount{Quantity: ri.Quantity, Unit: deref(ri.Unit), Text: ri.AmountText},
		})
	}
	return q.Select(ctx, &out.Steps, `SELECT id, text, timer_seconds FROM steps
		WHERE household_id = ? AND recipe_id = ? ORDER BY position`, hh, id)
}

// DeleteInfo is what the delete confirmation tells the member.
type DeleteInfo struct {
	Name        string
	TimesCooked int
	// Others are the household's other members, who lose the recipe too.
	Others []Person
}

// DeleteInfo describes what deleting the recipe affects, as seen by viewer.
func (s *Service) DeleteInfo(ctx context.Context, sc store.Scoped, id, viewer uuid.UUID) (DeleteInfo, error) {
	q := sc.Read()
	hh := sc.HouseholdID()
	var out DeleteInfo
	if err := q.QueryRow(ctx, `SELECT r.name,
			(SELECT count(*) FROM cook_events ce WHERE ce.household_id = r.household_id AND ce.recipe_id = r.id)
		FROM recipes r WHERE r.household_id = ? AND r.id = ? AND r.deleted_at IS NULL`, hh, id).
		Scan(&out.Name, &out.TimesCooked); err != nil {
		return DeleteInfo{}, notFound(err)
	}
	err := q.Select(ctx, &out.Others, `SELECT u.id, u.name, u.avatar_color AS color FROM memberships m
		JOIN users u ON u.id = m.user_id
		WHERE m.household_id = ? AND m.removed_at IS NULL AND m.user_id <> ? ORDER BY u.name`, hh, viewer)
	return out, err
}

// Delete moves a recipe to the bin. Undo with Restore within UndoWindow;
// the sweep deletes it for good afterwards.
func (s *Service) Delete(ctx context.Context, sc store.Scoped, id uuid.UUID) error {
	var linked []uuid.UUID
	err := sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		now := s.clock()
		res, err := q.Exec(ctx, `UPDATE recipes SET deleted_at = ?, updated_at = ?
			WHERE household_id = ? AND id = ? AND deleted_at IS NULL`, now, now, sc.HouseholdID(), id)
		if err != nil {
			return err
		}
		if err := mustAffect(res); err != nil {
			return err
		}
		linked, err = linkedIDs(ctx, q, sc, id)
		return err
	})
	if err != nil {
		return err
	}
	s.publish(sc, append(linkEvents(linked), recipeChanged(id), recipesChanged)...)
	return nil
}

// linkEvents refreshes the pages of linked recipes, whose "Goes well with"
// lists show the changed recipe.
func linkEvents(linked []uuid.UUID) []live.Event {
	events := make([]live.Event, 0, len(linked)+2)
	for _, l := range linked {
		events = append(events, recipeChanged(l))
	}
	return events
}

// Restore undoes Delete within UndoWindow, or returns ErrNotFound.
func (s *Service) Restore(ctx context.Context, sc store.Scoped, id uuid.UUID) error {
	var linked []uuid.UUID
	err := sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		res, err := q.Exec(ctx, `UPDATE recipes SET deleted_at = NULL, updated_at = ?
			WHERE household_id = ? AND id = ? AND deleted_at >= ?`, s.clock(), sc.HouseholdID(), id, s.undoCutoff())
		if err != nil {
			return err
		}
		if err := mustAffect(res); err != nil {
			return err
		}
		linked, err = linkedIDs(ctx, q, sc, id)
		return err
	})
	if err != nil {
		return err
	}
	s.publish(sc, append(linkEvents(linked), recipeChanged(id), recipesChanged)...)
	return nil
}

// SetFavourite marks or unmarks a recipe as a favourite of the member user.
// It is idempotent.
func (s *Service) SetFavourite(ctx context.Context, sc store.Scoped, user, id uuid.UUID, on bool) error {
	err := sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		hh := sc.HouseholdID()
		if err := lockRecipe(ctx, q, sc, id); err != nil {
			return err
		}
		if !on {
			_, err := q.Exec(ctx, `DELETE FROM favourites WHERE household_id = ? AND user_id = ? AND recipe_id = ?`, hh, user, id)
			return err
		}
		now := s.clock()
		_, err := q.Exec(ctx, `INSERT INTO favourites (household_id, user_id, recipe_id, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`, hh, user, id, now, now)
		return err
	})
	if err != nil {
		return err
	}
	s.publish(sc, recipeChanged(id), recipesChanged)
	return nil
}
