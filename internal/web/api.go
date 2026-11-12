package web

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/raphi011/mise/internal/auth"
	"github.com/raphi011/mise/internal/groceries"
	"github.com/raphi011/mise/internal/i18n"
	"github.com/raphi011/mise/internal/plan"
	"github.com/raphi011/mise/internal/recipes"
)

// The REST API under /api/v1 is for scripts and agents. It takes bearer
// tokens only: it never reads the session cookie, so a browser can't be
// made to call it (CSRF has nothing to ride on), and a token can't reach
// the HTML pages. Bodies and answers are JSON; errors look like
//
//	{"error": {"code": "invalid", "message": "...", "fields": {"name": "..."}}}

const (
	apiPrefix = "/api/"
	// apiMaxBody bounds a request body.
	apiMaxBody = 1 << 20
)

// isAPI reports whether path belongs to the REST API.
func isAPI(path string) bool { return strings.HasPrefix(path, apiPrefix) }

// apiError is the body of every API error.
type apiError struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// apiFail answers with an error body.
func apiFail(w http.ResponseWriter, status int, code, message string, fields map[string]string) {
	writeJSON(w, status, map[string]apiError{"error": {Code: code, Message: message, Fields: fields}})
}

// apiNotFound answers for an unknown route or a resource that isn't there.
func apiNotFound(w http.ResponseWriter, _ *http.Request) {
	apiFail(w, http.StatusNotFound, "not_found", "Not found.", nil)
}

// apiErr answers for err from a domain call: validation errors are 422,
// missing resources 404, anything else is logged and 500.
func (s *Server) apiErr(w http.ResponseWriter, r *http.Request, err error) {
	ctx := r.Context()
	var fe fieldErrors
	switch {
	case errors.As(err, &fe):
		fields := map[string]string{}
		for field, key := range fe.Fields() {
			fields[field] = i18n.T(ctx, key)
		}
		apiFail(w, http.StatusUnprocessableEntity, "invalid", i18n.T(ctx, "form.error.summary"), fields)
	case errors.Is(err, recipes.ErrNotFound), errors.Is(err, plan.ErrNotFound), errors.Is(err, groceries.ErrNotFound):
		apiNotFound(w, r)
	default:
		s.log.ErrorContext(ctx, "api request failed", slog.String("route", r.Pattern), slog.Any("err", err))
		apiFail(w, http.StatusInternalServerError, "internal", "Something went wrong.", nil)
	}
}

// decodeJSON reads the request body into dst, refusing unknown fields,
// trailing data and bodies over apiMaxBody. It answers and returns false on
// failure.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, apiMaxBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	err := dec.Decode(dst)
	if err == nil {
		if _, err2 := dec.Token(); !errors.Is(err2, io.EOF) {
			err = errors.New("unexpected data after the JSON value")
		}
	}
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			apiFail(w, http.StatusRequestEntityTooLarge, "too_large", "The request body is too large.", nil)
			return false
		}
		apiFail(w, http.StatusBadRequest, "invalid_json", "The request body is not valid JSON of the expected shape: "+err.Error(), nil)
		return false
	}
	return true
}

// bearerToken returns the token of an "Authorization: Bearer" header.
func bearerToken(r *http.Request) (string, bool) {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
		return "", false
	}
	return strings.TrimSpace(token), true
}

// apiRead and apiWrite wrap API handlers: they authenticate the bearer
// token and, for apiWrite, require the write scope.
func (s *Server) apiRead(h http.HandlerFunc) http.Handler  { return s.apiAuth(false, h) }
func (s *Server) apiWrite(h http.HandlerFunc) http.Handler { return s.apiAuth(true, h) }

func (s *Server) apiAuth(write bool, h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		ctx := r.Context()
		token, ok := bearerToken(r)
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="mise"`)
			apiFail(w, http.StatusUnauthorized, "unauthorized", "Send an API token as \"Authorization: Bearer <token>\".", nil)
			return
		}
		id, err := s.auth.AuthenticateToken(ctx, token)
		if errors.Is(err, auth.ErrNoToken) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="mise", error="invalid_token"`)
			apiFail(w, http.StatusUnauthorized, "invalid_token", "The API token is unknown, expired or revoked.", nil)
			return
		}
		if err != nil {
			s.log.ErrorContext(ctx, "api auth", slog.Any("err", err))
			apiFail(w, http.StatusInternalServerError, "internal", "Something went wrong.", nil)
			return
		}
		if !id.APIScope.Can(write) {
			apiFail(w, http.StatusForbidden, "insufficient_scope", "This token can only read; create a token with write access.", nil)
			return
		}
		ctx = auth.WithIdentity(ctx, id)
		ctx = i18n.WithLocale(ctx, i18n.Match(id.Language, id.HouseholdLanguage))
		h(w, r.WithContext(ctx))
	})
}

type meJSON struct {
	User struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		Name     string `json:"name"`
	} `json:"user"`
	Household struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Timezone  string `json:"timezone"`
		WeekStart string `json:"week_start"`
		Language  string `json:"language"`
	} `json:"household"`
	// Scope is the token's: read or write.
	Scope string `json:"scope"`
}

func (s *Server) apiMe(w http.ResponseWriter, r *http.Request) {
	id := identity(r)
	var v meJSON
	v.User.ID, v.User.Username, v.User.Name = id.UserID.String(), id.Username, id.Name
	v.Household.ID, v.Household.Name, v.Household.Timezone = id.HouseholdID.String(), id.HouseholdName, id.Timezone
	v.Household.WeekStart, v.Household.Language = id.WeekStart, id.HouseholdLanguage
	v.Scope = string(id.APIScope)
	writeJSON(w, http.StatusOK, v)
}

// apiRoutes registers /api/v1.
func (s *Server) apiRoutes(r *router) {
	r.read("/api/v1/me", s.apiRead(s.apiMe))

	r.read("/api/v1/recipes", s.apiRead(s.apiListRecipes))
	r.read("/api/v1/recipes/{id}", s.apiRead(s.apiGetRecipe))
	r.write(http.MethodPost, "/api/v1/recipes", s.apiWrite(s.apiCreateRecipe))
	r.write(http.MethodPut, "/api/v1/recipes/{id}", s.apiWrite(s.apiUpdateRecipe))
	r.write(http.MethodDelete, "/api/v1/recipes/{id}", s.apiWrite(s.apiDeleteRecipe))

	r.read("/api/v1/plan", s.apiRead(s.apiGetPlan))
	r.write(http.MethodPost, "/api/v1/plan/meals", s.apiWrite(s.apiAddMeal))
	r.write(http.MethodDelete, "/api/v1/plan/meals/{id}", s.apiWrite(s.apiRemoveMeal))

	r.read("/api/v1/lists", s.apiRead(s.apiLists))
	r.read("/api/v1/lists/{id}", s.apiRead(s.apiList))
	r.write(http.MethodPost, "/api/v1/lists/{id}/entries", s.apiWrite(s.apiAddEntry))
	r.write(http.MethodPost, "/api/v1/entries/{id}/check", s.apiWrite(s.apiCheckEntry))
	r.write(http.MethodPost, "/api/v1/entries/{id}/uncheck", s.apiWrite(s.apiUncheckEntry))
	r.write(http.MethodDelete, "/api/v1/entries/{id}", s.apiWrite(s.apiRemoveEntry))

	// Every other path under /api/ answers in JSON too.
	r.prefix(apiPrefix, http.HandlerFunc(apiNotFound))
}
