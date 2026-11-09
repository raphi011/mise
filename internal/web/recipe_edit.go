package web

import (
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/starfederation/datastar-go/datastar"

	"github.com/raphi011/mise/internal/groceries"
	"github.com/raphi011/mise/internal/i18n"
	"github.com/raphi011/mise/internal/images"
	"github.com/raphi011/mise/internal/recipes"
	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/ui"
)

// editorFieldBytes bounds the editor's text fields together, on top of the
// photo.
const editorFieldBytes = 1 << 20

// photoErrors maps upload errors to their messages.
var photoErrors = []struct {
	err error
	key i18n.Key
}{
	{images.ErrTooLarge, "recipes.photo.tooLarge"},
	{images.ErrTooManyPx, "recipes.photo.tooManyPixels"},
	{images.ErrHEIC, "recipes.photo.heic"},
	{images.ErrUnsupported, "recipes.photo.unsupported"},
}

func photoErrorKey(err error) (key i18n.Key, ok bool) {
	for _, pe := range photoErrors {
		if errors.Is(err, pe.err) {
			return pe.key, true
		}
	}
	return key, false
}

// editorState is the editor form as submitted.
type editorState struct {
	vals   url.Values
	rows   []ui.IngredientRow
	steps  []string
	action string
	// photoErr is the message key of a rejected photo.
	photoErr i18n.Key
}

var editorFields = []string{"name", "description", "prep_min", "cook_min", "servings", "source_url", "notes", "new_tags", "ing_new", "photo"}

// readEditor reads the editor's multipart form. The photo file is the last
// field, so a photo over the limit still leaves every other field read; it
// is stored for the household right away and referenced by its hash.
func (s *Server) readEditor(w http.ResponseWriter, r *http.Request) (editorState, error) {
	ctx := r.Context()
	r.Body = http.MaxBytesReader(w, r.Body, images.MaxUploadBytes+editorFieldBytes)
	var st editorState
	mr, err := r.MultipartReader()
	switch {
	case errors.Is(err, http.ErrNotMultipart):
		if err := r.ParseForm(); err != nil {
			return st, err
		}
		st.vals = r.PostForm
	case err != nil:
		return st, err
	default:
		st.vals = url.Values{}
		if err := s.readParts(ctx, mr, identity(r).HouseholdID, &st); err != nil {
			return st, err
		}
	}

	keys, amounts, names := st.vals["ing_key"], st.vals["ing_amount"], st.vals["ing_name"]
	if len(amounts) != len(keys) || len(names) != len(keys) {
		return st, errBadForm
	}
	optional := st.vals["ing_opt"]
	for i, k := range keys {
		st.rows = append(st.rows, ui.IngredientRow{Key: k, Amount: strings.TrimSpace(amounts[i]), Name: strings.TrimSpace(names[i]), Optional: slices.Contains(optional, k)})
	}
	st.steps = chooseSteps(st.vals)
	st.action = st.vals.Get("action")
	if st.vals.Get("remove_photo") != "" {
		st.vals.Set("photo", "")
	}
	return st, nil
}

var errBadForm = errors.New("web: malformed form")

func (s *Server) readParts(ctx context.Context, mr *multipart.Reader, hh uuid.UUID, st *editorState) error {
	photoSeen := false
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return nil
		}
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			// The photo ran past the limit; the fields before it are in.
			st.photoErr = "recipes.photo.tooLarge"
			return nil
		}
		if err != nil {
			return err
		}
		if part.FormName() == "photo_file" {
			if part.FileName() == "" {
				continue
			}
			// The editor sends one photo; more would each cost a decode.
			if photoSeen {
				return errBadForm
			}
			photoSeen = true
			hash, err := s.images.Save(ctx, hh, part)
			if errors.As(err, &tooBig) {
				st.photoErr = "recipes.photo.tooLarge"
				return nil
			}
			if key, ok := photoErrorKey(err); ok {
				st.photoErr = key
				continue
			}
			if err != nil {
				return err
			}
			st.vals.Set("photo", hash)
			continue
		}
		b, err := io.ReadAll(io.LimitReader(part, editorFieldBytes+1))
		if err != nil {
			return err
		}
		if len(b) > editorFieldBytes {
			return errBadForm
		}
		st.vals.Add(part.FormName(), string(b))
	}
}

// chooseSteps returns the method from whichever field the user edited:
// phones submit one step per line, wider screens one field per step. With
// JavaScript only the visible one is sent; without it both are, and the
// one that differs from the steps as rendered wins.
func chooseSteps(vals url.Values) []string {
	text, hasText := vals["steps_text"]
	fields, hasFields := vals["step"]
	var fromFields []string
	for _, f := range fields {
		fromFields = append(fromFields, strings.Join(strings.Fields(f), " "))
	}
	var fromText []string
	if hasText {
		fromText = strings.Split(strings.ReplaceAll(text[0], "\r\n", "\n"), "\n")
	}
	switch {
	case hasFields && hasText:
		if strings.Join(fromFields, "\n") != vals.Get("steps_orig") {
			return fromFields
		}
		return fromText
	case hasFields:
		return fromFields
	}
	return fromText
}

// nonBlank drops empty steps, so step N of the form is step N of the
// recipe and its errors line up.
func nonBlank(steps []string) []string {
	var out []string
	for _, st := range steps {
		if st = strings.TrimSpace(st); st != "" {
			out = append(out, st)
		}
	}
	return out
}

func newRowKey() string { return "n" + strings.ReplaceAll(store.NewID().String(), "-", "")[20:] }

// savedRowKey is the editor key of an ingredient row the recipe has. It
// carries the row's ID, so a save keeps it (see rowID).
func savedRowKey(id uuid.UUID) string { return "r" + id.String() }

// rowID returns the ingredient ID a row key carries, or zero for a new row.
// The domain ignores IDs the recipe does not have.
func rowID(key string) uuid.UUID {
	rest, ok := strings.CutPrefix(key, "r")
	if !ok {
		return uuid.Nil
	}
	id, err := uuid.Parse(rest)
	if err != nil {
		return uuid.Nil
	}
	return id
}

// parsedRow turns a typed line ("2 tbsp soy sauce") into an editor row.
func parsedRow(ctx context.Context, line string) (ui.IngredientRow, bool) {
	in := recipes.ParseIngredient(line, i18n.Locale(ctx))
	if in.Name == "" && in.Amount.Text == "" {
		return ui.IngredientRow{}, false
	}
	return ui.IngredientRow{Key: newRowKey(), Name: in.Name, Amount: in.Amount.Text}, true
}

// apply performs a row or step action of the form without JavaScript. It
// reports false for a plain save.
func (st *editorState) apply(ctx context.Context) bool {
	verb, arg, _ := strings.Cut(st.action, ":")
	idx := slices.IndexFunc(st.rows, func(r ui.IngredientRow) bool { return r.Key == arg })
	switch verb {
	case "add-ingredient":
		if row, ok := parsedRow(ctx, st.vals.Get("ing_new")); ok {
			st.rows = append(st.rows, row)
			st.vals.Set("ing_new", "")
		}
	case "up":
		if idx > 0 {
			st.rows[idx-1], st.rows[idx] = st.rows[idx], st.rows[idx-1]
		}
	case "down":
		if idx >= 0 && idx < len(st.rows)-1 {
			st.rows[idx+1], st.rows[idx] = st.rows[idx], st.rows[idx+1]
		}
	case "remove":
		if idx >= 0 {
			st.rows = slices.Delete(st.rows, idx, idx+1)
		}
	case "add-step":
		st.steps = append(st.steps, "")
	case "step-remove":
		if i, err := strconv.Atoi(arg); err == nil && i >= 0 && i < len(st.steps) {
			st.steps = slices.Delete(st.steps, i, i+1)
		}
	default:
		return false
	}
	return true
}

// optionalInt parses a whole number field; empty is nil.
func optionalInt(errs map[string]i18n.Key, field, v string) *int {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		errs[field] = "form.error.number"
		return nil
	}
	return &n
}

// input builds the domain input. rowOf maps the input's ingredient index
// to the form row, for "ingredients.N" errors.
func (st *editorState) input(ctx context.Context) (recipes.Input, []int, map[string]i18n.Key) {
	errs := map[string]i18n.Key{}
	lang := i18n.Locale(ctx)
	v := st.vals
	in := recipes.Input{
		Name: v.Get("name"), Description: v.Get("description"), SourceURL: v.Get("source_url"),
		Notes: v.Get("notes"), Photo: v.Get("photo"), Tags: v["tags"],
		PrepMin: optionalInt(errs, "prep_min", v.Get("prep_min")),
		CookMin: optionalInt(errs, "cook_min", v.Get("cook_min")),
	}
	for _, t := range strings.Split(v.Get("new_tags"), ",") {
		if t = strings.TrimSpace(t); t != "" {
			in.Tags = append(in.Tags, t)
		}
	}
	if sv := strings.TrimSpace(v.Get("servings")); sv == "" {
		errs["servings"] = "form.error.required"
	} else if n, err := strconv.Atoi(sv); err != nil {
		errs["servings"] = "form.error.number"
	} else {
		in.Servings = n
	}
	var rowOf []int
	for i, row := range st.rows {
		if row.Name == "" && row.Amount == "" {
			continue
		}
		rowOf = append(rowOf, i)
		in.Ingredients = append(in.Ingredients, recipes.IngredientInput{
			ID: rowID(row.Key), Name: row.Name, Amount: groceries.ParseAmount(row.Amount, lang), Optional: row.Optional,
		})
	}
	st.steps = nonBlank(st.steps)
	in.Steps = st.steps
	return in, rowOf, errs
}

// recipeEditView renders the editor for the form state.
func (s *Server) recipeEditView(ctx context.Context, sc store.Scoped, id string, st editorState, f ui.Form) (ui.RecipeEditView, error) {
	tags, err := s.recipes.Tags(ctx, sc)
	if err != nil {
		return ui.RecipeEditView{}, err
	}
	checked := map[string]bool{}
	for _, t := range st.vals["tags"] {
		checked[groceries.NameKey(t)] = true
	}
	f.Values = map[string]string{}
	for _, name := range editorFields {
		f.Values[name] = st.vals.Get(name)
	}
	v := ui.RecipeEditView{ID: id, Form: f, Photo: st.vals.Get("photo"), Rows: st.rows, Steps: st.steps}
	for _, t := range tags {
		v.Tags = append(v.Tags, ui.TagChoice{Name: t.Name, Checked: checked[groceries.NameKey(t.Name)]})
	}
	return v, nil
}

func (s *Server) renderEditor(w http.ResponseWriter, r *http.Request, status int, sc store.Scoped, id string, st editorState, f ui.Form) {
	ctx := r.Context()
	v, err := s.recipeEditView(ctx, sc, id, st, f)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	title := i18n.T(ctx, "recipes.edit.newTitle")
	if id != "" {
		title = i18n.T(ctx, "recipes.edit.title")
	}
	p := s.page(r, title, ui.NavRecipes)
	p.BackHref, p.BackLabel = v.Cancel(), i18n.T(ctx, "recipes.edit.cancel")
	p.Actions = ui.RecipeEditActions(v)
	s.render(w, r, status, ui.RecipeEdit(p, v))
}

func (s *Server) newRecipePage(w http.ResponseWriter, r *http.Request) {
	sc, err := s.db.Scope(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	st := editorState{vals: url.Values{"servings": {"2"}}}
	s.renderEditor(w, r, http.StatusOK, sc, "", st, ui.Form{})
}

func (s *Server) editRecipePage(w http.ResponseWriter, r *http.Request) {
	recipeID, sc, ok := s.recipeTarget(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	rec, err := s.recipes.Get(ctx, sc, recipeID, identity(r).UserID)
	if errors.Is(err, recipes.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	lang := i18n.Locale(ctx)
	st := editorState{vals: url.Values{
		"name": {rec.Name}, "description": {rec.Description}, "servings": {strconv.Itoa(rec.Servings)},
		"source_url": {rec.SourceURL}, "notes": {rec.Notes}, "photo": {rec.Photo},
	}}
	for field, m := range map[string]*int{"prep_min": rec.PrepMin, "cook_min": rec.CookMin} {
		if m != nil {
			st.vals.Set(field, strconv.Itoa(*m))
		}
	}
	for _, t := range rec.Tags {
		st.vals.Add("tags", t.Name)
	}
	for _, ing := range rec.Ingredients {
		amount := ing.Amount.Text
		if amount == "" {
			amount = recipes.DisplayAmount(ing.Amount, lang)
		}
		st.rows = append(st.rows, ui.IngredientRow{Key: savedRowKey(ing.ID), Name: ing.ItemName, Amount: amount, Optional: ing.Optional})
	}
	for _, step := range rec.Steps {
		st.steps = append(st.steps, step.Text)
	}
	s.renderEditor(w, r, http.StatusOK, sc, recipeID.String(), st, ui.Form{})
}

func (s *Server) createRecipe(w http.ResponseWriter, r *http.Request) {
	sc, err := s.db.Scope(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.saveRecipe(w, r, sc, uuid.Nil)
}

func (s *Server) updateRecipe(w http.ResponseWriter, r *http.Request) {
	recipeID, sc, ok := s.recipeTarget(w, r)
	if !ok {
		return
	}
	s.saveRecipe(w, r, sc, recipeID)
}

// saveRecipe handles every editor post: row and step actions re-render the
// form; a save creates (recipeID nil) or updates the recipe.
func (s *Server) saveRecipe(w http.ResponseWriter, r *http.Request, sc store.Scoped, recipeID uuid.UUID) {
	ctx := r.Context()
	idStr := ""
	if recipeID != uuid.Nil {
		idStr = recipeID.String()
	}
	st, err := s.readEditor(w, r)
	var tooBig *http.MaxBytesError
	if errors.Is(err, errBadForm) || errors.As(err, &tooBig) {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	f := ui.Form{Errors: map[string]string{}}
	if len(st.photoErr) > 0 {
		f.Errors["photo"] = i18n.T(ctx, st.photoErr)
	}
	if st.apply(ctx) {
		s.renderEditor(w, r, http.StatusOK, sc, idStr, st, f)
		return
	}
	// Enter in the add field saves; keep what was typed there.
	if row, ok := parsedRow(ctx, st.vals.Get("ing_new")); ok {
		st.rows = append(st.rows, row)
		st.vals.Set("ing_new", "")
	}
	in, rowOf, local := st.input(ctx)
	if photo := in.Photo; photo != "" {
		if img, err := s.images.Open(sc.HouseholdID(), photo, images.Thumb); err != nil {
			local["photo"] = "form.error.choice"
			st.vals.Set("photo", "")
		} else {
			_ = img.Close()
		}
	}
	if len(local) > 0 || len(f.Errors) > 0 {
		// Show the domain's messages too, except where a field already
		// has its own.
		f, _ = withErrors(ctx, f, recipes.Validate(in))
		for field, key := range local {
			f.Errors[field] = i18n.T(ctx, key)
		}
		s.editorErrors(w, r, sc, idStr, st, rowOf, f)
		return
	}
	user := identity(r).UserID
	if recipeID == uuid.Nil {
		recipeID, err = s.recipes.Create(ctx, sc, user, in)
	} else {
		err = s.recipes.Update(ctx, sc, recipeID, user, in)
	}
	if f, ok := withErrors(ctx, f, err); ok {
		s.editorErrors(w, r, sc, idStr, st, rowOf, f)
		return
	}
	if errors.Is(err, recipes.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	seeOther(w, r, ui.RecipeHref(recipeID.String()))
}

// editorErrors re-renders the editor with its messages, putting each
// "ingredients.N" message on its row.
func (s *Server) editorErrors(w http.ResponseWriter, r *http.Request, sc store.Scoped, id string, st editorState, rowOf []int, f ui.Form) {
	for i, row := range rowOf {
		st.rows[row].Error = f.Error("ingredients." + strconv.Itoa(i))
	}
	s.renderEditor(w, r, http.StatusUnprocessableEntity, sc, id, st, f)
}

// ingredientRow parses the editor's add field into a row appended to the
// list, as the user presses Enter. It only reads.
func (s *Server) ingredientRow(w http.ResponseWriter, r *http.Request) {
	if !isDatastar(r) {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	var sig struct {
		Newing string `json:"newing"`
	}
	if err := datastar.ReadSignals(r, &sig); err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	row, ok := parsedRow(r.Context(), sig.Newing)
	if !ok {
		s.sendPatches(w, r)
		return
	}
	s.sendPatches(w, r,
		func(sse *datastar.ServerSentEventGenerator) error {
			return sse.PatchElementTempl(ui.IngredientRowItem(row), datastar.WithSelectorID("ing-rows"), datastar.WithModeAppend())
		},
		patchSignals(map[string]any{"newing": ""}),
	)
}
