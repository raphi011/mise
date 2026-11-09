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
| `MISE_TRUSTED_PROXIES` | unset | CIDRs whose `X-Forwarded-For` is trusted for client IP (rate limiting, logs) |
| `MISE_SECRET_KEY` | generated into `$MISE_DATA_DIR/secret.key` | AES-256-GCM key for stored API keys (base64, 32 bytes) |
| `MISE_SMTP_HOST` | unset | optional; enables invite and password-reset emails. Without it invites are link-only and admins reset passwords |
| `MISE_SMTP_PORT` | `587` | |
| `MISE_SMTP_TLS` | `starttls` | `starttls`, `tls` (implicit, usually port 465) or `none` |
| `MISE_SMTP_USERNAME`, `MISE_SMTP_PASSWORD` | unset | auth, when the server needs it |
| `MISE_SMTP_FROM` | required with host | sender address, e.g. `mise <mise@example.com>` |

## Architecture

```
cmd/mise-server/          main: serve, migrate, backup subcommands
internal/config/
internal/store/           DB pools, dialect, rebind, time codec, tx helper, Scoped / System access
internal/store/migrations/{sqlite,postgres}/
internal/tenant/          household + user in request context
internal/auth/            sessions, passwords, invites, reset tokens, middleware, rate limit
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
- Repositories take a `store.Scoped` value built from the request context. Code without a request (sweeps, AI jobs, SSE renders) uses `store.ScopeFor(householdID)`; global tables (users, sessions, invites by token, households) go through `store.System()`. Both are greppable, and a custom lint check restricts them to `internal/jobs`, `internal/ai`, `internal/live` and `internal/auth`.
- Every index used for lookups starts with `household_id`.
- A test walks every scoped repository method against a two-household fixture and asserts no rows of the other household come back.
- First run: setup page creates the household and the first admin. With one household, the household picker never renders.

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
| users | id, username (unique), name, email, password_hash, avatar_color, theme, language |
| memberships | household_id, user_id, role (admin, member), removed_at |
| sessions | id (hash of token), user_id, household_id, expires_at, user_agent, last_seen_at |
| invites | id, token_hash, email, role, invited_by, expires_at (7 days), used_at |
| password_resets | id, token_hash, user_id, expires_at (1 hour), used_at; global like sessions (no household_id) |
| categories | id, name, name_key, color, position; "Uncategorised" is implicit (NULL) and sorts last |
| items | id, name, name_key, category_id, purchase_count, last_purchased_at |
| shopping_lists | id, name, position (first = default), deleted_at (Undo for list delete). First-run setup creates the default list and categories in the household's language, in its transaction; startup backfills households created before P1b, so every GET stays read-only. |
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

Derived, not stored: "on a list" per ingredient, times cooked (count of `cook_events`), "not cooked in a month", suggestion ranking.

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
- SSO: later (OIDC via `coreos/go-oidc`).
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
- Invites: copyable link (always), email if SMTP is configured; token stored hashed; role chosen at invite time.
- Reset: admin reset under Settings › People (P1a); with SMTP, self-service by email (P4): the link is valid 1 h and single-use.
- Roles enforced in middleware for admin routes (people, AI settings).

### Images

- Upload via form, max 15 MB. Before decoding, `image.DecodeConfig` rejects images over 40 megapixels.
- Accepted: JPEG, PNG, WebP (sniffed from bytes, not the extension). HEIC/HEIF is detected and rejected with a clear message ("Convert to JPEG or pick from Photos"). The file input lists `image/jpeg` first, which makes iOS convert when picking from Photos; picks from Files and macOS uploads can still send HEIC.
- EXIF orientation is read and applied before re-encoding (which strips all metadata).
- Output: original capped at 2400 px long edge and a 600 px thumbnail, both JPEG.
- Path: `$MISE_DATA_DIR/images/<household_id>/<sha256>-<size>.jpg`, content-addressed.
- Served through an authenticated handler that checks household ownership; `Cache-Control: private, max-age=31536000, immutable`.
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
  2. JSON-LD `Recipe` (also inside `@graph`, and `HowToSection`/`HowToStep` instructions): name, description, prep/cook time (ISO 8601 durations), yield → servings, ingredients, steps, keywords/category → tags, image. Ingredients go through the amount parser and match items by `name_key` like the editor (exact first, then the item search's ranking); no match → "New item". No model call, AI on or off.
  3. No JSON-LD: with AI on, the page's text (scripts, styles, nav stripped; capped at 50 KB) goes to the tool loop; with AI off the job fails with "No recipe data on that page" and offers "Type it myself".
- **Pasted text** (AI on only): the text (max 50 KB) goes straight to the tool loop.
- **Tool loop** with these tools, each gated by a switch:

  | Switch (settings label) | Tool | Default | Effect |
  |---|---|---|---|
  | Read recipe web pages | `fetch_page(url)` | on | fetches another page **on the source page's host** (e.g. a print view) through the safe fetcher; returns text plus any JSON-LD |
  | Look up your grocery items | `search_items(query)` | on | up to 10 items `{id, name, category}`, household-scoped |
  | Create new grocery items | `create_item(name, category)` | off | records a proposal on the job, returns a `new:N` reference; nothing is written until the user saves |
  | Create recipes | `create_recipe(...)` | always on, not a switch | submits the draft and ends the loop |

  The model never writes to the database: every write happens when the user saves the review. It cannot update or delete anything. With `search_items` off, the server matches ingredients by name after the loop; with `create_item` off, unmatched ingredients show as "New item" for the user to confirm. Fetched pages and pasted text are untrusted input: they go into user-turn content, never into the system prompt, and the system prompt says to ignore instructions found in them.
- `create_recipe` input (strict schema): name, description, prep_min, cook_min, servings, source_url, ingredients `[{text, item}]` (`text` as written, e.g. "2 tbsp soy sauce", parsed by the amount parser; `item` an item ID, `new:N` or empty), steps `[{text, timer_seconds}]`, tags (names; only existing household tags are kept), image_url. The draft keeps the source language.
- Limits: 12 model turns, 3 minutes per job, 16k max output tokens per turn, tool results capped at 50 KB.
- **Providers** behind `ai.Provider`: `Chat(ctx, Request) (Response, error)` with a provider-neutral request (system, messages, tool definitions with JSON Schema) and response (text, tool calls, stop reason, token usage), plus `Test(ctx)` (one tiny request). Implementations: `anthropic-sdk-go/v2` (Messages API, adaptive thinking, default effort; `tool_choice: auto` with `strict` tools since forced tool use is rejected on current models; server-side refusal fallback `fallbacks: "default"` with the `server-side-fallback-2026-07-01` beta; a refusal fails the job) and `openai-go` (chat completions with tools, for OpenAI-compatible endpoints and Ollama). Tests use a scripted fake provider; the two clients are tested against `httptest` servers replaying recorded responses.
- **Jobs:** `import_jobs` rows are the queue. A pool of 2 workers per process takes queued jobs oldest first, with `store.ScopeFor(household)`; at most 3 queued or running jobs per user (a fourth is refused with a message). Each step (`{kind, text, tool, state}`) is appended to `steps` and published as `import.changed{job_id}` to the user. Cancel cancels the job's context (or marks a queued job canceled). On start, jobs left queued or running by a previous process are marked failed ("The server restarted. Try again."). Ready and failed jobs are swept 14 days after they finish; saved, discarded and canceled jobs are deleted right away.
- **Statuses:** queued, running, ready, failed, canceled. Failures store an error code (`no_recipe`, `fetch_failed`, `blocked_address`, `too_large`, `provider_error`, `refused`, `limit`, `interrupted`) shown as translated copy; provider errors also show the provider's message under "See details".
- **Running screen** (`/recipes/import/{id}` while queued or running): source, live step list and progress bar from `GET /live?page=import&job={id}`, "You can leave this screen. The draft will be waiting under Recipes.", Cancel. Without JavaScript the page refreshes itself every 3 s (`<meta http-equiv="refresh">`) until the job finishes.
- **Failed screen** (same URL): the error, then the import form with "Pasted text" selected (when AI is on) and the link kept, "Type it myself" (the empty editor) and "Import text".
- **Review screen** (same URL when ready): the draft card (photo, name, times, servings, steps count, tags), the assistant's steps (collapsed on phones), and the ingredient table "On the page / Your item / Status" with Matched, Check (model match whose name differs from the page's, or a JSON-LD match that was not exact) and New item. Each row's item is changeable ("Choose another item": the item search, or a new item). Actions: "Save recipe" (with "& N new items" when any), "Open in editor", "Discard". Save creates the new items, the recipe with ingredients, steps, tags and photo in one transaction, deletes the job and goes to the recipe. "Open in editor" opens `/recipes/new?import={id}` prefilled with the draft (ingredients as typed lines); the editor's save deletes the job.
- **Photo:** downloaded through the safe fetcher only on save (review save or editor save), then through the image pipeline; a failed download saves the recipe without a photo and says so in the toast. Review shows the remote photo through a server-side proxy (`/recipes/import/{id}/photo`) that uses the safe fetcher, never as a direct `<img src>` to the source host.
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

### P5 — Later
- OIDC, KitchenOwl import, "always have" staples, tablet polish.

### P6 — AI import
- As decided in AI import above. P5 stays later; P6 goes first.
- Routes: `/settings/ai` (admin page; `POST /settings/ai` saves, `POST /settings/ai/test` tests), `/recipes/import` (form; `POST /recipes/import` creates a job and redirects to it), `/recipes/import/{id}` (running, failed or review by status), `POST /recipes/import/{id}/cancel`, `/save`, `/discard`, `/recipes/import/{id}/items?row=` (item picker for a review row), `/recipes/import/{id}/photo`, `/recipes/new?import={id}`, `GET /live?page=import&job={id}`.
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
