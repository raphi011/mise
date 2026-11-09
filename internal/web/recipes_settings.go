package web

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/i18n"
	"github.com/raphi011/mise/internal/recipes"
	"github.com/raphi011/mise/internal/ui"
)

// ---- Recipe tags (Settings › Recipe tags) ----

// tagNotices are the status messages after a tag change.
var tagNotices = map[string]i18n.Key{
	"saved":   "notice.saved",
	"created": "notice.tagCreated",
	"merged":  "notice.tagMerged",
	"deleted": "notice.tagDeleted",
}

type tagsState struct {
	create ui.Form
	editID string
	edit   ui.Form
}

func (s *Server) tagsPage(w http.ResponseWriter, r *http.Request) {
	s.renderTags(w, r, http.StatusOK, tagsState{})
}

func (s *Server) renderTags(w http.ResponseWriter, r *http.Request, status int, st tagsState) {
	ctx := r.Context()
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	tags, err := s.recipes.Tags(ctx, sc)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	v := ui.TagsView{New: st.create, EditID: st.editID, Edit: st.edit}
	if key, ok := tagNotices[r.URL.Query().Get("done")]; ok && status == http.StatusOK {
		v.Notice = i18n.T(ctx, key)
	}
	for _, t := range tags {
		v.Tags = append(v.Tags, ui.TagRow{ID: t.ID.String(), Name: t.Name, Pinned: t.Pinned, Count: t.Count})
	}
	p := s.page(r, i18n.T(ctx, "tags.title"), ui.NavSettings)
	p.BackHref, p.BackLabel = "/settings#recipes", i18n.T(ctx, "settings.back")
	s.render(w, r, status, ui.Tags(p, v))
}

// tagsError answers err from a tag action and reports whether it did; nil
// errors are left to the caller.
func (s *Server) tagsError(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, recipes.ErrNotFound):
		s.notFound(w, r)
	default:
		s.serverError(w, r, err)
	}
	return true
}

func (s *Server) createTag(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	f := formOf(r, "name")
	_, err = s.recipes.CreateTag(ctx, sc, f.Value("name"))
	if f, ok := withErrors(ctx, f, err); ok {
		s.renderTags(w, r, http.StatusUnprocessableEntity, tagsState{create: f})
		return
	}
	if !s.tagsError(w, r, err) {
		seeOther(w, r, "/settings/tags?done=created")
	}
}

func (s *Server) renameTag(w http.ResponseWriter, r *http.Request) {
	id, sc, ok := s.groceriesTarget(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	f := formOf(r, "name")
	err := s.recipes.RenameTag(ctx, sc, id, f.Value("name"))
	if f, ok := withErrors(ctx, f, err); ok {
		s.renderTags(w, r, http.StatusUnprocessableEntity, tagsState{editID: id.String(), edit: f})
		return
	}
	if !s.tagsError(w, r, err) {
		seeOther(w, r, "/settings/tags?done=saved")
	}
}

func (s *Server) pinTag(w http.ResponseWriter, r *http.Request) {
	id, sc, ok := s.groceriesTarget(w, r)
	if !ok {
		return
	}
	pinned, err := strconv.ParseBool(r.PostFormValue("pinned"))
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	if !s.tagsError(w, r, s.recipes.SetTagPinned(r.Context(), sc, id, pinned)) {
		seeOther(w, r, "/settings/tags?done=saved")
	}
}

func (s *Server) mergeTag(w http.ResponseWriter, r *http.Request) {
	id, sc, ok := s.groceriesTarget(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	into, err := uuid.Parse(r.PostFormValue("into"))
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	err = s.recipes.MergeTag(ctx, sc, id, into)
	if _, ok := withErrors(ctx, ui.Form{}, err); ok {
		// Only a tag merged into itself; the form never offers that.
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	if !s.tagsError(w, r, err) {
		seeOther(w, r, "/settings/tags?done=merged")
	}
}

func (s *Server) deleteTag(w http.ResponseWriter, r *http.Request) {
	id, sc, ok := s.groceriesTarget(w, r)
	if !ok {
		return
	}
	if !s.tagsError(w, r, s.recipes.DeleteTag(r.Context(), sc, id)) {
		seeOther(w, r, "/settings/tags?done=deleted")
	}
}
