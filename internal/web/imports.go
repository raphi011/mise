package web

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/starfederation/datastar-go/datastar"

	"github.com/raphi011/mise/internal/ai"
	"github.com/raphi011/mise/internal/auth"
	"github.com/raphi011/mise/internal/i18n"
	"github.com/raphi011/mise/internal/live"
	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/ui"
)

// importRefresh is how often the running screen reloads itself without
// JavaScript, in seconds.
const importRefresh = 3

// importedCookie remembers, for the saved recipe's page, that the import's
// photo could not be downloaded.
const importedCookie = "mise_imported"

// ---- Form ----

// importTitle is the import pages' title: "Import with AI" when AI import
// is on, else "Import a recipe" (link import works without it).
func importTitle(ctx context.Context, aiOn bool) string {
	if aiOn {
		return i18n.T(ctx, "import.titleAI")
	}
	return i18n.T(ctx, "import.title")
}

// importForm is the form's view with the household's AI settings.
func (s *Server) importForm(ctx context.Context, sc store.Scoped, id *auth.Identity, f ui.Form) (ui.ImportFormView, error) {
	st, err := s.ai.Settings(ctx, sc)
	if err != nil {
		return ui.ImportFormView{}, err
	}
	return ui.ImportFormView{Form: f, Tab: ui.ImportTabLink, AI: st.Enabled, Model: st.Model, Admin: id.IsAdmin()}, nil
}

func (s *Server) renderImportForm(w http.ResponseWriter, r *http.Request, status int, v ui.ImportFormView) {
	p := s.page(r, importTitle(r.Context(), v.AI), ui.NavRecipes)
	p.BackHref, p.BackLabel = "/recipes", i18n.T(r.Context(), "recipes.back")
	s.render(w, r, status, ui.ImportForm(p, v))
}

func (s *Server) importFormPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	v, err := s.importForm(ctx, sc, identity(r), ui.Form{})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderImportForm(w, r, http.StatusOK, v)
}

func (s *Server) startImport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	user := identity(r).UserID
	in := ai.ImportInput{URL: r.PostFormValue("url")}
	replace, replaceErr := uuid.Parse(r.PostFormValue("replace"))
	id, err := s.ai.StartImport(ctx, sc, user, in)
	if err == nil {
		// "Import again" on a failed import takes its place.
		if replaceErr == nil {
			if job, err := s.ai.Job(ctx, sc, user, replace); err == nil && job.Status == ai.JobFailed {
				if err := s.ai.CancelImport(ctx, sc, user, replace); err != nil && !errors.Is(err, ai.ErrJobNotFound) {
					s.log.WarnContext(ctx, "replace failed import", "job", replace.String(), "err", err)
				}
			}
		}
		seeOther(w, r, ui.ImportHref(id.String()))
		return
	}
	f := ui.Form{Values: map[string]string{"url": in.URL}}
	switch {
	case errors.Is(err, ai.ErrTooManyJobs):
		f.Alert = i18n.T(ctx, "import.error.tooMany", ai.MaxActiveJobs)
	default:
		var ok bool
		if f, ok = withErrors(ctx, f, err); !ok {
			s.serverError(w, r, err)
			return
		}
	}
	v, err := s.importForm(ctx, sc, identity(r), f)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if replaceErr == nil {
		v.Replace = replace.String()
	}
	s.renderImportForm(w, r, http.StatusUnprocessableEntity, v)
}

// ---- Job page ----

// importTarget loads the viewer's import job named by the path.
func (s *Server) importTarget(w http.ResponseWriter, r *http.Request) (ai.Job, store.Scoped, bool) {
	ctx := r.Context()
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.notFound(w, r)
		return ai.Job{}, store.Scoped{}, false
	}
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return ai.Job{}, store.Scoped{}, false
	}
	job, err := s.ai.Job(ctx, sc, identity(r).UserID, id)
	if errors.Is(err, ai.ErrJobNotFound) {
		s.notFound(w, r)
		return ai.Job{}, store.Scoped{}, false
	}
	if err != nil {
		s.serverError(w, r, err)
		return ai.Job{}, store.Scoped{}, false
	}
	return job, sc, true
}

// importPage shows a job as running, failed or ready for review.
func (s *Server) importPage(w http.ResponseWriter, r *http.Request) {
	job, sc, ok := s.importTarget(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	st, err := s.ai.Settings(ctx, sc)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	p := s.page(r, importTitle(ctx, st.Enabled), ui.NavRecipes)
	p.BackHref, p.BackLabel = "/recipes", i18n.T(ctx, "recipes.back")
	switch {
	case job.Active():
		p.Refresh = importRefresh
		s.render(w, r, http.StatusOK, ui.ImportRunning(p, runningView(ctx, job)))
	case job.Status == ai.JobReady && job.Draft != nil:
		p.Title = i18n.T(ctx, "import.review.title")
		s.render(w, r, http.StatusOK, ui.ImportReview(p, reviewView(ctx, job)))
	default:
		v := ui.ImportFormView{
			Form: ui.Form{Values: map[string]string{"url": job.SourceURL}}, Tab: ui.ImportTabLink,
			AI: st.Enabled, Model: st.Model, Admin: identity(r).IsAdmin(), Failure: importFailure(ctx, job, st.Enabled),
			Replace: job.ID.String(),
		}
		s.render(w, r, http.StatusOK, ui.ImportForm(p, v))
	}
}

// importSource is a job's source as the running screen shows it.
func importSource(ctx context.Context, job ai.Job) string {
	if job.SourceURL == "" {
		return i18n.T(ctx, "import.source.text")
	}
	u, err := ai.ParseURL(job.SourceURL)
	if err != nil {
		return job.SourceURL
	}
	return strings.TrimPrefix(strings.TrimSuffix(u.Host+u.EscapedPath(), "/"), "www.")
}

func runningView(ctx context.Context, job ai.Job) ui.ImportRunningView {
	v := ui.ImportRunningView{ID: job.ID.String(), Source: importSource(ctx, job), Queued: job.Status == ai.JobQueued,
		Steps: importSteps(ctx, job.Steps)}
	// Imports have no fixed number of steps: each finished one moves the
	// bar on, and it stays short of the end until the draft is there.
	v.Progress = 5
	for _, st := range job.Steps {
		if st.State != ai.StepRunning {
			v.Progress += 15
		}
	}
	v.Progress = min(v.Progress, 90)
	return v
}

func importSteps(ctx context.Context, steps []ai.JobStep) []ui.ImportStep {
	out := make([]ui.ImportStep, 0, len(steps))
	for _, st := range steps {
		out = append(out, ui.ImportStep{Text: stepText(ctx, st), Tool: st.Tool, State: string(st.State)})
	}
	return out
}

// stepText words a step in the viewer's language.
func stepText(ctx context.Context, st ai.JobStep) string {
	switch st.Kind {
	case ai.StepMatch:
		matched, total, ok := strings.Cut(st.Text, "/")
		m, err1 := strconv.Atoi(matched)
		n, err2 := strconv.Atoi(total)
		if !ok || err1 != nil || err2 != nil {
			return i18n.T(ctx, "import.step.matchAny")
		}
		return i18n.T(ctx, "import.step.match", m, n)
	case ai.StepReadText:
		return i18n.T(ctx, "import.step.read_text")
	case ai.StepReadPage, ai.StepFetchPage, ai.StepSearchItems, ai.StepCreateItem, ai.StepCreateRecipe:
		return i18n.T(ctx, i18n.Key("import.step."+string(st.Kind)), st.Text)
	}
	return st.Text
}

// importFailure words a failed job's error code.
func importFailure(ctx context.Context, job ai.Job, aiOn bool) *ui.ImportFailure {
	code := string(job.ErrorCode)
	switch job.ErrorCode {
	case ai.CodeNoRecipe:
		switch {
		case job.SourceText != "":
			code = "no_recipe_text"
		case !aiOn:
			code = "no_recipe_off"
		}
	case ai.CodeFetchFailed, ai.CodeBlockedAddress, ai.CodeTooLarge, ai.CodeProviderError, ai.CodeRefused, ai.CodeLimit:
	default:
		code = string(ai.CodeInterrupted)
	}
	return &ui.ImportFailure{
		Title:  i18n.T(ctx, i18n.Key("import.failed."+code)),
		Body:   i18n.T(ctx, i18n.Key("import.failed."+code+".body")),
		Detail: job.ErrorDetail,
	}
}

func reviewView(ctx context.Context, job ai.Job) ui.ImportReviewView {
	d := job.Draft
	v := ui.ImportReviewView{ID: job.ID.String(), Name: d.Name, HasPhoto: d.ImageURL != "", Steps: importSteps(ctx, job.Steps),
		NewItems: len(d.NewItems())}
	if job.StartedAt != nil && job.FinishedAt != nil {
		secs := int(job.FinishedAt.Sub(*job.StartedAt).Round(time.Second) / time.Second)
		v.Elapsed = i18n.T(ctx, "import.review.seconds", max(secs, 1))
	}
	var meta []string
	if d.PrepMin > 0 {
		meta = append(meta, i18n.T(ctx, "import.review.prep", d.PrepMin))
	}
	if d.CookMin > 0 {
		meta = append(meta, i18n.T(ctx, "import.review.cook", d.CookMin))
	}
	if d.Servings > 0 {
		meta = append(meta, servingsText(ctx, d.Servings))
	}
	if len(d.Steps) == 1 {
		meta = append(meta, i18n.T(ctx, "import.review.stepsOne"))
	} else {
		meta = append(meta, i18n.T(ctx, "import.review.steps", len(d.Steps)))
	}
	if len(d.Tags) > 0 {
		meta = append(meta, i18n.T(ctx, "import.review.tags", joinNames(ctx, d.Tags)))
	}
	v.Meta = strings.Join(meta, " · ")
	counts := map[ai.Match]int{}
	for i, ing := range d.Ingredients {
		row := ui.ImportRow{Index: i, Text: ing.Text, Item: ing.ItemName, Match: string(ing.Match), Category: ing.Category}
		if ing.ItemID != nil {
			row.ItemID = ing.ItemID.String()
		}
		counts[ing.Match]++
		v.Rows = append(v.Rows, row)
	}
	v.Summary = i18n.T(ctx, "import.review.summary", len(d.Ingredients), counts[ai.MatchExact], counts[ai.MatchCheck], counts[ai.MatchNew])
	return v
}

// ---- Actions ----

// cancelImport stops a running import or discards a draft or failure.
func (s *Server) cancelImport(w http.ResponseWriter, r *http.Request) {
	job, sc, ok := s.importTarget(w, r)
	if !ok {
		return
	}
	err := s.ai.CancelImport(r.Context(), sc, identity(r).UserID, job.ID)
	if err != nil && !errors.Is(err, ai.ErrJobNotFound) {
		s.serverError(w, r, err)
		return
	}
	seeOther(w, r, "/recipes")
}

// saveImport saves a ready draft as a recipe. The photo is downloaded
// first; when that fails the recipe is saved without one and its page
// says so.
func (s *Server) saveImport(w http.ResponseWriter, r *http.Request) {
	job, sc, ok := s.importTarget(w, r)
	if !ok {
		return
	}
	if job.Status != ai.JobReady {
		seeOther(w, r, ui.ImportHref(job.ID.String()))
		return
	}
	ctx := r.Context()
	user := identity(r).UserID
	photo, photoFailed := s.importPhoto(ctx, sc, job)
	recipeID, err := s.ai.SaveImport(ctx, sc, user, job.ID, photo)
	if errors.Is(err, ai.ErrJobNotFound) || errors.Is(err, ai.ErrNotReady) {
		// Saved or discarded in another tab meanwhile.
		seeOther(w, r, "/recipes")
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

// importPhoto downloads a draft's photo through the safe fetcher and
// stores it. failed reports a photo the draft names that could not be
// stored.
func (s *Server) importPhoto(ctx context.Context, sc store.Scoped, job ai.Job) (hash string, failed bool) {
	if job.Draft == nil || job.Draft.ImageURL == "" {
		return "", false
	}
	res, err := s.ai.ImportPhoto(ctx, sc, job.UserID, job.ID)
	if err == nil {
		hash, err = s.images.Save(ctx, sc.HouseholdID(), bytes.NewReader(res.Body))
	}
	if err != nil {
		s.log.InfoContext(ctx, "import photo not saved", "job", job.ID.String(), "err", err)
		return "", true
	}
	return hash, false
}

func (s *Server) setImportedFlash(w http.ResponseWriter, r *http.Request, recipeID uuid.UUID) {
	http.SetCookie(w, &http.Cookie{
		Name: importedCookie, Value: "nophoto", Path: ui.RecipeHref(recipeID.String()), MaxAge: 60,
		HttpOnly: true, Secure: s.secureCookie(r), SameSite: http.SameSiteLaxMode,
	})
}

// takeImportedFlash reports and clears the note that a saved import's
// photo could not be downloaded.
func (s *Server) takeImportedFlash(w http.ResponseWriter, r *http.Request, recipeID uuid.UUID) bool {
	if _, err := r.Cookie(importedCookie); err != nil {
		return false
	}
	http.SetCookie(w, &http.Cookie{
		Name: importedCookie, Path: ui.RecipeHref(recipeID.String()), MaxAge: -1,
		HttpOnly: true, Secure: s.secureCookie(r), SameSite: http.SameSiteLaxMode,
	})
	return true
}

// importPhotoProxy serves a draft's photo through the safe fetcher, so the
// review never loads it from the source site directly.
func (s *Server) importPhotoProxy(w http.ResponseWriter, r *http.Request) {
	job, sc, ok := s.importTarget(w, r)
	if !ok {
		return
	}
	res, err := s.ai.ImportPhoto(r.Context(), sc, identity(r).UserID, job.ID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", res.ContentType)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	_, _ = w.Write(res.Body)
}

// ---- Item picker ----

func (s *Server) importRow(w http.ResponseWriter, r *http.Request, job ai.Job, raw string) (int, bool) {
	row, err := strconv.Atoi(raw)
	if err != nil || job.Draft == nil || row < 0 || row >= len(job.Draft.Ingredients) {
		s.notFound(w, r)
		return 0, false
	}
	return row, true
}

func (s *Server) importPickPage(w http.ResponseWriter, r *http.Request) {
	job, sc, ok := s.importTarget(w, r)
	if !ok {
		return
	}
	if job.Status != ai.JobReady {
		http.Redirect(w, r, ui.ImportHref(job.ID.String()), http.StatusFound)
		return
	}
	row, ok := s.importRow(w, r, job, r.URL.Query().Get("row"))
	if !ok {
		return
	}
	q, set := r.URL.Query()["q"]
	query := ""
	if set {
		query = strings.TrimSpace(q[0])
	}
	s.renderImportPick(w, r, http.StatusOK, sc, job, row, query, set, ui.Form{})
}

func (s *Server) renderImportPick(w http.ResponseWriter, r *http.Request, status int, sc store.Scoped, job ai.Job, row int, query string, searched bool, f ui.Form) {
	ctx := r.Context()
	ing := job.Draft.Ingredients[row]
	search := query
	if !searched {
		search = ing.ItemName
	}
	found, err := s.groceries.SearchItems(ctx, sc, search, 10)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	v := ui.ImportPickView{ID: job.ID.String(), Row: row, Text: ing.Text, Query: search, Form: f}
	if ing.ItemID != nil {
		v.Current = ing.ItemID.String()
	}
	for _, sg := range found {
		it := ui.PickItem{ID: sg.Item.ID.String(), Name: sg.Item.Name}
		if sg.Item.Category != nil {
			it.Category = sg.Item.Category.Name
		}
		v.Items = append(v.Items, it)
	}
	if f.Values == nil {
		name := ing.ItemName
		if searched && query != "" {
			name = query
		}
		v.Form.Values = map[string]string{"name": name}
	}
	p := s.page(r, i18n.T(ctx, "import.pick.title"), ui.NavRecipes)
	p.BackHref, p.BackLabel = ui.ImportHref(job.ID.String())+"#ingredients", i18n.T(ctx, "import.pick.back")
	s.render(w, r, status, ui.ImportPick(p, v))
}

// pickImportItem sets a review row's item: an existing one ("item") or a
// new one ("name").
func (s *Server) pickImportItem(w http.ResponseWriter, r *http.Request) {
	job, sc, ok := s.importTarget(w, r)
	if !ok {
		return
	}
	if job.Status != ai.JobReady {
		seeOther(w, r, ui.ImportHref(job.ID.String()))
		return
	}
	row, ok := s.importRow(w, r, job, r.PostFormValue("row"))
	if !ok {
		return
	}
	ctx := r.Context()
	var pick ai.ItemPick
	if raw := r.PostFormValue("item"); raw != "" {
		itemID, err := uuid.Parse(raw)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
		pick.ItemID = &itemID
	} else {
		pick.Name = r.PostFormValue("name")
	}
	err := s.ai.PickItem(ctx, sc, identity(r).UserID, job.ID, row, pick)
	if f, ok := withErrors(ctx, ui.Form{Values: map[string]string{"name": pick.Name}}, err); ok {
		if f.Error("item") != "" {
			f.Alert = f.Error("item")
		}
		s.renderImportPick(w, r, http.StatusUnprocessableEntity, sc, job, row, pick.Name, pick.Name != "", f)
		return
	}
	if errors.Is(err, ai.ErrNotReady) || errors.Is(err, ai.ErrJobNotFound) {
		seeOther(w, r, ui.ImportHref(job.ID.String()))
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	seeOther(w, r, ui.ImportHref(job.ID.String())+"#ingredients")
}

// ---- Live ----

// importLive follows a running import: the region is re-rendered on each
// step, and the page reloads once the job is finished (or goes to the
// library when it is gone), so the review or failure renders in full.
func (s *Server) importLive(w http.ResponseWriter, r *http.Request, sc store.Scoped, id *auth.Identity) {
	jobID, err := uuid.Parse(r.URL.Query().Get("job"))
	if err != nil {
		s.notFound(w, r)
		return
	}
	s.serveLive(w, r, id, live.Stream{
		Refresh: func(ctx context.Context, sse *datastar.ServerSentEventGenerator) error {
			job, err := s.ai.Job(ctx, sc, id.UserID, jobID)
			if errors.Is(err, ai.ErrJobNotFound) {
				return sse.Redirect("/recipes")
			}
			if err != nil {
				return err
			}
			if !job.Active() {
				return sse.Redirect(ui.ImportHref(jobID.String()))
			}
			return sse.PatchElementTempl(ui.ImportRunningRegion(runningView(ctx, job)))
		},
		Relevant: func(e live.Event) bool { return e.Kind == live.ImportChanged && e.JobID == jobID },
	})
}
