package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/text/language"

	"github.com/raphi011/mise/internal/auth"
	"github.com/raphi011/mise/internal/buildinfo"
	"github.com/raphi011/mise/internal/i18n"
)

// The MCP endpoint (docs/mcp.md) offers the REST API's operations as tools
// to AI agents, over the streamable HTTP transport. It takes the same
// bearer tokens as the API, and OAuth access tokens; read tokens see only
// the read tools. It is stateless: every request gets its own server,
// bound to the request's member, so nothing is kept between requests and
// any replica can answer.

const mcpPath = "/mcp"

// mcpInstructions tell the agent what the server is for.
const mcpInstructions = `mise holds one household's recipes, meal plan and shopping lists.
Ids are UUIDs that tools return; look recipes up with search_recipes before planning or changing them.
Amounts are written as in a shopping list: "400 g spaghetti", "2 tbsp soy sauce", "1 bunch parsley".
Dates are YYYY-MM-DD in the household's time zone; whoami tells today's date.`

// mcpHandler serves /mcp for requests that mcpAuth let through.
func (s *Server) mcpHandler() http.Handler {
	schemas := mcp.NewSchemaCache()
	return mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		id, _ := auth.FromContext(r.Context())
		return s.mcpServer(id, i18n.Locale(r.Context()), schemas)
	}, &mcp.StreamableHTTPOptions{
		Stateless:           true,
		JSONResponse:        true,
		Logger:              s.log,
		MaxRequestBodyBytes: apiMaxBody,
	})
}

// mcpAuth authenticates an MCP request like the API. Without a valid token
// it answers 401 pointing at the protected-resource metadata, which is how
// clients find the authorization server.
func (s *Server) mcpAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowCORS(w)
		w.Header().Set("Cache-Control", "no-store")
		ctx := r.Context()
		challenge := fmt.Sprintf(`Bearer resource_metadata=%q, scope="read write"`, s.issuer(r)+wellKnownResource+mcpPath)
		token, ok := bearerToken(r)
		if !ok {
			w.Header().Set("WWW-Authenticate", challenge)
			apiFail(w, http.StatusUnauthorized, "unauthorized", "Sign in with OAuth or send an API token as \"Authorization: Bearer <token>\".", nil)
			return
		}
		id, err := s.auth.AuthenticateToken(ctx, token)
		if errors.Is(err, auth.ErrNoToken) {
			w.Header().Set("WWW-Authenticate", challenge+`, error="invalid_token"`)
			apiFail(w, http.StatusUnauthorized, "invalid_token", "The token is unknown, expired or revoked.", nil)
			return
		}
		if err != nil {
			s.log.ErrorContext(ctx, "mcp auth", slog.Any("err", err))
			apiFail(w, http.StatusInternalServerError, "internal", "Something went wrong.", nil)
			return
		}
		ctx = auth.WithIdentity(ctx, id)
		ctx = i18n.WithLocale(ctx, i18n.Match(id.Language, id.HouseholdLanguage))
		requestInfo(r).household = id.HouseholdID
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) mcpRoutes(r *router) {
	h := s.mcpAuth(s.mcpHandler())
	r.write(http.MethodPost, mcpPath, h)
	// Stateless servers offer no stream to GET and no session to DELETE;
	// the transport answers 405.
	r.read(mcpPath, h)
	r.write(http.MethodDelete, mcpPath, h)
}

// toolMessage is an error message written for the agent to read.
type toolMessage string

func (m toolMessage) Error() string { return string(m) }

// toolErr turns an operation's error into the message the agent sees:
// validation errors with their fields, not-found, problems as they are.
// Anything else is logged and hidden.
func (s *Server) toolErr(ctx context.Context, tool string, err error) error {
	var p *apiProblem
	var fe fieldErrors
	switch {
	case errors.As(err, &p):
		return toolMessage(problemText(p.message, p.fields))
	case errors.As(err, &fe):
		return toolMessage(problemText(i18n.T(ctx, "form.error.summary"), translateFields(ctx, fe)))
	case isNotFound(err):
		return toolMessage("Not found: no such id in this household.")
	default:
		s.log.ErrorContext(ctx, "mcp tool failed", slog.String("tool", tool), slog.Any("err", err))
		return toolMessage("Something went wrong.")
	}
}

func problemText(message string, fields map[string]string) string {
	if len(fields) == 0 {
		return message
	}
	names := make([]string, 0, len(fields))
	for f := range fields {
		names = append(names, f)
	}
	slices.Sort(names)
	var b strings.Builder
	b.WriteString(message)
	for _, f := range names {
		b.WriteString("\n- " + f + ": " + fields[f])
	}
	return b.String()
}

// mcpID parses an id argument, naming the field when it is no UUID.
func mcpID(field, v string) (uuid.UUID, error) {
	id, err := uuid.Parse(v)
	if err != nil {
		return uuid.Nil, &apiProblem{code: "invalid", message: "Some fields need another look.",
			fields: map[string]string{field: "Not an id; use one a tool returned."}}
	}
	return id, nil
}

// Tool inputs. Descriptions in jsonschema tags reach the agent.

type mcpSearchIn struct {
	Query string `json:"query,omitempty" jsonschema:"words to find in recipe names and ingredients; leave out to list the library"`
	Sort  string `json:"sort,omitempty" jsonschema:"library order when there is no query: cooked (most recently cooked, default), added, most (most cooked), name or quickest"`
	Limit int    `json:"limit,omitempty" jsonschema:"return at most this many recipes, up to 200 (default 50)"`
}

type mcpRecipeIDIn struct {
	ID string `json:"id" jsonschema:"the recipe's id"`
}

type mcpIngredientIn struct {
	ID       string `json:"id,omitempty" jsonschema:"when updating, the id of an existing ingredient to keep it (its note and checks stay)"`
	Text     string `json:"text" jsonschema:"amount and name in one line, e.g. '400 g spaghetti', '1 pinch salt' or '2 cloves garlic'"`
	Optional bool   `json:"optional,omitempty" jsonschema:"leave it off the shopping list when the meal is planned"`
}

type mcpStepIn struct {
	Text         string `json:"text" jsonschema:"what to do"`
	TimerSeconds *int   `json:"timer_seconds,omitempty" jsonschema:"a timer the cook can start for this step"`
}

type mcpRecipeIn struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	PrepMin     *int              `json:"prep_min,omitempty" jsonschema:"preparation time in minutes"`
	CookMin     *int              `json:"cook_min,omitempty" jsonschema:"cooking time in minutes"`
	Servings    int               `json:"servings" jsonschema:"how many people the amounts serve"`
	SourceURL   string            `json:"source_url,omitempty"`
	Notes       string            `json:"notes,omitempty"`
	Tags        []string          `json:"tags,omitempty" jsonschema:"tag names; unknown tags are created"`
	Ingredients []mcpIngredientIn `json:"ingredients,omitempty"`
	Steps       []mcpStepIn       `json:"steps,omitempty"`
	Links       []string          `json:"links,omitempty" jsonschema:"ids of recipes that go well with this one"`
}

func (in mcpRecipeIn) recipeIn() (recipeIn, error) {
	out := recipeIn{
		Name: in.Name, Description: in.Description, PrepMin: in.PrepMin, CookMin: in.CookMin, Servings: in.Servings,
		SourceURL: in.SourceURL, Notes: in.Notes, Tags: in.Tags,
	}
	for i, ing := range in.Ingredients {
		row := ingredientIn{Optional: ing.Optional, line: ing.Text}
		if ing.ID != "" {
			id, err := mcpID(fmt.Sprintf("ingredients[%d].id", i), ing.ID)
			if err != nil {
				return out, err
			}
			row.ID = id
		}
		out.Ingredients = append(out.Ingredients, row)
	}
	for _, st := range in.Steps {
		out.Steps = append(out.Steps, stepIn(st))
	}
	for i, l := range in.Links {
		id, err := mcpID(fmt.Sprintf("links[%d]", i), l)
		if err != nil {
			return out, err
		}
		out.Links = append(out.Links, id)
	}
	return out, nil
}

type mcpUpdateRecipeIn struct {
	ID     string      `json:"id" jsonschema:"the recipe's id"`
	Recipe mcpRecipeIn `json:"recipe" jsonschema:"the whole recipe: tags, ingredients, steps and links left out are removed"`
}

type mcpPlanIn struct {
	Date string `json:"date,omitempty" jsonschema:"any day of the week to show, YYYY-MM-DD (default today)"`
}

type mcpPlanMealIn struct {
	RecipeID  string `json:"recipe_id"`
	Date      string `json:"date" jsonschema:"YYYY-MM-DD"`
	Servings  int    `json:"servings,omitempty" jsonschema:"default: the recipe's servings"`
	AddToList bool   `json:"add_to_list,omitempty" jsonschema:"also put the recipe's required ingredients on the default shopping list, scaled to the servings"`
}

type mcpMealIDIn struct {
	MealID string `json:"meal_id" jsonschema:"the planned meal's id (not the recipe's)"`
}

type mcpListIn struct {
	ListID string `json:"list_id,omitempty" jsonschema:"the list's id (default: the household's first list)"`
}

type mcpAddEntryIn struct {
	ListID string `json:"list_id,omitempty" jsonschema:"the list's id (default: the household's first list)"`
	Text   string `json:"text" jsonschema:"amount and item in one line, e.g. '2 l milk' or 'basil'; amounts merge into an open entry of the same item"`
}

type mcpEntryIDIn struct {
	EntryID string `json:"entry_id" jsonschema:"the list entry's id"`
}

type mcpNoIn struct{}

// mcpDone answers tools that return nothing else.
type mcpDone struct {
	OK bool `json:"ok"`
}

// whoamiJSON is doMe with today's date, which agents need for planning.
type whoamiJSON struct {
	meJSON
	Today string `json:"today"`
}

func boolPtr(b bool) *bool { return &b }

var (
	readOnly    = &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: boolPtr(false)}
	additive    = &mcp.ToolAnnotations{DestructiveHint: boolPtr(false), OpenWorldHint: boolPtr(false)}
	idempotent  = &mcp.ToolAnnotations{DestructiveHint: boolPtr(false), IdempotentHint: true, OpenWorldHint: boolPtr(false)}
	destructive = &mcp.ToolAnnotations{DestructiveHint: boolPtr(true), IdempotentHint: true, OpenWorldHint: boolPtr(false)}
)

// addTool registers a tool whose handler runs as id, in lang.
func addTool[In, Out any](s *Server, srv *mcp.Server, id *auth.Identity, lang language.Tag, t *mcp.Tool, do func(context.Context, In) (Out, error)) {
	mcp.AddTool(srv, t, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		ctx = i18n.WithLocale(auth.WithIdentity(ctx, id), lang)
		out, err := do(ctx, in)
		if err != nil {
			var zero Out
			return nil, zero, s.toolErr(ctx, t.Name, err)
		}
		return nil, out, nil
	})
}

// mcpServer builds the tools for one request's member.
func (s *Server) mcpServer(id *auth.Identity, lang language.Tag, schemas *mcp.SchemaCache) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "mise", Title: "mise", Version: buildinfo.Version()},
		&mcp.ServerOptions{Instructions: mcpInstructions, SchemaCache: schemas})

	addTool(s, srv, id, lang, &mcp.Tool{Name: "whoami", Title: "Who am I",
		Description: "The member and household the tools act for, the token's scope (read or write), and today's date.",
		Annotations: readOnly},
		func(ctx context.Context, _ mcpNoIn) (whoamiJSON, error) {
			out := whoamiJSON{meJSON: doMe(ctx)}
			sc, err := s.db.Scope(ctx)
			if err != nil {
				return out, err
			}
			today, err := s.plan.Today(ctx, sc)
			out.Today = today.String()
			return out, err
		})
	addTool(s, srv, id, lang, &mcp.Tool{Name: "search_recipes", Title: "Search recipes",
		Description: "Find recipes by name or ingredient, or list the library. Returns cards with ids; use get_recipe for ingredients and steps.",
		Annotations: readOnly},
		func(ctx context.Context, in mcpSearchIn) (recipeListJSON, error) {
			return s.doListRecipes(ctx, in.Query, in.Sort, min(max(in.Limit, 0), 200))
		})
	addTool(s, srv, id, lang, &mcp.Tool{Name: "get_recipe", Title: "Get recipe",
		Description: "One recipe with its ingredients, steps, tags and linked recipes.", Annotations: readOnly},
		func(ctx context.Context, in mcpRecipeIDIn) (recipeJSON, error) {
			recipeID, err := mcpID("id", in.ID)
			if err != nil {
				return recipeJSON{}, err
			}
			return s.doGetRecipe(ctx, recipeID)
		})
	addTool(s, srv, id, lang, &mcp.Tool{Name: "get_plan", Title: "Get meal plan",
		Description: "The meal plan for the week containing a date: each day's planned meals with their ids and servings.",
		Annotations: readOnly},
		func(ctx context.Context, in mcpPlanIn) (weekJSON, error) { return s.doGetPlan(ctx, in.Date) })
	addTool(s, srv, id, lang, &mcp.Tool{Name: "list_shopping_lists", Title: "List shopping lists",
		Description: "The household's shopping lists with how many entries are still to get. The first is the default list.",
		Annotations: readOnly},
		func(ctx context.Context, _ mcpNoIn) (listsJSON, error) { return s.doLists(ctx) })
	addTool(s, srv, id, lang, &mcp.Tool{Name: "get_shopping_list", Title: "Get shopping list",
		Description: "A list's entries: those still to get in aisle order, then those in the cart (checked).",
		Annotations: readOnly},
		func(ctx context.Context, in mcpListIn) (listViewJSON, error) {
			listID, err := s.mcpListID(ctx, in.ListID)
			if err != nil {
				return listViewJSON{}, err
			}
			return s.doList(ctx, listID)
		})

	if !id.APIScope.Can(true) {
		return srv
	}

	addTool(s, srv, id, lang, &mcp.Tool{Name: "create_recipe", Title: "Create recipe",
		Description: "Add a recipe to the library.", Annotations: additive},
		func(ctx context.Context, in mcpRecipeIn) (recipeJSON, error) {
			r, err := in.recipeIn()
			if err != nil {
				return recipeJSON{}, err
			}
			return s.doCreateRecipe(ctx, r)
		})
	addTool(s, srv, id, lang, &mcp.Tool{Name: "update_recipe", Title: "Update recipe",
		Description: "Replace a recipe. Send the whole recipe: tags, ingredients, steps and links left out are removed. " +
			"Send ingredient ids from get_recipe to keep existing ingredients.",
		Annotations: idempotent},
		func(ctx context.Context, in mcpUpdateRecipeIn) (recipeJSON, error) {
			recipeID, err := mcpID("id", in.ID)
			if err != nil {
				return recipeJSON{}, err
			}
			r, err := in.Recipe.recipeIn()
			if err != nil {
				return recipeJSON{}, err
			}
			return s.doUpdateRecipe(ctx, recipeID, r)
		})
	addTool(s, srv, id, lang, &mcp.Tool{Name: "delete_recipe", Title: "Delete recipe",
		Description: "Delete a recipe and take it off the plan. Members can restore it in the app for a short while.",
		Annotations: destructive},
		func(ctx context.Context, in mcpRecipeIDIn) (mcpDone, error) {
			recipeID, err := mcpID("id", in.ID)
			if err != nil {
				return mcpDone{}, err
			}
			return mcpDone{OK: true}, s.doDeleteRecipe(ctx, recipeID)
		})
	addTool(s, srv, id, lang, &mcp.Tool{Name: "plan_meal", Title: "Plan meal",
		Description: "Plan a recipe for a day, optionally putting its ingredients on the default shopping list.",
		Annotations: additive},
		func(ctx context.Context, in mcpPlanMealIn) (mealJSON, error) {
			recipeID, err := mcpID("recipe_id", in.RecipeID)
			if err != nil {
				return mealJSON{}, err
			}
			return s.doAddMeal(ctx, addMealIn{RecipeID: recipeID, Date: in.Date, Servings: in.Servings, AddToList: in.AddToList})
		})
	addTool(s, srv, id, lang, &mcp.Tool{Name: "remove_meal", Title: "Remove meal",
		Description: "Take a planned meal off the plan.", Annotations: destructive},
		func(ctx context.Context, in mcpMealIDIn) (mcpDone, error) {
			mealID, err := mcpID("meal_id", in.MealID)
			if err != nil {
				return mcpDone{}, err
			}
			return mcpDone{OK: true}, s.doRemoveMeal(ctx, mealID)
		})
	addTool(s, srv, id, lang, &mcp.Tool{Name: "add_to_shopping_list", Title: "Add to shopping list",
		Description: "Put an item on a shopping list. An item already on the list gets the amount added when the units match.",
		Annotations: additive},
		func(ctx context.Context, in mcpAddEntryIn) (entryJSON, error) {
			listID, err := s.mcpListID(ctx, in.ListID)
			if err != nil {
				return entryJSON{}, err
			}
			return s.doAddEntry(ctx, listID, addEntryIn{Text: in.Text})
		})
	addTool(s, srv, id, lang, &mcp.Tool{Name: "check_entry", Title: "Check entry",
		Description: "Move a list entry into the cart (got it).", Annotations: idempotent},
		func(ctx context.Context, in mcpEntryIDIn) (entryJSON, error) {
			entryID, err := mcpID("entry_id", in.EntryID)
			if err != nil {
				return entryJSON{}, err
			}
			return s.doEntryAction(ctx, entryID, checkEntry)
		})
	addTool(s, srv, id, lang, &mcp.Tool{Name: "uncheck_entry", Title: "Uncheck entry",
		Description: "Move a list entry out of the cart, back to the items still to get.", Annotations: idempotent},
		func(ctx context.Context, in mcpEntryIDIn) (entryJSON, error) {
			entryID, err := mcpID("entry_id", in.EntryID)
			if err != nil {
				return entryJSON{}, err
			}
			return s.doEntryAction(ctx, entryID, uncheckEntry)
		})
	addTool(s, srv, id, lang, &mcp.Tool{Name: "remove_entry", Title: "Remove entry",
		Description: "Take an entry off its shopping list.", Annotations: destructive},
		func(ctx context.Context, in mcpEntryIDIn) (mcpDone, error) {
			entryID, err := mcpID("entry_id", in.EntryID)
			if err != nil {
				return mcpDone{}, err
			}
			return mcpDone{OK: true}, s.doRemoveEntry(ctx, entryID)
		})
	return srv
}

// mcpListID parses a list id argument, or picks the default list.
func (s *Server) mcpListID(ctx context.Context, v string) (uuid.UUID, error) {
	if v != "" {
		return mcpID("list_id", v)
	}
	sc, err := s.db.Scope(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	l, err := s.groceries.FirstList(ctx, sc)
	return l.ID, err
}
