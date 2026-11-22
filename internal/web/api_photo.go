package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/ai"
	"github.com/raphi011/mise/internal/i18n"
	"github.com/raphi011/mise/internal/images"
)

// Recipe photos over the API and MCP go through the editor's image store
// (images.Save), so uploads are checked, resized and stripped the same way.

// photoURL is the API path of a recipe's photo, or nil without one.
func photoURL(recipeID uuid.UUID, photo string) *string {
	if photo == "" {
		return nil
	}
	u := "/api/v1/recipes/" + recipeID.String() + "/photo"
	return &u
}

// photoProblem reports a rejected image on field with the editor's
// message; a file over the limit is 413. Other errors pass through.
func photoProblem(ctx context.Context, field string, err error) error {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		err = images.ErrTooLarge
	}
	key, ok := photoErrorKey(err)
	if !ok {
		return err
	}
	p := &apiProblem{status: http.StatusUnprocessableEntity, code: "invalid", message: i18n.T(ctx, "form.error.summary"),
		fields: map[string]string{field: i18n.T(ctx, key)}}
	if errors.Is(err, images.ErrTooLarge) {
		p.status, p.code = http.StatusRequestEntityTooLarge, "too_large"
	}
	return p
}

// fieldProblem is a validation problem on one field.
func fieldProblem(ctx context.Context, key i18n.Key, fields ...string) error {
	p := &apiProblem{status: http.StatusUnprocessableEntity, code: "invalid", message: i18n.T(ctx, "form.error.summary"),
		fields: map[string]string{}}
	for _, f := range fields {
		p.fields[f] = i18n.T(ctx, key)
	}
	return p
}

// doSetRecipePhoto stores the image open returns and makes it the recipe's
// photo. open runs only once the recipe is known to exist, so a wrong id
// costs no download or decode; field names the image in problems.
func (s *Server) doSetRecipePhoto(ctx context.Context, recipeID uuid.UUID, field string, open func(context.Context) (io.Reader, error)) (recipeJSON, error) {
	sc, err := s.db.Scope(ctx)
	if err != nil {
		return recipeJSON{}, err
	}
	me := ctxIdentity(ctx).UserID
	if _, err := s.recipes.Get(ctx, sc, recipeID, me); err != nil {
		return recipeJSON{}, err
	}
	img, err := open(ctx)
	if err != nil {
		return recipeJSON{}, err
	}
	hash, err := s.images.Save(ctx, sc.HouseholdID(), img)
	if err != nil {
		return recipeJSON{}, photoProblem(ctx, field, err)
	}
	if err := s.recipes.SetPhoto(ctx, sc, recipeID, me, hash); err != nil {
		return recipeJSON{}, err
	}
	return s.doGetRecipe(ctx, recipeID)
}

func (s *Server) doRemoveRecipePhoto(ctx context.Context, recipeID uuid.UUID) error {
	sc, err := s.db.Scope(ctx)
	if err != nil {
		return err
	}
	return s.recipes.SetPhoto(ctx, sc, recipeID, ctxIdentity(ctx).UserID, "")
}

// fetchPhoto downloads an image an agent names by URL: https only, from
// public addresses only (checked at every dial, so redirects and DNS
// rebinding can't reach the local network), within the upload limit and
// the fetcher's timeout, and only JPEG, PNG or WebP.
func (s *Server) fetchPhoto(ctx context.Context, field, rawURL string) (io.Reader, error) {
	res, err := s.fetcher.FetchHTTPS(ctx, rawURL, ai.KindImage)
	if err == nil {
		return bytes.NewReader(res.Body), nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	s.log.InfoContext(ctx, "recipe photo not fetched", "err", err)
	var key i18n.Key
	switch {
	case errors.Is(err, ai.ErrBadURL), errors.Is(err, ai.ErrNotHTTPS):
		key = "recipes.photo.url"
	case errors.Is(err, ai.ErrBlockedAddress):
		key = "recipes.photo.blocked"
	case errors.Is(err, ai.ErrTooLarge):
		return nil, photoProblem(ctx, field, images.ErrTooLarge)
	case errors.Is(err, ai.ErrContentType):
		key = "recipes.photo.unsupported"
	default:
		key = "recipes.photo.fetchFailed"
	}
	return nil, fieldProblem(ctx, key, field)
}

// decodeImageData reads base64 image data, bare or as a data: URL.
func decodeImageData(v string) ([]byte, bool) {
	if rest, ok := strings.CutPrefix(v, "data:"); ok {
		meta, data, ok := strings.Cut(rest, ",")
		if !ok || !strings.HasSuffix(meta, ";base64") {
			return nil, false
		}
		v = data
	}
	v = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, v)
	b, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		b, err = base64.RawStdEncoding.DecodeString(v)
	}
	return b, err == nil
}

// apiSetRecipePhoto takes the image as the raw request body, up to the
// editor's upload limit.
func (s *Server) apiSetRecipePhoto(w http.ResponseWriter, r *http.Request) {
	recipeID, err := parseID(r.PathValue("id"))
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	body := http.MaxBytesReader(w, r.Body, images.MaxUploadBytes+1)
	out, err := s.doSetRecipePhoto(r.Context(), recipeID, "photo", func(context.Context) (io.Reader, error) { return body, nil })
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) apiRemoveRecipePhoto(w http.ResponseWriter, r *http.Request) {
	recipeID, err := parseID(r.PathValue("id"))
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	if err := s.doRemoveRecipePhoto(r.Context(), recipeID); err != nil {
		s.apiErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// apiGetRecipePhoto serves a recipe's photo as JPEG: the full size, or
// ?size=thumb. Clients revalidate by ETag, which changes with the photo.
func (s *Server) apiGetRecipePhoto(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	recipeID, err := parseID(r.PathValue("id"))
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	size := images.Size(r.URL.Query().Get("size"))
	if size == "" {
		size = images.Full
	}
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	rec, err := s.recipes.Get(ctx, sc, recipeID, ctxIdentity(ctx).UserID)
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	if rec.Photo == "" {
		apiNotFound(w, r)
		return
	}
	f, err := s.images.Open(sc.HouseholdID(), rec.Photo, size)
	if errors.Is(err, images.ErrNotFound) {
		apiNotFound(w, r)
		return
	}
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	defer func() { _ = f.Close() }()
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("ETag", `"`+rec.Photo+"-"+string(size)+`"`)
	http.ServeContent(w, r, "", time.Time{}, f)
}
