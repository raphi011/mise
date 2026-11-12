# REST API

For scripts and AI agents. Base path `/api/v1`, JSON in and out, one household per token.

## Tokens

Create one under **Settings › Account & security › API tokens**: a name, an access level and an optional expiry. The token (`mise_...`) is shown once; mise stores only its SHA-256.

- **Read** tokens call `GET` endpoints; **Read and write** tokens also change things (`insufficient_scope`, 403, otherwise).
- A token acts as its member in the household it was created in. Removing the member, resetting their password, or revoking the token ends it. A normal password change does not.
- Send it as `Authorization: Bearer <token>`. The API ignores the session cookie, and tokens don't open the web pages or create other tokens.
- At most 20 tokens per person.

```sh
curl -H "Authorization: Bearer $MISE_TOKEN" https://mise.example.com/api/v1/me
```

## Errors

Every error is `{"error": {"code", "message", "fields"?}}`:

| Status | `code` | |
|--------|--------|---|
| 400 | `invalid_json`, `invalid_date` | Body not parseable, unknown field, bad query |
| 401 | `unauthorized`, `invalid_token` | Missing, unknown, expired or revoked token |
| 403 | `insufficient_scope` | Read token on a write endpoint |
| 404 | `not_found` | Unknown path or id (also ids of other households) |
| 413 | `too_large` | Body over 1 MiB |
| 422 | `invalid` | Validation; `fields` maps field name to message |
| 503 | `setup_required` | mise is not set up yet |

Messages follow the token owner's language.

## Endpoints

| | |
|---|---|
| `GET /me` | The token's user, household and scope |
| `GET /recipes?q=&sort=&limit=` | Library cards (`sort`: `cooked`, `added`, `most`, `name`, `quickest`; `limit` default 50, max 200) or, with `q`, recipes whose name or an ingredient matches. Returns `{recipes, total}` |
| `GET /recipes/{id}` | One recipe with ingredients, steps, tags, links |
| `POST /recipes` | Create (201 with the recipe) |
| `PUT /recipes/{id}` | Replace: tags, ingredients, steps and links not sent are removed |
| `DELETE /recipes/{id}` | Delete (204) |
| `GET /plan?date=YYYY-MM-DD` | The week containing the date (default today) |
| `POST /plan/meals` | `{recipe_id, date, servings?, add_to_list?}`; `add_to_list` puts the required ingredients on the default list, scaled |
| `DELETE /plan/meals/{id}` | Take a meal off the plan (204) |
| `GET /lists` | Shopping lists with their open counts |
| `GET /lists/{id}` | Entries: still to get in aisle order, then the cart (`checked`) |
| `POST /lists/{id}/entries` | `{text: "2 l milk"}` or `{name, amount}`; amounts merge into an open entry of the same unit |
| `POST /entries/{id}/check`, `/uncheck` | Move an entry into or out of the cart |
| `DELETE /entries/{id}` | Remove an entry (204) |

### Writing a recipe

```json
{
  "name": "Pasta aglio e olio",
  "servings": 4,
  "prep_min": 5,
  "cook_min": 15,
  "tags": ["quick"],
  "ingredients": [
    "400 g spaghetti",
    {"name": "salt", "amount": "1 pinch", "optional": true}
  ],
  "steps": [
    "Boil the water.",
    {"text": "Cook the pasta.", "timer_seconds": 600}
  ],
  "links": ["<id of a side dish>"]
}
```

Ingredients and steps are either one line (amounts are read like the shopping list's add field, in the owner's language) or an object. An ingredient's `amount` may carry a note after the amount (`"600 g, gemischt"`, `"1 TL, rot"`); it is stored separately and returned joined again. To keep an existing ingredient across an update, send its `id` back. Photos are not part of the API yet.

Amounts come back as one string ("400 g", "½ bunch"), formatted for the owner's language. Dates are `YYYY-MM-DD`; times are RFC 3339.
