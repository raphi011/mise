package web

import (
	"context"
	"errors"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/starfederation/datastar-go/datastar"

	"github.com/raphi011/mise/internal/ai"
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
	steps  []ui.StepRow
	action string
	// photoErr is the message key of a rejected photo.
	photoErr i18n.Key
}

var editorFields = []string{"name", "description", "prep_min", "cook_min", "servings", "source_url", "notes", "new_tags", "ing_new", "link_new", "photo"}

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
	texts, timers := st.vals["step"], st.vals["step_timer"]
	if len(timers) != len(texts) {
		return st, errBadForm
	}
	for i, text := range texts {
		st.steps = append(st.steps, ui.StepRow{Text: strings.Join(strings.Fields(text), " "), Timer: strings.TrimSpace(timers[i])})
	}
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

// nonBlank drops steps without text or timer, so step N of the form is
// step N of the recipe and its errors line up.
func nonBlank(steps []ui.StepRow) []ui.StepRow {
	var out []ui.StepRow
	for _, st := range steps {
		if st.Text != "" || st.Timer != "" {
			out = append(out, st)
		}
	}
	return out
}

// timerSeconds parses a step's timer, typed in whole minutes; empty is
// nil. Numbers too large to convert become an out-of-range value for the
// domain to reject.
func timerSeconds(errs map[string]i18n.Key, field, minutes string) *int {
	n := optionalInt(errs, field, minutes)
	if n == nil {
		return nil
	}
	secs := math.MaxInt
	if *n <= math.MaxInt/60 {
		secs = *n * 60
	}
	return &secs
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
		st.steps = append(st.steps, ui.StepRow{})
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
		Notes: v.Get("notes"), Photo: v.Get("photo"), Tags: v["tags"], Links: parseIDs(v["link"]),
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
		amount, note := groceries.ParseIngredientAmount(row.Amount, lang)
		in.Ingredients = append(in.Ingredients, recipes.IngredientInput{
			ID: rowID(row.Key), Name: row.Name, Amount: amount, Note: note, Optional: row.Optional,
		})
	}
	st.steps = nonBlank(st.steps)
	for i, row := range st.steps {
		in.Steps = append(in.Steps, recipes.StepInput{
			Text: row.Text, TimerSeconds: timerSeconds(errs, "steps."+strconv.Itoa(i)+".timer", row.Timer),
		})
	}
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
	if id == "" {
		if imp, err := uuid.Parse(st.vals.Get("import")); err == nil {
			v.Import = imp.String()
			v.ImportPhoto = st.vals.Get("import_photo") != "" && st.vals.Get("remove_photo") == ""
		} else {
			// A new recipe offers import; links import without AI too.
			aiSettings, err := s.ai.Settings(ctx, sc)
			if err != nil {
				return ui.RecipeEditView{}, err
			}
			v.ImportCard = "link"
			if aiSettings.Enabled {
				v.ImportCard = "ai"
			}
		}
	}
	for _, t := range tags {
		v.Tags = append(v.Tags, ui.TagChoice{Name: t.Name, Checked: checked[groceries.NameKey(t.Name)]})
	}
	linked, err := s.recipes.NamesOf(ctx, sc, parseIDs(st.vals["link"]))
	if err != nil {
		return ui.RecipeEditView{}, err
	}
	for _, l := range linked {
		v.Links = append(v.Links, ui.LinkChoice{ID: l.ID.String(), Name: l.Name})
	}
	all, err := s.recipes.Names(ctx, sc)
	if err != nil {
		return ui.RecipeEditView{}, err
	}
	for _, n := range all {
		if n.ID.String() == id || slices.ContainsFunc(linked, func(l recipes.RecipeName) bool { return l.ID == n.ID }) {
			continue
		}
		v.LinkOptions = append(v.LinkOptions, n.Name)
	}
	return v, nil
}

// addLink links the recipe named in the "Goes well with" field and clears
// it. unknown reports a name no other recipe has; an empty field does
// nothing.
func (s *Server) addLink(ctx context.Context, sc store.Scoped, self uuid.UUID, st *editorState) (unknown bool, err error) {
	name := strings.TrimSpace(st.vals.Get("link_new"))
	if name == "" {
		return false, nil
	}
	found, ok, err := s.recipes.FindByName(ctx, sc, name)
	if err != nil {
		return false, err
	}
	if !ok || found.ID == self {
		return true, nil
	}
	if !slices.Contains(parseIDs(st.vals["link"]), found.ID) {
		st.vals.Add("link", found.ID.String())
	}
	st.vals.Set("link_new", "")
	return false, nil
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
	p.Bar = true
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
	if raw := r.URL.Query().Get("import"); raw != "" {
		jobID, err := uuid.Parse(raw)
		if err != nil {
			s.notFound(w, r)
			return
		}
		job, err := s.ai.Job(r.Context(), sc, identity(r).UserID, jobID)
		if errors.Is(err, ai.ErrJobNotFound) {
			s.notFound(w, r)
			return
		}
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		if job.Status != ai.JobReady || job.Draft == nil {
			http.Redirect(w, r, ui.ImportHref(jobID.String()), http.StatusFound)
			return
		}
		st = importEditorState(r.Context(), job)
	}
	s.renderEditor(w, r, http.StatusOK, sc, "", st, ui.Form{})
}

// importEditorState fills the editor with an import's draft: ingredients
// as typed lines, matched to items by name when saved.
func importEditorState(ctx context.Context, job ai.Job) editorState {
	d := job.Draft
	servings := d.Servings
	if servings == 0 {
		servings = ai.DefaultServings
	}
	st := editorState{vals: url.Values{
		"name": {d.Name}, "description": {d.Description}, "servings": {strconv.Itoa(servings)},
		"source_url": {d.SourceURL}, "import": {job.ID.String()},
	}}
	if d.ImageURL != "" {
		st.vals.Set("import_photo", "1")
	}
	for field, m := range map[string]int{"prep_min": d.PrepMin, "cook_min": d.CookMin} {
		if m > 0 {
			st.vals.Set(field, strconv.Itoa(m))
		}
	}
	for _, t := range d.Tags {
		st.vals.Add("tags", t)
	}
	lang := i18n.Locale(ctx)
	for _, ing := range d.Ingredients {
		st.rows = append(st.rows, ui.IngredientRow{Key: newRowKey(), Name: ing.ItemName, Amount: recipes.AmountField(ing.Amount, "", lang)})
	}
	for _, step := range d.Steps {
		row := ui.StepRow{Text: step.Text}
		if step.TimerSeconds > 0 {
			row.Timer = strconv.Itoa((step.TimerSeconds + 59) / 60)
		}
		st.steps = append(st.steps, row)
	}
	return st
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
	for _, l := range rec.Links {
		st.vals.Add("link", l.ID.String())
	}
	for _, ing := range rec.Ingredients {
		st.rows = append(st.rows, ui.IngredientRow{Key: savedRowKey(ing.ID), Name: ing.ItemName, Amount: recipes.AmountField(ing.Amount, ing.Note, lang), Optional: ing.Optional})
	}
	for _, step := range rec.Steps {
		row := ui.StepRow{Text: step.Text}
		if step.TimerSeconds != nil {
			row.Timer = strconv.Itoa((*step.TimerSeconds + 59) / 60)
		}
		st.steps = append(st.steps, row)
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
	// A name in the "Goes well with" field is added by its button and by
	// a save, like the ingredient field.
	unknownLink, err := s.addLink(ctx, sc, recipeID, &st)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if st.action == "add-link" {
		status := http.StatusOK
		if unknownLink {
			f.Errors["link_new"] = i18n.T(ctx, "recipes.edit.linkUnknown")
			status = http.StatusUnprocessableEntity
		}
		s.renderEditor(w, r, status, sc, idStr, st, f)
		return
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
	if unknownLink {
		local["link_new"] = "recipes.edit.linkUnknown"
	}
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
	photoFailed := false
	importID, importErr := uuid.Parse(st.vals.Get("import"))
	switch {
	case recipeID == uuid.Nil && importErr == nil:
		// Opened from an import: its photo unless removed or replaced, and
		// the job goes with the save.
		if in.Photo == "" && st.vals.Get("remove_photo") == "" && st.vals.Get("import_photo") != "" {
			if job, err := s.ai.Job(ctx, sc, user, importID); err == nil {
				in.Photo, photoFailed = s.importPhoto(ctx, sc, job)
			}
		}
		recipeID, err = s.ai.SaveEdited(ctx, sc, user, importID, in)
	case recipeID == uuid.Nil:
		recipeID, err = s.recipes.Create(ctx, sc, user, in)
	default:
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
	if photoFailed {
		s.setImportedFlash(w, r, recipeID)
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
