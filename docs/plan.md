# mise — implementation plan

Self-hosted recipe, meal-plan and shopping-list app. One Go binary serves HTML, assets and images.

- Features: [Mise — feature summary](https://claude.ai/artifact/7WTaQihNaPB4QYxp2C8b2p)
- Design: [Mise — App Design](https://claude.ai/artifact/PryEj2Ldyo5h2VB1418yTG) (phone, tablet, web, dark, style guide)

## Goals and non-goals

- Single binary `mise-server`, SQLite by default, Postgres optional.
- Multi-tenant data model from day one; the UI exposes exactly one household.
- Live sharing between devices (two people shopping from one list).
- Installable on phones (manifest), full-screen launch.
- English and German from the start of the UI work (catalog in P0; since P2 a test keeps German complete).
- **Not a goal:** offline editing. The app needs a connection. A service worker exists only to make the app installable.

The binary is named `mise-server`, not `mise`, so `go install` does not shadow the jdx/mise CLI in `~/go/bin`. The product name stays "mise".

## Stack

| Concern | Choice |
|---|---|
| Language | Go 1.27 (`go 1.27` in go.mod), `import _ "time/tzdata"` |
| HTTP | stdlib `net/http` ServeMux (method + pattern routing), `http.CrossOriginProtection` for CSRF |
| HTML | [templ](https://templ.guide) components, rendered server-side |
| Interactivity | [Datastar](https://data-star.dev) v1.x: SSE-driven element patches + client signals. Go SDK `github.com/starfederation/datastar-go/datastar`. JS vendored into the embed, version pinned, no Pro attributes |
| Styling | Hand-written CSS with design tokens (custom properties) from the style guide; light + dark |
| SQLite | `modernc.org/sqlite` (pure Go, no CGO) |
| Postgres | `github.com/jackc/pgx/v5` via `database/sql` (`pgx/v5/stdlib`) |
| Query layer | Hand-written portable SQL, placeholder rebind per dialect, `scany/v2/sqlscan` for both drivers |
| Migrations | goose, embedded, one directory per dialect, schema-parity test in CI |
| Passwords | argon2id (`golang.org/x/crypto/argon2`) |
| IDs | UUIDv7 (`github.com/google/uuid`) |
| Text | `golang.org/x/text/cases` (case folding for name keys), `golang.org/x/text/message` (i18n) |
| Images | stdlib `image/*` + `golang.org/x/image/draw`, EXIF orientation reader |
| AI | `anthropic-sdk-go/v2`, `openai-go` (OpenAI-compatible endpoints incl. Ollama) |
| Logging | `log/slog` |
| Lint | golangci-lint v2 |
| Dev tooling | `mise.toml` in repo pins go and golangci-lint; templ via `go tool` (go.mod `tool` directive). Migrations are embedded, so no goose CLI |

### Why templ + Datastar

Offline support is out of scope, so a server-rendered UI covers every interaction in the design without a JS build step or a JSON API mirrored in TypeScript:

- Live sharing: each open page holds one SSE stream; any write re-renders affected fragments and pushes them to every viewer in the household.
- Local-only interactions (servings scaling, cook-mode timers, toasts, sheets) use Datastar signals, no round trip.
- Drag and drop: native HTML5 drag events post the move; every drag has a tap/click alternative (meal menu), as the design requires.

Fallback if Datastar causes trouble: templ + htmx (SSE extension) + Alpine. Handlers and templ components stay the same; only attributes change.

## Configuration

Env vars (flags mirror them):

| Var | Default | Meaning |
|---|---|---|
| `MISE_DB` | `sqlite://$MISE_DATA_DIR/mise.db` | `sqlite://path` or `postgres://…` |
| `MISE_DATA_DIR` | `./data` | images, SQLite file, generated secret key |
| `MISE_LISTEN` | `:8080` | listen address |
| `MISE_BASE_URL` | required after setup | public URL for invite/reset links; also passed to `CrossOriginProtection.AddTrustedOrigin` for proxies that rewrite `Host`. If unset at first run, the setup page proposes the request's origin and stores it |
| `MISE_EXTRA_BASE_URLS` | unset | further public URLs (e.g. a tailnet name), matched by `Host`: trusted origins, and SSO returns to the one the flow started on. Email and share links use `MISE_BASE_URL` |
| `MISE_TRUSTED_PROXIES` | unset | CIDRs whose `X-Forwarded-For` is trusted for client IP (rate limiting, logs) |
| `MISE_SECRET_KEY` | generated into `$MISE_DATA_DIR/secret.key` | AES-256-GCM key for stored API keys (base64, 32 bytes) |
| `MISE_SMTP_HOST` | unset | optional; enables invite and password-reset emails. Without it invites are link-only and admins reset passwords |
| `MISE_SMTP_PORT` | `587` | |
| `MISE_SMTP_TLS` | `starttls` | `starttls`, `tls` (implicit, usually port 465) or `none` |
| `MISE_SMTP_USERNAME`, `MISE_SMTP_PASSWORD` | unset | auth, when the server needs it |
| `MISE_SMTP_FROM` | required with host | sender address, e.g. `mise <mise@example.com>` |
| `MISE_OIDC_ISSUER`, `MISE_OIDC_CLIENT_ID`, `MISE_OIDC_CLIENT_SECRET` (or `_FILE`), `MISE_OIDC_NAME` | unset, name `SSO` | optional single sign-on next to passwords (see Auth) |

## Architecture

```
cmd/mise-server/          main: serve, migrate, backup subcommands
internal/config/
internal/store/           DB pools, dialect, rebind, time codec, tx helper, Scoped / System access
internal/store/migrations/{sqlite,postgres}/
internal/tenant/          household + user in request context
internal/auth/            sessions, passwords, invites, reset tokens, public link tokens, middleware, rate limit
internal/i18n/            catalog, T(ctx, key), locale-aware number parsing
internal/groceries/       items, categories, lists, entries, amount parser
internal/recipes/         recipes, ingredients, steps, tags, favourites, search
internal/plan/            planned meals, cook events, suggestions
internal/images/          upload, validate, orient, resize, serve
internal/ai/              settings, secret box, providers, tool loop, import jobs, JSON-LD extractor, safe fetcher
internal/live/            per-household pub/sub, SSE fan-out
internal/jobs/            periodic sweeps (soft deletes, orphan images, expired sessions/invites)
internal/ui/              templ components, layouts, static assets (embedded)
internal/web/             HTTP handlers wiring domain packages to ui
```

Domain packages own their SQL and business rules; `web` handlers parse requests, call domain services, render templ.

### Multi-tenancy

- `households`; `users` are global; `memberships(household_id, user_id, role)`; a session pins one active household.
- Every table carries `household_id`, child tables included. Every parent table has `UNIQUE (household_id, id)`; foreign keys are composite, e.g. `(household_id, recipe_id) → recipes(household_id, id)`, so the database rejects cross-household references.
- References to people (`added_by`, `created_by`, `updated_by`, `notes_by`, `favourites.user_id`, `cook_events.user_id`, `invites.invited_by`) are composite FKs to `memberships(household_id, user_id)`, so attribution is household-checked too.
- **Composite FKs use `ON DELETE RESTRICT`, never `SET NULL`.** `SET NULL` on a composite FK would also null `household_id`, and SQLite has no column-list form. Where a reference should become NULL (category deleted → items uncategorised; recipe hard-deleted → list entries lose their recipe tag), the domain code nulls the child column in the same transaction before deleting the parent. `ON DELETE CASCADE` is fine for owned children (steps, ingredients, recipe_tags).
- Removing a member: membership row stays with `removed_at` set (attribution keeps working, sign-in is refused); hard removal only when they own nothing. Their recipes stay, as the feature doc requires.
- Repositories take a `store.Scoped` value built from the request context. Code without a request (sweeps, AI jobs, SSE renders) uses `store.ScopeFor(householdID)`; global tables (users, sessions, invites by token, households) go through `store.System()`. Both are greppable, and a custom lint check restricts them to `internal/jobs`, `internal/ai`, `internal/live` and `internal/auth`. Public recipe links are bearer tokens like sessions, so `internal/auth` resolves them too and hands the request a context whose only household is the link's (see Public recipe links).
- Every index used for lookups starts with `household_id`, except the global token lookups (`sessions.id`, `invites.token_hash`, `password_resets.token_hash`, `recipe_shares.token`).
- A test walks every scoped repository method against a two-household fixture and asserts no rows of the other household come back.
- First run: setup page creates the household and the first admin. With one household, the household picker never renders.

#### Multiple households per user (O4, decided)

A person can belong to several households (family, parents, flat share). The data model already allows it; the work is in sign-in, invites and the UI. The mockups are the "Household" boards of the app design canvas.

- **No migration.** `users` are global, `memberships` is many-to-many, and `sessions.household_id` and `api_tokens.household_id` are composite FKs to `memberships(household_id, user_id)`. A session pins one active household; scoping, live events and the isolation tests stay as they are.
- **Switching** is `POST /settings/households/{id}/switch`: it checks an active membership, updates `sessions.household_id` of the current session only (other devices keep theirs) and redirects to `/recipes`, never back to the page left, whose IDs belong to the old household. `internal/auth` owns it (it writes a global table). Open tabs of the old household keep their stream until reloaded; their writes hit the new household with foreign IDs and fail as not found, which scoping already guarantees.
- **Sign-in** keeps starting in the oldest active membership. Remembering the last used household would need a column (`users.last_household_id`); decided against.
- **Selector** renders only with two or more active memberships: a chip above the page title on phones, a control at the top of the sidebar on desktop, and "Settings › Account › Your households" (list, Switch, join, leave). The list is loaded when a page renders (one query in `s.page`), not on every request, and is not part of `Identity`. Without JavaScript the chip links to the Account list, where Switch is a plain form post.
- **Invites.** `/invite/{token}` today only creates an account. For a signed-in user it offers "Join {household} as {name}": `POST` adds the membership with the invite's role, marks the invite used and switches the session to it. A signed-out person with an account signs in first and opens the link again (it stays valid). Account creation is unchanged. The Account page gets a field to paste an invite link.
- **Leaving** (`POST /settings/households/{id}/leave`): sets `removed_at` (attribution keeps working), deletes the member's sessions, API tokens and open invites for that household (the logic of member removal), refused for the last admin and for the user's last household (an account always has a household). Leaving the active household switches the session to another one.
- **API tokens** stay bound to one household, the active one when created. Account › API tokens names that household, and lists only its tokens.
- **Per user, not per household:** name, avatar colour, theme, language, units, password and SSO links. The household's timezone, week start and default language apply to the active one.
- **Out of scope:** creating a second household from the UI (it appears through an invite from another household or an operator), deleting a household, moving recipes between households.
- **Tests:** `isolation_test.go` cases for switch (foreign household refused, removed member refused, other sessions untouched), invite accept for an existing user, leave rules, and a web test of the selector's visibility.

Decisions: Q1 no "New household" in the selector; households come through invites only. Q2 sign-in keeps starting in the oldest active membership, no new column. Q3 leaving the user's last household is refused.

### Database layer

- One set of SQL statements for both engines. Portable subset: `RETURNING`, `ON CONFLICT`, partial indexes, `TRUE`/`FALSE` literals (or bound booleans). Placeholders written as `?`, rebound to `$n` for Postgres by a rebinder that skips quoted strings and comments.
- **Time:** never use `CURRENT_TIMESTAMP`, `now()` or SQL date functions. Every timestamp is bound from Go via a store codec: SQLite column `TEXT` in fixed-width UTC `2006-01-02T15:04:05.000000Z`, Postgres `timestamptz`. Fixed width keeps text comparison correct on SQLite. Cutoffs ("expired", "older than 24 h", "not cooked in a month") are computed in Go and bound as parameters.
- **Dates** (planned meals): SQLite `TEXT 'YYYY-MM-DD'`, Postgres `date`; same codec.
- **IDs:** SQLite `TEXT`, Postgres `uuid`; codec converts.
- **Case-insensitive names:** a `name_key` column filled in Go with `cases.Fold` + whitespace normalisation; `UNIQUE (household_id, name_key)` on both engines. Handles umlauts identically everywhere.
- **Soft-deleted rows and uniqueness:** unique indexes on soft-deletable tables are partial (`WHERE deleted_at IS NULL`).
- **Search** (recipes by name and ingredient, items in the add field) matches the folded `name_key` with `LIKE '%…%'` within one household, on both engines. Search runs as you type and must match inside words ("ging" finds "Gingerbread" and "fresh ginger"), which word-based full text (FTS5, `tsvector`) does not do. A household holds hundreds of recipes, not millions, so a scan of its rows is fast enough. Ranking (exact, prefix, word start, contains) happens in Go. Revisit with trigram indexes (FTS5 `trigram`, `pg_trgm`) only if a household outgrows this.
- **SQLite pools:** two `*sql.DB` handles on the same file. Writer: `SetMaxOpenConns(1)`, `_txlock=immediate`. Reader: pool of N, read-only. Pragmas in the DSN so every connection gets them: `_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)`. Postgres also uses a writer pool and a reader pool whose connections set `default_transaction_read_only=on`, so writes through `Read()` fail on both engines. `Tx` refuses to nest (`ErrNestedTx`): on SQLite it would deadlock on the single writer.
- **Tests:** store tests run against both engines: SQLite on a temp file per test (not `:memory:`, which gives each pooled connection its own database), Postgres via testcontainers-go.
- **SQLite table rebuilds** (anything `ALTER TABLE` can't do) use `-- +goose NO TRANSACTION` with `PRAGMA foreign_keys=OFF` and their own `BEGIN`/`COMMIT`, since the pragma is a no-op inside goose's transaction and `DROP TABLE` would cascade; Postgres uses `ALTER TABLE`. See `internal/store/migrations/README.md`.
- **Schema parity:** CI migrates both engines and compares tables, columns, nullability, unique keys and foreign keys from the catalogs; a mismatch fails the build.

### Schema

All tables also have `household_id` (except `households`, `users`) and `created_at`/`updated_at`.

| Table | Key columns |
|---|---|
| households | id, name, timezone (IANA), week_start (mon/sun), default_language |
| users | id, username (unique), name, email, password_hash, avatar_color, theme, language, units |
| memberships | household_id, user_id, role (admin, member), removed_at |
| sessions | id (hash of token), user_id, household_id, expires_at, user_agent, last_seen_at |
| invites | id, token_hash, email, role, invited_by, expires_at (7 days), used_at |
| password_resets | id, token_hash, user_id, expires_at (1 hour), used_at; global like sessions (no household_id) |
| categories | id, name, name_key, color, position, catalog_key (nullable; the catalogue category this one stands for, see Default ingredients); "Uncategorised" is implicit (NULL) and sorts last |
| items | id, name, name_key, category_id, purchase_count, last_purchased_at, icon (nullable override), catalog_key (nullable link to the embedded catalogue; unique per household when set) |
| shopping_lists | id, name, position (first = default), deleted_at (Undo for list delete). First-run setup creates the default list and categories in the household's language (categories carry `catalog_key`, see Default ingredients), in its transaction; startup backfills households created before P1b, so every GET stays read-only. |
| list_entries | id, list_id, item_id, quantity, unit, amount_text, note, added_by, recipe_id (added in P2 with its FK), checked_at, deleted_at |
| recipes | id, name, name_key, description, photo, prep_min, cook_min, servings, source_url, notes, notes_by, notes_at, draft, created_by, updated_by, deleted_at |
| recipe_ingredients | id, recipe_id, item_id, quantity, unit, amount_text, optional, position |
| steps | id, recipe_id, position, text, timer_seconds (set per step in the editor, P4) |
| tags / recipe_tags | id, name, name_key, pinned / recipe_id, tag_id |
| favourites | user_id, recipe_id |
| planned_meals | id, date, recipe_id, servings, position (order within the day), created_by, deleted_at (several per date allowed; hidden while their recipe is in the bin, deleted with it) |
| cook_events | id, recipe_id, user_id, cooked_at (table created in P2 for the library's filters and sorts; written from P3 on) |
| ai_settings | household_id (PK), enabled, provider, endpoint, model, api_key_enc, allow_fetch_page, allow_search_items, allow_create_item, tested_at, test_ok (P6) |
| import_jobs | id, user_id, source_url, source_text, status, steps (JSON), draft (JSON), error_code, error_detail, started_at, finished_at (P6) |
| user_identities | issuer, subject (primary key), user_id (unique per issuer, cascades with the user), created_at; global like sessions |
| api_tokens | id, household_id, user_id (composite FK to memberships), name, token_hash (SHA-256, unique), scope (`read`, `write`), expires_at, last_used_at, created_at, updated_at; global like sessions (looked up by hash) |
| recipe_links | recipe_id, linked_id; one row per direction, written and deleted in pairs ("Goes well with", see Linked recipes) |
| recipe_shares | recipe_id (one link per recipe), token (unique, stored as issued), created_by (see Public recipe links) |
| item_nutrition | item_id (PK with household_id), kcal, protein_g, fat_g, saturated_fat_g, carbs_g, sugars_g, fibre_g, sodium_mg (all per 100 g, nullable except kcal), density_g_ml (nullable), updated_by (see Nutrition) |
| item_unit_weights | item_id, unit (`piece`, `clove`, `slice`, `bunch`, `can`, `pack`), grams; PK (household_id, item_id, unit) (see Nutrition) |

Derived, not stored: "on a list" per ingredient, times cooked (count of `cook_events`), "not cooked in a month", suggestion ranking, nutrition per serving (see Nutrition).

### Decisions on the feature doc's open questions

- Meals per day: several allowed; the week view shows the first and a "+N" marker. Adding or moving a meal onto a day appends it after the day's meals. Dropping a meal on a day that has meals (or choosing "Swap with …") swaps it with that day's first meal; the day's other meals stay. Moves and swaps return the placements that undo them.
- Recipe ingredients go to the first list. Planning a meal puts the ingredients picked in the add-to-plan sheet on it, scaled to the meal's servings, in the same transaction; items already on the list stay as they are.
- Changing servings on a planned meal does not touch amounts already on a list.
- Times cooked: `cook_events` written by cook mode's last step ("Done") or a manual "I made this" (recipe menu, and the meal menu of today's and past meals; it records now and can be undone for 24 h); past planned meals are not counted automatically.
- "Haven't cooked in a while": recipes last cooked more than 3 weeks ago, or never, not planned in the week shown. Recipes cooked before come first, longest ago first, because they are known to be liked; never-cooked ones follow, oldest first. The plan shows three; wide screens add "Last: 5 weeks ago". Tapping one opens the add-to-plan sheet; dragging it onto a day plans it at the recipe's servings without shopping.
- "Plan a meal" on an empty day opens a recipe picker (search, "haven't cooked in a while", all recipes), then the add-to-plan sheet with that day chosen. The sheet's ingredient chips start unpicked and only list ingredients not on the first list.
- "Suggested for tonight" on Recipes: today's first planned meal, else the first "haven't cooked in a while" pick not planned this week.
- A meal whose recipe is deleted is hidden while the recipe is in the bin and comes back if it is restored; it is deleted with the recipe.
- Editing categories, items, tags, lists: everyone.
- Live updates: SSE.
- Amounts: structured quantity (decimal) + unit + original text; scaling acts on the structured part, free text like "1 thumb" is left as is. The parser is locale-aware: decimal comma ("1,5 kg"), fractions ("½", "1/2"), English and German units ("EL", "TL", "Prise", "Bund").
- SSO: OIDC via `coreos/go-oidc`, optional and next to passwords (see Auth).
- KitchenOwl import: later.

### Time and locale

- "Today", week boundaries and month views use the household's `timezone` and `week_start` (setup asks; defaults from the browser). Never the server's local time; the container runs in UTC.
- UI language per user (`users.language`, falling back to the household default, then `Accept-Language`).
- `internal/i18n` provides `T(ctx, key, args...)`; every templ component uses it from P0 on. Catalog files per language, embedded. A test fails on keys missing from the English catalog, and on keys or format verbs a translation lacks.

### Live updates

- `internal/live`: in-process pub/sub keyed by household. Writes publish a typed event (`list.changed{list_id}`, `plan.changed{week}`, `recipe.changed{id}`) after commit.
- Each page opens one Datastar SSE stream (`GET /live?page=…`). The handler subscribes, and on a relevant event re-renders the affected fragments **per subscriber** (each viewer's user, locale and favourites) and sends element patches.
- Broadcast to all subscribers, the acting client included. Patches are idempotent morphs, so the acting client receiving its own change twice is harmless. No per-tab origin tracking.
- Never patch a region that contains a focused text input: the add field and edit sheets sit outside patch targets; patches target the list body only.
- Stream handling:
  - Clear the write deadline per stream with `http.ResponseController.SetWriteDeadline(time.Time{})`; the server keeps normal timeouts for other routes.
  - Heartbeat comment every 25 s; `X-Accel-Buffering: no`.
  - Client closes the stream when the tab is hidden (`visibilitychange`) and reopens it with a full refresh of the page region when visible.
  - Sign-out, session revocation, password change or reset and member removal publish a per-user `sessions.changed` event; each of that user's streams re-checks its session (also on every heartbeat) and ends when it is no longer valid.
  - At most 8 streams per session; a newer one ends the oldest. Graceful shutdown ends all streams (`RegisterOnShutdown`) instead of waiting for them.
- Browsers allow 6 HTTP/1.1 connections per origin; with SSE plus streamed action responses, many tabs on plain HTTP can stall. Docs recommend serving through a TLS reverse proxy (HTTP/2). Single instance only; multi-instance would need Postgres LISTEN/NOTIFY, not planned.

### Undo

- Remove/clear/check-off set `deleted_at` or `checked_at`; the toast's Undo posts to an endpoint that clears it. A sweep in `internal/jobs` hard-deletes rows soft-deleted more than 24 h ago; Undo endpoints check the 24 h window themselves, since the sweep runs hourly.
- Moves (planned meals) return the inverse operation in the toast; Undo posts it.
- Confirmations only for delete recipe and clear cart, per the design.

### Auth and request security

- Username + password sign-in; argon2id; session cookie (`HttpOnly`, `Secure` when base URL is https, `SameSite=Lax`), 30-day sliding expiry; session list on the Account page with revoke. `last_seen_at` and expiry are written at most once per 5 minutes per session to spare the SQLite writer.
- CSRF: `http.CrossOriginProtection` (uses `Sec-Fetch-Site`, falls back to Origin vs Host) plus `AddTrustedOrigin(MISE_BASE_URL)`. It always allows GET/HEAD/OPTIONS, so **every state-changing route is POST/PUT/PATCH/DELETE**, and Datastar actions use `@post`/`@put`/`@patch`/`@delete`, never `@get`, for writes. A router test asserts no GET route is registered in the mutating handler set.
- Client IP from `X-Forwarded-For` only when `RemoteAddr` is in `MISE_TRUSTED_PROXIES`.
- Rate limit sign-in and reset per client IP, per client IP and username, and per username (in-memory token buckets). The per-username bucket refills faster than one IP may spend, so a lockout needs several addresses; successful sign-ins give their attempts back. The account page's current-password check has its own per-user bucket.
- Email change on the Account page takes the current password (other profile changes do not); with SMTP the old address gets a notice. An account without a password (SSO only) cannot change its email address or set a password from its session until an admin sets one; the page asks it for no password and offers no password change, and a password sign-in to it is refused like a wrong password (`docs/security-review.md` F1).
- Invites: copyable link (always), email if SMTP is configured; token stored hashed; role chosen at invite time.
- Reset: admin reset under Settings › People (P1a); with SMTP, self-service by email (P4): the link is valid 1 h and single-use.
- Roles enforced in middleware for admin routes (people, AI settings).
- API tokens (`docs/api.md`): `mise_` + 256 random bits, only the SHA-256 stored, per member and household, scope `read` or `write`, optional expiry, at most 20 per member. Created and revoked only from a browser session (Account › API tokens; the token is shown once through a short-lived `Strict` cookie, like invite links). `/api/` is outside the cookie session: `LoadSession` skips it and `apiAuth` accepts only a bearer token, so CSRF has nothing to ride on and a token never opens a page. A request is authenticated like a session (removed members fail) and carries the member's `Identity` with `APIScope`; write routes need `write`. Member removal and password reset delete the member's tokens; `last_used_at` is written at most hourly.
- SSO (optional): authorization code + PKCE; state, nonce and verifier in a 10-minute HttpOnly cookie on `/auth/oidc/`. `POST /auth/oidc/start` redirects to the provider with the callback on the host the request came in on (`MISE_BASE_URL` or one of `MISE_EXTRA_BASE_URLS`), since the flow cookie is host-only; `GET /auth/oidc/callback` is the one state-changing GET (registered through `router.callback`, which the router test pins). `user_identities` (issuer, subject → user, migration `00008_oidc`): an identity signs in only as the account linked to it; SSO never creates accounts and never links by email address (a member could set their address to a housemate's and catch the housemate's first sign-in, `docs/security-review.md` F2). Linking happens only from a signed-in session: "Link" on the Account page (`POST /settings/account/sso/link`) runs the same flow with the flow cookie bound to a hash of the session ID, and the callback links the identity to that session's account, refusing a changed session, an identity linked to another account and a second identity of the provider. An unlinked SSO sign-in is refused with a hint to sign in with the password and link. Unlink on the Account page needs a password; member removal drops links. Callback attempts share the sign-in per-IP limit. Details in `docs/operations.md`.

### Images

- Upload via form, max 15 MB. Before decoding, `image.DecodeConfig` rejects images over 40 megapixels. One upload decodes at a time (a 40 MP decode is about 160 MB); a request waiting for its turn gives up when it is cancelled.
- Accepted: JPEG, PNG, WebP (sniffed from bytes, not the extension). HEIC/HEIF is detected and rejected with a clear message ("Convert to JPEG or pick from Photos"). The file input lists `image/jpeg` first, which makes iOS convert when picking from Photos; picks from Files and macOS uploads can still send HEIC.
- EXIF orientation is read and applied before re-encoding (which strips all metadata).
- Output: original capped at 2400 px long edge and a 600 px thumbnail, both JPEG.
- Path: `$MISE_DATA_DIR/images/<household_id>/<sha256>-<size>.jpg`, content-addressed.
- Served through an authenticated handler that checks household ownership; `Cache-Control: private, max-age=31536000, immutable`. A public recipe link serves its recipe's photo through its own route instead (see Public recipe links).
- Orphan sweep removes files no recipe references.

### AI import

Recipe import from a link or pasted text. Pages with schema.org `Recipe` JSON-LD import without a model, so link import works for every household; the model (off by default, per household) handles pages without JSON-LD and pasted text.

- **Settings** (admin, Settings › AI import): enabled, provider, endpoint, model, API key, tool switches, test connection. Providers: "Anthropic API" (endpoint fixed, default model `claude-opus-5-5`), "OpenAI-compatible API" (endpoint and model required), "Local model (Ollama)" (OpenAI-compatible preset: endpoint `http://localhost:11434/v1`, no key; the model must support tool calls). Changing provider, endpoint, model or key clears the last test result. Changing the provider or endpoint also drops a stored key unless a new one is entered, so a key is never sent to a server it was not entered for. Enabling the Anthropic provider requires a key.
- **API key:** AES-256-GCM, never rendered back (the field shows "Replace"; a blank submit keeps the stored key). Key from `MISE_SECRET_KEY` (base64, 32 bytes) or generated once into `$MISE_DATA_DIR/secret.key` (0600). Stored as `v1:` + base64(nonce ‖ ciphertext) with the household ID as additional data, so a ciphertext copied to another household does not decrypt. Backups leave `secret.key` out; after a restore on a new machine without it (and without `MISE_SECRET_KEY`), the settings page says the key can't be read and asks for it again.
- **Who:** admins configure; every member imports. Import jobs are per user: the library and job pages show only the viewer's own jobs.
- **Safe fetcher** for the page (and its photo) and for `fetch_page`, not for the admin-configured provider endpoint (often a LAN Ollama):
  - Custom `net.Dialer.Control` checks the IP actually being dialled, so DNS rebinding cannot slip past a pre-resolve check.
  - Blocks loopback, private (RFC 1918), link-local, CGNAT `100.64.0.0/10`, IPv6 ULA `fc00::/7`, IPv4-mapped IPv6, `0.0.0.0/8`, multicast, and the other special-purpose ranges (NAT64, 6to4 and Teredo, which embed IPv4 addresses; benchmarking; reserved); ports 80 and 443 only, checked with the address; no proxy from the environment.
  - Re-validates on every redirect (max 5); 5 MB body cap (15 MB for photos); 15 s timeout; `http`/`https` only; pages must be HTML, XHTML or plain text.
- **Pipeline for a link:**
  1. The server fetches the page (user-initiated, so no permission switch).
  2. JSON-LD `Recipe` (also inside `@graph`, and `HowToSection`/`HowToStep` instructions): name, description, prep/cook time (ISO 8601 durations), yield → servings, ingredients, steps, keywords/category → tags, image. Ingredients go through the amount parser; the name drops notes (parentheses, text after a comma, and words such as "plus", "for", "to", "zum", "für": "oil plus extra for frying" is "oil") and splits alternatives ("or", "oder"; "sunflower or vegetable oil" accepts sunflower oil or vegetable oil). Items match by `name_key` like the editor: exact on the first alternative is Matched, exact on a later one is Check, then an item named within the line, then the item search's ranking; no match → "New item", named after the first alternative, or the last when the first may be short for it. No model call, AI on or off.
  3. No JSON-LD: with AI on, the page's text (scripts, styles, nav stripped; capped at 50 KB) goes to the tool loop; with AI off the job fails with "No recipe data on that page" and offers "Type it myself".
- **Pasted text** (AI on only): the text (max 50 KB) goes straight to the tool loop.
- **Tool loop** with these tools, each gated by a switch:

  | Switch (settings label) | Tool | Default | Effect |
  |---|---|---|---|
  | Read recipe web pages | `fetch_page(url)` | on | fetches another page **on the source page's host**, redirects included (e.g. a print view) through the safe fetcher; returns text plus any JSON-LD |
  | Look up your grocery items | `search_items(query)` | on | up to 10 items `{id, name, category}`, household-scoped |
  | Create new grocery items | `create_item(name, category)` | off | records a proposal on the job, returns a `new:N` reference; nothing is written until the user saves |
  | Create recipes | `create_recipe(...)` | always on, not a switch | submits the draft and ends the loop |

  The model never writes to the database: every write happens when the user saves the review. It cannot update or delete anything. With `search_items` off, the server matches ingredients by name after the loop; with `create_item` off, unmatched ingredients show as "New item" for the user to confirm. Fetched pages and pasted text are untrusted input: they go into user-turn content, never into the system prompt, and the system prompt says to ignore instructions found in them.
- `create_recipe` input (strict schema): name, description, prep_min, cook_min, servings, source_url, ingredients `[{text, item}]` (`text` as written, e.g. "2 tbsp soy sauce", parsed by the amount parser; `item` an item ID, `new:N` or empty), steps `[{text, timer_seconds}]`, tags (names; only existing household tags are kept), image_url. The draft keeps the source language.
- Limits: 12 model turns, 3 minutes per job, 16k max output tokens per turn, tool results capped at 50 KB.
- **Providers** behind `ai.Provider`: `Chat(ctx, Request) (Response, error)` with a provider-neutral request (system, messages, tool definitions with JSON Schema) and response (text, tool calls, stop reason, token usage), plus `Test(ctx)` (one tiny request). Implementations: `anthropic-sdk-go/v2` (Messages API, adaptive thinking, default effort; `tool_choice: auto` with `strict` tools since forced tool use is rejected on current models; server-side refusal fallback `fallbacks: "default"` with the `server-side-fallback-2026-07-01` beta; a refusal fails the job) and `openai-go` (chat completions with tools, for OpenAI-compatible endpoints and Ollama). Tests use a scripted fake provider; the two clients are tested against `httptest` servers replaying recorded responses.
- **Jobs:** `import_jobs` rows are the queue. A pool of 2 workers per process takes queued jobs oldest first, with `store.ScopeFor(household)`; at most 3 queued or running jobs per user (a fourth is refused with a message). Each step (`{kind, text, tool, state}`, worded "Reading…" while running, "Read…" when done, "Couldn’t read…" when failed) is appended to `steps`; each model turn is a step too ("Looking for the recipe…", then "Going over the results…"), since it is the longest wait, and published as `import.changed{job_id}` to the user. Cancel cancels the job's context (or marks a queued job canceled). On start, jobs left queued or running by a previous process (created before this process's service) are marked failed ("The server restarted. Try again."). Ready and failed jobs are swept 14 days after they finish; saved, discarded and canceled jobs are deleted right away.
- **Statuses:** queued, running, ready, failed, canceled. Failures store an error code (`no_recipe`, `fetch_failed`, `blocked_address`, `too_large`, `provider_error`, `refused`, `limit`, `interrupted`) shown as translated copy; provider errors also show the provider's message under "See details".
- **Running screen** (`/imports/{id}` while queued or running): source, live step list and progress bar from `GET /live?page=import&job={id}`, "You can leave this screen. The draft will be waiting under Recipes.", Cancel. Without JavaScript the page refreshes itself every 3 s (`<meta http-equiv="refresh">`) until the job finishes.
- **Failed screen** (same URL): the error, then the import form with "Pasted text" selected (when AI is on) and the link kept, "Type it myself" (the empty editor) and "Import text".
- **Review screen** (same URL when ready): the draft card (photo, name, times, servings, steps count, tags), the assistant's steps (collapsed on phones), and the ingredient table "On the page / Your item / Status" with Matched, Check (model match whose name differs from the page's, or a JSON-LD match that was not exact) and New item. Each row's item is changeable ("Choose another item": the item search, or a new item). Actions: "Save recipe" (with "& N new items" when any), "Open in editor", "Discard". Save creates the new items, the recipe with ingredients, steps, tags and photo in one transaction, deletes the job and goes to the recipe. "Open in editor" opens `/recipes/new?import={id}` prefilled with the draft (ingredients as typed lines); the editor's save deletes the job.
- **Photo:** downloaded through the safe fetcher only on save (review save or editor save), then through the image pipeline; a failed download saves the recipe without a photo and says so in the toast. Review shows the remote photo through a server-side proxy (`/imports/{id}/photo`) that uses the safe fetcher, never as a direct `<img src>` to the source host.
- **Drafts under Recipes:** the library shows the viewer's jobs above the cards ("Importing…", "Ready to review", "Import failed"), each linking to its job page. `recipes.draft` stays unused by import: a draft is the job row until saved.
- **Entry points:** the editor's "Have a link? Import it with AI" card (shown on new recipes; reads "Import a recipe" when AI is off, since link import still works) and "Import" in the library's add menu, both to `/recipes/import`. The page has "Link" and "Pasted text" tabs; "Pasted text" shows only with AI on.

### UI structure

- Layout shell: bottom bar (< 768 px), icon rail (768–1199 px), sidebar (≥ 1200 px), CSS only.
- Tokens from the style guide as CSS custom properties; `data-theme` on `<html>` for System/Light/Dark.
- Accessibility: real buttons/links, ≥ 44 px targets, `aria-live` regions for counts and toasts, tap alternatives to drag, 4.5:1 contrast.
- PWA: `manifest.webmanifest`, icons, minimal service worker (no caching beyond what install requires).

### Operations

- `mise-server backup <dir>`: SQLite via `VACUUM INTO` + tar of the images directory, into one timestamped archive. For Postgres, docs describe `pg_dump` plus the images tar; the command still archives images.
- `mise-server migrate` runs migrations without serving; `serve` runs them on start.
- `/healthz` (process up) and `/readyz` (DB reachable, migrations current).
- `slog` JSON request logs: method, route pattern, status, duration, household ID; no request bodies.
- Docs: reverse proxy examples (Caddy, nginx) with SSE settings, HTTP/2, trusted proxies.

## Phases

Each phase ends with something usable and is merged before the next starts.

### P0 — Skeleton
- go.mod, `mise.toml`, task runner (`gen`, `lint`, `test`, `run`).
- Config, store: both drivers, SQLite reader/writer pools with DSN pragmas, dialect, rebinder, time/date/ID codecs, Scoped/ScopeFor/System, migrations runner; first migration with `households`, `users`, `memberships`, `sessions`.
- Schema-parity CI check and two-engine test harness (temp-file SQLite, testcontainers Postgres).
- i18n package with `T(ctx, key)` and English catalog.
- templ + Datastar (vendored JS) wired; layout shell with nav and tokens (light/dark); static asset embedding with hashed filenames.
- `/healthz`, `/readyz`, slog request logging, CSRF middleware, router test for mutating methods.
- CI: lint, `templ generate` diff check, tests on both engines.
- Dockerfile (distroless static, volume `/data`).
- Done when: `mise-server` starts on either database and serves the empty shell.

### P1a — Accounts
- First-run setup (household name, timezone, week start, base URL, first admin), sign in/out, sessions with revoke, rate limiting, roles, invite links, account page, admin password reset, member removal.
- Done when: an admin invites a second person by link and both can sign in.

### P1b — Groceries
- Categories with aisle order, items catalogue (`name_key`), shopping lists (create, rename, reorder, delete).
- List view grouped by category, add field with locale-aware amount parsing and ranked suggestions, check off with Undo, In the cart, clear cart with confirm, Usually bought chips, item edit sheet.
- `internal/live` and SSE for lists; soft-delete sweep.
- Done when: two people shop from the same list on two phones and see each other's changes within a second.

### P2 — Recipes
- Library with cards, quick filters, filter sheet, sort; search by name and ingredient with highlights and recent searches.
- Editor (ingredients typed as "2 tbsp soy sauce" / "2 EL Sojasoße", steps, tags, photo upload), detail with servings scaling, inline ingredient highlights in steps, +/✓ add-to-list toggles with live count, notes, favourites, share link, delete with confirm.
- Image pipeline (orientation, size limits, HEIC message, orphan sweep).
- Settings: tags.
- Done when: a recipe's ingredients reach the list one by one.

### P3 — Meal plan
- Week and month views in household timezone and week start, add to plan (day, servings, optional ingredient picks), meal menu (open, move, change servings, swap, remove), drag to move/swap with Undo, "haven't cooked in a while", cook events.
- Routes: `/plan?week=`, `/plan/month?month=&date=`, `/plan/add`, `/plan/pick`, `/plan/meals/{id}` (menu; `?view=move|servings|swap`), writes under `/plan` and `/plan/meals/{id}/…`, "I made this" at `/recipes/{id}/cooked`. Sheets are Datastar dialogs and plain pages without JavaScript; drops post a JSON payload to the same write routes.
- `GET /live?page=plan` re-renders the shown week or month on `plan.changed`, recipe and list events; `page=recipes` also follows `plan.changed` for "Suggested for tonight".
- Done when: a week can be planned on desktop by drag and on phone by tap.

### P4 — Installable, cook mode, operations
- Manifest, icons, minimal service worker, add-to-home-screen hint.
- Cook mode: one step per screen, wake lock, step timers.
- `backup` command, proxy docs.
- SMTP: invite emails and self-service password reset.

- Order and commits: installable, cook mode, SMTP, backup and docs; one commit each.
- **Installable.** `/manifest.webmanifest` (name "mise", `start_url` `/`, `display: standalone`, theme and background colours from the tokens, PNG icons 192 and 512 plus a maskable 512) and `/sw.js` are served from the root so the worker's scope is `/`; both are infrastructure paths (no session). The worker only installs and activates: no fetch handler, no caching. `apple-touch-icon` (180 px PNG) and `theme-color` metas for light and dark. PNGs are rendered from `icon.svg` (`mise run icons`, ImageMagick) and committed.
- **Add-to-home-screen hint**, per the design: after the first cleared cart, on the groceries page, once per device (remembered in `localStorage`), only in a mobile browser not already running the installed app. iOS Safari shows the Share → Add to Home Screen steps and "Got it"; Chromium keeps the `beforeinstallprompt` event and shows "Install"; other browsers show nothing.
- **Cook mode** at `/recipes/{id}/cook?servings=&step=`, from a "Cook mode" button in the method's header (hidden without steps). Full screen, always dark, outside the app shell. One step per screen with "Step 2 of 5", a progress bar, the step's ingredient mentions with amounts at the detail page's servings (each step highlights its own first mention of an ingredient, where the detail page highlights only the first in the method), previous and next; the last step's "Done" posts "I made this" (`/recipes/{id}/cooked`) and returns to the recipe with its Undo toast. Steps have no titles, so next reads "Next step". All steps are rendered; with JavaScript, app.js switches between them in place (keeping `?step=` in the address), so timers keep running across steps; without it, previous and next are links to `?step=`. Done is a plain form post; a short-lived cookie carries the cook event to the recipe page, which shows its Undo toast once.
- Wake lock: requested on entering cook mode and again when the tab becomes visible; "Screen stays on" shows only while the lock is held (it needs HTTPS or localhost).
- Timers: a step with `timer_seconds` offers "Start 10:00 timer". Several can run; timers of other steps show as chips that jump to their step. A finished timer vibrates, beeps and shows which step it belongs to. Timers live in the browser only (end times in `sessionStorage`, so a reload keeps them).
- Editor: the method is a list of step rows on every screen size (text plus an optional timer in minutes), replacing the phone textarea; `steps_text`/`steps_orig` go away. A save keeps a step's row when its text is unchanged and updates its timer.
- **SMTP** (`wneessen/go-mail`) behind a small `internal/mail` interface, faked in tests. Emails are multipart text and HTML, in the recipient's language (user, then household default); invites use the household default.
- Invites: with SMTP and an email address, creating the invite also emails the link, synchronously with a timeout. The link is shown either way; a failed send shows an error and the invite stays valid. Without SMTP nothing changes.
- Reset: with SMTP, `/forgot-password` asks for the email address and always answers "Check your inbox". Each account with that address (case-insensitive) and an active membership gets its own email naming its username; tokens are created and sent in the background after the response, so the answer does not reveal whether an account exists. Rate limited per client IP and per address. `/reset/{token}` sets a new password, marks the token used, drops the user's other reset tokens, revokes all their sessions (`sessions.changed`) and signs in on this device. Without SMTP the page keeps "Ask an admin". Expired and used tokens are swept.
- **Backup** as in Operations: `mise-server backup <dir>` writes `mise-backup-<UTC timestamp>.tar.gz` with the SQLite database (`VACUUM INTO`) and the images directory. On Postgres the archive holds only the images and the docs describe `pg_dump`/`pg_restore`. Restore is documented (stop the server, unpack into the data directory), not a command.
- **Docs** in `docs/operations.md`: reverse proxy (Caddy, nginx) with SSE buffering off, HTTP/2, `MISE_BASE_URL`, `MISE_TRUSTED_PROXIES`; backup and restore for both engines; SMTP.

### Linked recipes
- "Goes well with": a recipe links to other recipes of the household (side dishes, sauces, breads), up to 20. A link goes both ways: `recipe_links` (migration `00009_recipe_links`) stores a row per direction, and saving a recipe replaces its links on both sides. Composite FKs cascade with a hard-deleted recipe; a recipe in the bin drops out of the lists and keeps its links, so a restore brings them back.
- Editor: linked recipes are checked boxes (untick to unlink); the add field suggests recipe names (`<datalist>`) and resolves the typed name case-insensitively on "Add" or save, so it works without JavaScript like the tag field. An unknown name is a field error.
- Detail page: "Goes well with" sits in the header under the tiles: a label and up to two chips (thumbnail or initial and name, linking to the recipe). With more links a "+N more" chip expands the full list in place (name and total time per row); it is a checkbox label, so it works without JavaScript. Nothing shows without links. Saving, deleting or restoring a recipe publishes `recipe.changed` for every recipe linked to it, so their open pages follow.
- Add-to-plan sheet: "Plan with it" chips (unpicked) for the linked recipes; picked ones are planned the same day at the same servings after the meal, in Add's transaction. The toast's Undo removes them with the meal.

### Units
- Each user reads amounts in Metric or US (`users.units`, migration `00010_units`, default metric), set next to theme and language. Recipes and list entries are stored as written and converted when shown, per viewer, so a shared list or recipe reads in each member's units.
- Only structured amounts in the other system's units convert: g/kg ↔ oz/lb, ml/cl/dl/l ↔ tsp/tbsp/fl oz/cups, using US measures (1 cup = 236.6 ml). Metric to US: oz below a pound, then lb in quarters; tsp below a tablespoon, tbsp below 2 fl oz, fl oz below a cup, then cups in quarters. US to metric: g/ml to the gram below 100, in fives from 100, kg/l in 0.05 from 1000. mg, tsp, tbsp, pinches, pieces and free text ("1 thumb") stay as written, as do amounts already in the viewer's system. A recipe written in cups (or "Tasse") reads in ml for metric users.
- A recipe scales in the unit it was written in and converts after, so the unit fits the scaled amount (800 g reads as 1.75 lb, not 28 oz).
- Temperatures in step text ("180°C", "350 °F", "180–200 °C") convert on the detail page and in cook mode: to five degrees from 100, whole degrees below. The editor always shows what was written. The parser knows `fl oz` ("fl oz", "fl. oz", "floz").

### Default ingredients
Status: built (migration `00013_item_catalog`), with a seed catalogue of 139 entries, all with an icon (69 icons); growing it to 300 entries and about 200 icons is data work (D2, D4). Where the built version differs from the text below, the notes marked "Built" say how. Icons: see `docs/icon-experiments/VERDICT.md`. Design: the "Catalogue" boards of the design artifact.

**Today.** A new household starts with one list and six categories (`groceryDefaults`, `internal/web/groceries.go:34`, run by `SeedTx` in the setup transaction and by `SeedGroceries` at startup for older households) and **no items**. Items appear only through `EnsureItem`/`EnsureItemIn` (`internal/groceries/entries.go:257`): typing a name in the add field, the recipe editor, and saving an import. Add-field suggestions (`SearchItems`) search only the household's own items, so a new household gets no suggestions. Import matching (`matchIngredient`, `internal/ai/draft.go:135`) searches the same table; a line with no hit becomes `MatchNew`, shown as "New item" (`import.match.new`) with only the model's category guess. Items have no icon; the UI shows a category-coloured initial tile (`.tile`).

**Goal.** Ship a catalogue of common ingredients with a stable key, a localized name, a default category and an icon, so suggestions work from the first minute, imported ingredients arrive categorised and with an icon, and other features (nutrition) have one key per ingredient to hang data on.

#### Catalogue
- Embedded data, not database rows and not `en.json` entries, loaded once into an in-memory index per language. Built: `internal/catalog/data/entries.json` holds key, category and icon once (`{"key": "lemon", "category": "produce", "icon": "lemon"}`); `names.en.json` and `names.de.json` hold `{"lemon": {"name": "Lemon", "aliases": ["lemons"]}}`, so category and icon cannot differ between languages. Same key set in both languages; German entries add their own aliases ("Zitronen"). The seed set has 139 entries, the target is 300.
- **The key is the contract.** English snake_case ASCII slug, `^[a-z][a-z0-9_]*$`, unique, immutable, never reused. Other features (nutrition density and piece weights, allergens, seasonality) live in their own files keyed by it, never in the name files. Renaming a name or alias is free; changing a key is a breaking change. A key is retired by moving it to `data/retired.json` as `{"key": "old", "replaced_by": "new" | null}`; `TestCatalogKeysAppendOnly` compares against the committed `data/keys.lock` and fails when a key disappears without a `retired` entry.
- `category` is one of a fixed catalogue category key set (see D3), each with a `groceries.category.<key>` translation in both locales. `icon` is a key of the shipped sprite; several entries may share an icon (clustering, about 150-250 SVGs for 400 entries).
- Icons: mono duotone on `currentColor` (`stroke-width` 1.75, 24 px grid, 20 % fill). Built: one file per icon in `internal/catalog/icons/<icon>.svg`, embedded and rendered inline inside the existing tile (`catalog.IconSVG`), so `--av-fg` colours it in both themes; no sprite and no generate step. Several entries share an icon (clustering, about 150-250 SVGs for 400 entries). No icon, or an unknown icon key (e.g. after a downgrade): initial tile as today.
- Package API: `catalog.For(lang) *Catalog` with `Entry(key)`, `Lookup(phrase)` (NameKey on name or alias, exact), `Within(text)` (longest entry name inside a phrase; built and tested, not used by the importer, see below) and `Search(typed, n, skip)`. `catalog.NameKey` is the one implementation; `groceries.NameKey` calls it. Languages: `For` takes a tag or code; an unsupported language falls back to English.

#### Schema (migration `00013_item_catalog`, both dialects)
```sql
ALTER TABLE items ADD COLUMN icon TEXT;
ALTER TABLE items ADD COLUMN catalog_key TEXT;
CREATE UNIQUE INDEX items_catalog_key ON items (household_id, catalog_key) WHERE catalog_key IS NOT NULL;
ALTER TABLE categories ADD COLUMN catalog_key TEXT;
CREATE UNIQUE INDEX categories_catalog_key ON categories (household_id, catalog_key) WHERE catalog_key IS NOT NULL;
```
No backfill in SQL: names are translated, so linking existing rows is Go (below). Both columns nullable, so the migration is reversible by dropping them. `TestSchemaParity` must see the same columns and partial indexes on both engines; `isolation_test.go` gains cases for the new reads and writes.

#### Resolution rules
- Item icon shown = `items.icon` if set, else the catalogue entry's icon for `items.catalog_key`, else the initial tile. Category is a household choice and wins over the catalogue's default once an item exists.
- `items.catalog_key` is set when an item is created from a catalogue entry (picked in the add field, import save) or when a typed or imported name matches an entry's name or alias in `EnsureItemIn`. It survives renames: the household may call it "Zitronen" and still has the lemon icon and, later, nutrition data. Clearing it is not offered.
- A household's own items always win over the catalogue: lookup order is existing item (exact), existing item (near), catalogue entry, new free-text item.

#### Seeding and the add field (D1)
Recommended: catalogue entries stay virtual until used.
- Setup seeds only categories, now with `categories.catalog_key` set from the catalogue category set (the six existing ones plus the additions of D3). `groceryDefaults` reads the key set from `internal/catalog`.
- `Suggest` (the add field; `SearchItems` stays household-only because the importer relies on its exact match) merges: household items first (current ranking), then catalogue entries whose key is not already an item's `catalog_key`, ranked by the same Exact/Prefix/Word order (Built: mid-word matches are dropped for catalogue entries, "lem" would otherwise offer "wholemeal flour"), marked `Suggestion.Catalog *catalog.Entry`. Picking one calls `EnsureCatalogItem(key)`: creates the item in the household's language name, the entry's default category (resolved through `categories.catalog_key`; uncategorised if the household deleted it) and `catalog_key`, in the caller's transaction, publishing `CatalogChanged`.
- Why not insert 400 rows per household (the eager variant, D1 option B): the Items settings list and category counts fill with things nobody bought, a deleted item comes back on the next catalogue upgrade unless tombstoned, every catalogue upgrade needs a per-household migration, and purchase-count ranking is buried. The eager variant is simpler to search (one table) and works unchanged for the REST API. If chosen: `SeedTx` inserts every entry (`ON CONFLICT DO NOTHING`), and upgrades insert missing keys guarded by a `catalog_version` column on `households` plus a `deleted_catalog_keys` tombstone table.

#### Importer
- `matchIngredient` gains a step after `ItemsWithin`/near fail. Built: only an exact name or alias of the name the item would get (`name.New`) matches, in the recipe language first, then the household's; `Within` is left out because "firm tofu" proposing "Tofu" would drop what the recipe said. A hit sets `DraftIngredient.CatalogKey`, `Icon`, `ItemName` to the household-language name, `Category` to the entry's default category name (the household's category name, or empty), and `Match = MatchCatalog` ("From catalogue", `import.match.catalog`). The model's category guess is used only when no entry matched; deterministic data beats it.
- The lookup tries the recipe's language first, then the household's, so an English recipe in a German household yields "Zitrone" (key `lemon`). Keys make that cross-language step trivial; names alone would not.
- `Draft` JSON gains `catalog_key` (omitted when empty, so drafts stored before the change still load). `NewItems()` counts both `MatchNew` and `MatchCatalog`. `Save` calls `EnsureCatalogItem` for `MatchCatalog` rows and `EnsureItemIn` for the rest, which now also links a typed name to the catalogue.
- The review row shows the icon next to the name and the category chip; the "New item" badge stays for free text, catalogue hits show the neutral "Catalogue" badge. Picking another item or naming one clears `CatalogKey`.

#### Upgrades and renames
- Existing households: `jobs.LinkCatalog` runs at startup next to `SeedGroceries` (`internal/jobs` may use `ScopeFor`). It sets `categories.catalog_key` where a category's `name_key` equals the translation of a catalogue category in the household's language, and `items.catalog_key` where an item's `name_key` equals an entry's name or alias in the household's language or any other catalogue language (an English household may type German names; the oldest item wins a key). Idempotent; never changes names, categories or icons; skips an item whose key is already used by another item of the household.
- Catalogue upgrades (new release adds entries, fixes names, swaps icons): lazy variant needs nothing per household. A changed icon key shows everywhere at once; a changed name affects only items created afterwards. Existing item names are the household's.
- Keys never change; retired keys keep resolving to their `replaced_by` entry (`catalog.Entry` follows it) so `items.catalog_key` rows never dangle. A `catalog_key` unknown to the binary (downgrade) is treated as unlinked.
- Household renames an item: name changes, `catalog_key` stays. Household merges or deletes: deleting frees the key (the partial unique index), the catalogue entry reappears in suggestions. A second item that resolves to an already-linked key is created unlinked.
- Language change (`households.default_language`): existing names stay; only new items use the new language's names. No renaming sweep.

#### i18n and tests
- Catalogue names are data files, not `i18n.Key`s: 2 x 400 strings in `en.json` would drown the UI strings and `TestKeysUsedExist` only covers code literals. New UI strings (icon picker, "From catalogue", category names, aria labels) go to `en.json` and `de.json` with the usual format-verb parity.
- `TestCatalogParity`: en and de have identical key sets; keys match the pattern; per language, NameKey of every name and alias is unique across entries (a collision would make `Lookup` ambiguous); every `category` is in the category set and has a translation in both locales; every `icon` exists in the sprite and every sprite symbol is used.
- `TestCatalogKeysAppendOnly` (above). `TestSchemaParity` covers migration 13. Service tests on both engines: create from catalogue, typed-name linking, rename keeps key, deleted category leaves the item uncategorised, `LinkCatalog` idempotency; web tests for the add field suggestions through `b.post` and `b.datastar`; an importer test for `MatchCatalog` including the cross-language case.
- Works without JavaScript: catalogue suggestions use the same plain form-post path as household suggestions today; the item editor's icon picker is radio inputs in a `<fieldset>`.

#### UI
- Icon (24 px glyph on the 38 px `.tile--sm`) on list rows, cart rows, add-field suggestions, recipe ingredient rows, import review. Item editor (Settings, and the sheet from a list row): name, category select, icon picker (search, grid, "Use catalogue icon" reset, "No icon"). Boards: Catalogue Add, Catalogue Item editor, Catalogue List.

#### Decisions (all decided, recommended options)
- D1 Virtual catalogue: nothing is inserted at setup, entries become items on first use (eager seeding rejected, reasons above).
- D2 Icons are mono duotone `currentColor` SVGs, model-drafted from a written style spec, human-reviewed on a contact sheet, clustered to 150-250 files in one sprite, under the repo's licence. No emoji anywhere, not as fallback and not as a user choice.
- D3 Nine catalogue categories: produce, bakery, fridge, meat_fish, frozen, pantry, spices, drinks, household. New households get all nine; existing households keep theirs, and entries whose category they lack land uncategorised.
- D4 First release: 300 entries (about 200 icons), growing by release.
- D5 Custom item icons are picked from the shipped icons only. Built: `items.icon` is an icon key, NULL (inherit the catalogue icon) or `none` (deliberately no icon). The picker is a radio grid of all icons without a search field yet; add one when the set passes about 40 icons.
- D6 `items.catalog_key` is unique per household (partial unique index).
- D7 Plural and synonym aliases ship per language.
- D8 Existing items without a catalogue match stay unlinked and keep the initial tile.
- D9 Nutrition data lives in a separate `internal/catalog/data/nutrition.json` keyed by catalogue key, agreed with the nutrition plan.

### Library paging
- The library shows 48 cards (whole rows at 2, 3, 4 and 6 columns) and "Show more" adds 48. Sorting stays in Go, so every match is read and only the shown cards get their tags. The page size is the `$shown` signal on the live stream, as servings are on the recipe page: raising it reopens the stream and the first render is the longer page; without JavaScript "Show more" links to `?shown=`.
- The plan's recipe picker ("All recipes") and the meal menu's swap list page the same way, 48 at a time by name. They are not live, so "Show more" links to the next page size (`shown=`) and Datastar fetches just the list with it, as the search does. Searches list every match. The swap list leaves out the meal's own recipe in the query (`recipes.Filter.Exclude`), so its count is right.

### Public recipe links
- A member turns on a recipe's public link in its ⋯ menu ("Public link": "Create public link", then the address with Copy and "Turn off public link"). Anyone with the address reads the recipe at `/s/{token}` without signing in. Every member sees and manages the link, as with the recipe itself; the menu says who turned it on and when. The detail page says "Public link on" while it is, live (`recipe.changed`), so nobody forgets a recipe is public.
- **Link preview and look.** The page carries `og:title`, `og:description` (the recipe's description, else its times and servings), `og:image` (the photo, as an absolute address from `MISE_BASE_URL` or the request's origin) and `twitter:card`, so chat apps show a card; the photo route is open to anyone with the link, so crawlers can fetch it. The recipe name leads the hero, ingredients sit on a card (sticky beside the method on wide screens) and the page has print styles.
- The page shows what the recipe itself says: photo, name, description, prep and cook time, servings, source link, ingredients and the method with its ingredient mentions, at the recipe's own servings. Nothing about the household: no people, notes, tags, times cooked, linked recipes, list state, edit controls or links into the app (the brand is not a link). It is plain HTML without the app's scripts or manifest. Amounts read in US units when the visitor's first `Accept-Language` names the US (or Liberia, Myanmar) explicitly, metric otherwise; the language follows `Accept-Language`.
- **Token:** 32 bytes from `crypto/rand`, base64url (43 characters, 256 bits), so links cannot be guessed or enumerated; no rate limit is needed. One link per recipe: creating again returns the existing link (two members at once get the same one); turning it off deletes the row, and a new link gets a new token.
- **Stored as issued, not hashed.** Members copy the link again whenever they want, so the server must be able to show it; a hash would make the link show-once like an invite. Hashing would not protect anything either: the token only grants reading one recipe, and whoever can read `recipe_shares` (a database or backup leak) can read `recipes` directly. Sessions and invites are hashed because they grant more than the database holds (acting as a user, joining a household).
- **Revocation is immediate:** every request looks the token up (no cache), and the page is `Cache-Control: no-store`. A recipe in the bin answers like a revoked link and comes back with its link when restored; a hard delete cascades. Removing a member does not turn off links: anyone who had the address keeps it until a member turns it off, like a copy of the recipe they could have made.
- **Isolation:** `/s/` paths skip the session middleware, so a member's identity never reaches them. `auth.RequireShare` resolves the token with `store.System()` (lint-allowed in `internal/auth`, next to sessions, invites and reset tokens) to `(household_id, recipe_id)` from the stored row and puts only that household in the request context; handlers then use `db.Scope(ctx)` like any other, and `recipes.Public` reads only the shareable columns, joined on the link. The recipe ID never comes from the URL, so a token cannot reach another recipe or household. No lint change was needed.
- **Photo:** `/s/{token}/photo/{full|thumb}` serves the shared recipe's current photo from the link's household. It takes no image hash, so a link cannot open any other image, and the members' `/images/` route stays authenticated. `Cache-Control: private, no-cache` with an ETag: browsers revalidate each time (a cheap 304), so a turned-off link stops serving the photo too.
- **Headers** on every `/s/` response, 404s included: `X-Robots-Tag: noindex, nofollow, noarchive`, `Referrer-Policy: no-referrer` (the token is in the URL; the source link also has `rel="noreferrer"`), `Cache-Control: no-store` (the photo as above); the page repeats robots and referrer as `<meta>`. An unknown, revoked or binned link answers 404 "This link doesn't work" without the app shell and without saying which. Logs record the route pattern, never the token.
- Routes: `POST /recipes/{id}/share` (create), `POST /recipes/{id}/share/revoke`; both answer Datastar with the menu's link entry and a toast, and a form post with a redirect to the menu page. Schema: `recipe_shares` (migration `00011_recipe_shares`): household_id, recipe_id (primary key with household_id; composite FK to recipes, cascade), token (unique), created_by (composite FK to memberships), created_at, updated_at.

### Nutrition

Per-serving nutrition on the recipe page, estimated from the ingredients. An estimate, labelled as one. Decisions D1–D8 at the end of this section are all decided as recommended (user, 2026-11-12).

**Status.** Built: migration `00014_nutrition`, `internal/nutrition` (gram conversion, calculation, per-household values and weights, validation), the lookup through `items.catalog_key` (`catalog.NutritionFor`, `internal/catalog/data/nutrition.json`; a household's values replace the catalogue's, weights merge per unit, Clear goes back to the catalogue), the header tile and popup on the recipe page, the item editor at `/items/{id}/nutrition` (also linked from the grocery entry sheet), the public page block, `groceries.BaseAmount`, i18n and live updates. **Data.** `internal/catalog/data/nutrition.json` holds 129 of the 139 catalogue entries, generated by `go run ./internal/catalog/gen/nutrition <fdc-csv-dir>` from Foundation Foods (2026-04-30) and SR Legacy (2018-04) and the map `internal/catalog/gen/nutrition/nutrition_map.csv` (a source row per key, which portion row gives density or piece weight, hand-set values flagged `hand_edited` with a reason). Carbohydrate is stored without fibre (EU label convention). Unmapped: `frozen_berries`, `vegetable_stock`, `coffee`, `tea` and the six household items. The footnote names USDA as soon as the file has rows. **Not built yet:** the parser change of D5. The sections below describe the full design.

#### Data source

- **USDA FoodData Central, Foundation Foods and SR Legacy.** The API Guide (`fdc.nal.usda.gov/api-guide`, "Licensing") says the data are public domain, published under CC0 1.0, no permission needed; users are *asked* (not required) to list FoodData Central as the source. Suggested wording: "U.S. Department of Agriculture, Agricultural Research Service. FoodData Central, 2019. fdc.nal.usda.gov." No other page (FAQ, downloads, data documentation) restates the licence, and it makes no per-dataset exception. We credit it anyway: `docs/credits.md` holds the full citation (the repo's credit file), and a footnote under the nutrition block and on the public page names it.
- **Sets.** Foundation Foods is current (the 2026-10-29 CSV zip on the download page lacked `food.csv`, `food_nutrient.csv` and `food_portion.csv` when fetched, so the 2026-04-30 release, 3.8 MB zipped, is used) but has only a few hundred foods. SR Legacy is frozen (April 2018, final; CSV 6.7 MB zipped, about 7,800 foods) and covers everyday ingredients; its portion rows (`food_portion`, grams per cup, per piece) are the source for densities and piece weights. Both are used: Foundation row where one exists, else SR Legacy.
- **Rejected.** Open Food Facts: ODbL, derivative databases must be shared alike, and attribution is required. German BLS 4.0: the BLS site says "ohne Lizenzbarrieren" and the FAQ says the data "kann frei genutzt werden", including for app development, but names no licence and gives no redistribution terms, so it stays out until the Max Rubner-Institut confirms in writing (D6). Swiss database: allows reuse with source credit on its home page, but its legal page requires written consent for reproducing any element; the two are not reconciled. Ciqual, Fineli and Frida could not be read (HTTP 503, 403, a script-only shell), so their licences are unverified.
- **Embedded subset, not the runtime API.** The API needs a key, allows 1,000 requests per hour per IP (DEMO_KEY: 30 per hour), and would send household ingredient names to a US government service from a self-hosted app. A subset of about 500 ingredients is a JSON file of roughly 100 KB (about 15 KB gzipped): per catalogue key, FDC ID, 8 nutrients per 100 g, density, piece weights. It lives at `internal/catalog/data/nutrition.json`, keyed by catalogue key (ingredient catalogue decision D9), embedded with the catalogue and loaded into a map at start; no table, since it is global reference data and every table carries `household_id`.
- **Build step, not runtime.** `go run ./internal/catalog/gen/nutrition <fdc-csv-dir>` reads downloaded FDC CSVs (not committed; point it at a folder holding both unzipped downloads) and the hand-kept `internal/catalog/gen/nutrition/nutrition_map.csv` (catalogue key → FDC ID, plus the portion description to take density or piece weight from) and writes `nutrition.json`. A reviewer diffs `nutrition.json`. Keys are append-only and immutable, so rows never move; a retired key keeps its row via `replaced_by`. Every row keeps its `fdc_id` as provenance. Only raw foods are mapped ("rice, white, raw", not cooked), because recipes list raw amounts.
- **German.** Names come from the ingredient catalogue's localized names; keys are language-neutral, so no German dataset is needed. Values for German products differ slightly from USDA ones (flour types, cheeses), which the "estimate" label covers.

#### Keys and lookup

- Nutrition hangs off the catalogue key (`items.catalog_key`, from the ingredient catalogue work: a virtual catalogue, the key is set on first use, unique per household, immutable; existing items that match no entry stay unlinked and so have no nutrition until a member types values or the item is linked). Resolution order for an ingredient's item: household row in `item_nutrition` (custom or overriding) → embedded entry for `items.catalog_key` → none.
- A household row replaces the catalogue's nutrient values as a whole. `item_unit_weights` rows merge per unit with the catalogue's; `density_g_ml` follows the same rule as the nutrient row.
- Items without a catalogue key and without a household row simply have no data; the recipe page names them.

#### From ingredient line to grams

New package `internal/nutrition` (no SQL beyond `item_nutrition`/`item_unit_weights`; pure conversion in `grams.go`). Input is `recipe_ingredients.quantity` + `unit` (canonical keys from `groceries.Units`), never `amount_text`. Conversion factors come from `internal/groceries`, which gains an exported `BaseAmount(Amount) (value float64, kind, ok)` over the existing constants; `toMetric` has no teaspoons or tablespoons today, so the function covers them (`mlPerTsp`, `mlPerTbsp` exist).

| Line | Rule | Example |
|---|---|---|
| `g`, `kg`, `mg`, `oz`, `lb` | mass factor | 200 g spaghetti |
| `ml`, `cl`, `dl`, `l`, `tsp`, `tbsp`, `cup`, `floz` | millilitres × `density_g_ml` of the item; no density → not counted | 2 tbsp olive oil |
| no unit, `piece` | `piece` weight; an item with one weight row of any other unit also counts for a bare number | 4 eggs, 1 onion |
| `clove`, `slice`, `bunch`, `can`, `pack` | that unit's weight row | 2 cloves garlic |
| `pinch` | 0.4 g | 1 pinch salt |
| no `quantity` ("1 thumb", "1 handful", "2-3 cloves", "ca. 200 g", "2 large") | not counted | |
| empty `amount_text` ("salt") | skipped: left out of both sides of the coverage count | |
| `optional` ingredient | left out of the total, mentioned under the block | |

No guessing: a volume line for an item without density is not counted rather than assumed to be water. Conversion is independent of the viewer's metric/US setting; it uses the stored structured amount.

**Prototype result** (full report in the notes folder, `nutrition-coverage-report.md`): 8 hand-written recipes, 64 lines, with a 45-entry table: 55 resolve (86 %), 1 skipped, 8 not counted (5 free-text measures, 2 missing weight or density, 1 item without data). Weakest recipe 73 %. Parser gaps worth fixing as part of this feature (D5): ranges ("2-3 cloves", "2–3 EL") and "ca." currently produce no quantity.

#### Calculation

- Derived at render, not stored. A recipe has about 10 ingredient rows; the work is a map lookup per row plus one query for the household's `item_nutrition` and `item_unit_weights` for those items (the recipe load already reads ingredients). Storing would need invalidation on every item, ingredient and servings edit and a live event per item edit; the saving is microseconds. Revisit only if a list page shows nutrition per card.
- Per serving = Σ (grams × nutrient per 100 g / 100) over counted lines ÷ `recipes.servings`. Servings ≤ 0 shows nothing.
- **Servings scaling does not change per-serving figures** (amounts and servings scale together). The detail page's scaler changes the amounts only; the block says "per serving" and is not recomputed on the client. No total for the whole recipe in v1.
- Rounding: kcal to 5, grams to 1 (0.1 below 10), sodium to 10 mg. Salt is shown as sodium × 2.5 / 1000 in grams, EU-style.
- **Coverage.** `counted` = lines with grams; `of` = non-optional lines with an amount. The block always states it when `counted < of`: "Based on 7 of 9 ingredients". Under 50 % (D3) the numbers are hidden: "Not enough data yet. 3 of 9 ingredients have nutrition." The detail list (disclosure) names each uncounted line with its reason: "no nutrition data", "no weight for 'handful'", "no density for volumes", and links to the item editor where fixable.
- All figures are labelled "estimate"; a footnote under the block says what the estimate rests on (naming USDA once the embedded data ships).

#### Schema (migration `00014_nutrition`, both dialects)

SQLite shown; Postgres: `uuid` ids, `double precision`, `timestamptz`, same names and keys. `TestSchemaParity` covers both.

```sql
CREATE TABLE item_nutrition (
    household_id TEXT NOT NULL,
    item_id      TEXT NOT NULL,
    kcal REAL NOT NULL CHECK (kcal >= 0 AND kcal <= 900),
    protein_g REAL CHECK (protein_g >= 0), fat_g REAL CHECK (fat_g >= 0),
    saturated_fat_g REAL CHECK (saturated_fat_g >= 0), carbs_g REAL CHECK (carbs_g >= 0),
    sugars_g REAL CHECK (sugars_g >= 0), fibre_g REAL CHECK (fibre_g >= 0),
    sodium_mg REAL CHECK (sodium_mg >= 0),
    density_g_ml REAL CHECK (density_g_ml > 0),
    updated_by TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
    PRIMARY KEY (household_id, item_id),
    FOREIGN KEY (household_id, item_id) REFERENCES items (household_id, id) ON DELETE CASCADE,
    FOREIGN KEY (household_id, updated_by) REFERENCES memberships (household_id, user_id) ON DELETE RESTRICT
) STRICT;
CREATE TABLE item_unit_weights (
    household_id TEXT NOT NULL, item_id TEXT NOT NULL,
    unit TEXT NOT NULL CHECK (unit IN ('piece','clove','slice','bunch','can','pack')),
    grams REAL NOT NULL CHECK (grams > 0),
    created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
    PRIMARY KEY (household_id, item_id, unit),
    FOREIGN KEY (household_id, item_id) REFERENCES items (household_id, id) ON DELETE CASCADE
) STRICT;
```

Timestamps bound from Go (`store.T`). `item_id` is the leading key column after `household_id`, so every lookup is indexed. Items are hard-deleted only through the existing item delete, which cascades.

#### UI and manual entry

- **Recipe detail:** a full-width tile in the header, below Yield and Time, shows "Nutrition per serving", the kcal, a small protein/fat/carbs bar and "Details". It is a button (a GET form, so it loads a page without JavaScript) that opens a popup at `/recipes/{id}/nutrition`: a bottom sheet on phones, a centred 480 px dialog from 768 px. The popup has kcal, the macro bar and macros, saturated fat, sugars, fibre and salt, the coverage line "Based on 7 of 9 ingredients", the "What's missing" list with links to the item editor, the USDA footnote and "Done". While it is open the page's `nutOpen` signal is true and the live stream re-renders its body. Shown whenever the recipe has ingredients; with too little counted the tile says "Not enough data" and the popup explains and lists what is missing. Nothing is rendered under the ingredients any more; the public page keeps an inline block.
- **Item editor (edit sheet):** a "Nutrition" section: source line ("From catalogue · USDA", "Custom"), kcal and 7 nutrient fields per 100 g, "1 l weighs … g" (density, entered as g per 100 ml), unit weights ("1 piece = … g", add row for clove, slice, bunch, can, pack). "Reset to catalogue" deletes the household rows when the item has a catalogue key. Validation in `internal/nutrition` returns `FieldErrors` keyed by form field: non-negative, kcal ≤ 900, protein + fat + carbs + fibre ≤ 100 g. Plain form post works without JavaScript like every other form.
- **Entry points and design:** boards "Nutrition · …" on the design canvas (recipe page phone, tablet and desktop in both themes, "not enough data" state, item editor with a validation error, public page). The editor opens from a "Nutrition" row in the existing item edit sheet and from the recipe's "What's missing" rows. Label values are typed as salt in grams (EU packs) and stored as sodium.
- **Public recipe page:** shows the per-serving block when coverage is at least the threshold, with the estimate wording, without the "what's missing" list (D4). It sits under the method in the main column, not under the ingredients, because the ingredients card is sticky on wide screens and a taller sticky column would cut off its bottom.
- **Live:** saving an item's nutrition publishes `recipe.changed` for each recipe using the item, like linked recipes do, so open recipe pages recalculate.
- **i18n keys** (en and de, `i18n.Key` constants): `nutrition.title`, `nutrition.per_serving`, `nutrition.estimate`, `nutrition.kcal`, `nutrition.protein`, `nutrition.fat`, `nutrition.fat_saturated`, `nutrition.carbs`, `nutrition.sugars`, `nutrition.fibre`, `nutrition.salt`, `nutrition.coverage` ("Based on {counted} of {of} ingredients", plural-aware), `nutrition.not_enough`, `nutrition.missing_title`, `nutrition.missing_no_data`, `nutrition.missing_no_weight`, `nutrition.missing_no_density`, `nutrition.optional_left_out`, `nutrition.source_catalogue`, `nutrition.source_custom`, `nutrition.credit`, `nutrition.edit_title`, `nutrition.per_100g`, `nutrition.density`, `nutrition.unit_weight`, `nutrition.add_weight`, `nutrition.reset`, `nutrition.err_range`, `nutrition.err_macros`.
- **Tests:** conversion table tests per unit rule (no DB); calculation with partial coverage and servings; isolation cases for `item_nutrition` and `item_unit_weights` reads and writes in `internal/nutrition/isolation_test.go`; a test that every `nutrition.json` row has a key in the catalogue, an FDC ID, kcal ≤ 900 and macros ≤ 100 g; web tests (`b.post` and `b.datastar`) for the item editor and the recipe block; `go test` on both engines.

#### Out of scope

Daily targets and goals, meal-plan day or week totals, barcode lookup and Open Food Facts, per-recipe manual overrides ("I know this is 450 kcal"), micronutrients, cooked-versus-raw yield factors, allergens and diets, free-text measure weights ("1 handful"), a runtime USDA lookup, nutrition in list cards or filters, nutrition in the REST API (D7).

#### Decisions (all decided as recommended)

- **D1 Data: embedded USDA subset, no runtime API.** Recommend as written above. Alternative: optional API lookup for custom items; costs a key setup and sends names out.
- **D2 Ship nutrition rows for the catalogue only.** Items outside the catalogue get data only by manual entry. Recommend yes; no fuzzy matching of free-text items to USDA names.
- **D3 Coverage threshold: hide numbers under 50 % of counted ingredients.** Recommend 50 %. Alternative: always show with the warning.
- **D4 Public recipe page shows nutrition** when above the threshold, estimate wording and credit, no missing list. Recommend show. Alternatives: hide, or a per-link switch (extra column on `recipe_shares`).
- **D5 Parser: accept ranges ("2-3") and "ca." as a quantity** (a range would need a second quantity column or a midpoint stored as the quantity, so this touches the schema and scaling). Recommend a separate change before nutrition, since scaling and the shopping list benefit too, and the midpoint as the quantity with the typed text kept in `amount_text`. Alternative: leave as free text.
- **D6 German BLS:** ask the Max Rubner-Institut for written confirmation of redistribution, as a background task. Recommend asking; nothing in v1 depends on it.
- **D7 REST API:** leave nutrition out of `docs/api.md` for v1. Recommend out; add `nutrition` to the recipe resource when someone needs it.
- **D8 Optional ingredients:** excluded from the total. Recommend excluded, with a note under the block. Alternative: include them.

### P5 — Later
- KitchenOwl import, "always have" staples. (OIDC is done, see Auth; tablet polish is done.)

### P6 — AI import
- As decided in AI import above. P5 stays later; P6 goes first.
- Routes: `/settings/ai` (admin page; `POST /settings/ai` saves, `POST /settings/ai/test` tests), `/recipes/import` (form; `POST /recipes/import` creates a job and redirects to it), `/imports/{id}` (running, failed or review by status; not under `/recipes/`, where `/recipes/import/{id}` would clash with `/recipes/{id}/edit` and the other recipe routes in Go's router), `POST /imports/{id}/cancel`, `/save`, `/discard`, `/imports/{id}/items?row=` (item picker for a review row; `POST` sets the row's item), `/imports/{id}/photo`, `/recipes/new?import={id}`, `GET /live?page=import&job={id}`.
- Schema (migration `00007_ai_import` on both engines):
  - `ai_settings`: household_id (PK, FK households), enabled, provider (`anthropic`, `openai`, `ollama`), endpoint, model, api_key_enc (nullable), allow_fetch_page, allow_search_items, allow_create_item, tested_at, test_ok (nullable), updated_by (composite FK to memberships), created_at, updated_at. Booleans as columns instead of a `tool_permissions` JSON blob, so both engines check them the same way.
  - `import_jobs`: id, household_id, user_id (composite FK to memberships), source_url, source_text (one of them set), status, steps (JSON text), draft (JSON text, nullable), error_code, error_detail, created_at, updated_at, started_at, finished_at. `UNIQUE (household_id, id)`; index `(household_id, user_id, created_at)` and `(status, created_at)` for the worker. JSON is `TEXT` on both engines; only Go reads it.
- Order and commits, each with tests: settings page and encrypted key; safe fetcher; JSON-LD extractor (with name matching and the no-model import path end to end); provider clients behind the interface; tool loop with permissions; job runner; running/failed/review screens; pasted text; the editor card and library entries.

## Risks

- **R1 Datastar.** v1.0 has shipped; remaining risk is a smaller community than htmx. Usage stays in templ attributes and `internal/live`; htmx + Alpine fallback is a mechanical swap.
- **R2 Search scale.** Search scans a household's rows with `LIKE`; fine for hundreds of recipes, would need trigram indexes for tens of thousands.
- **R3 SSE behind proxies and HTTP/1.1.** Buffering proxies break streaming; HTTP/1.1 caps connections per origin. Mitigations in Live updates and Operations.
- **R4 SQLite write contention.** Single writer with WAL and throttled session writes; fine for a household.
- **R5 Migration drift between dialects.** Two migration directories; caught by the schema-parity check in CI.
