# mise

Self-hosted recipes, meal plan and shopping lists: one Go binary, server-rendered with templ + Datastar, SQLite or Postgres.

- Design, schema and every product decision: [docs/plan.md](docs/plan.md). Read the section for the area you touch before changing behaviour, and update it in the same change when a decision moves.
- REST API and tokens: [docs/api.md](docs/api.md).
- Proxy, backup, SMTP, SSO: [docs/operations.md](docs/operations.md).
- Writing a migration: [internal/store/migrations/README.md](internal/store/migrations/README.md).

## Done means

1. `go tool templ generate` after any `.templ` edit; commit the generated `*_templ.go` (CI fails on stale ones).
2. `golangci-lint run ./...` reports 0 issues.
3. `go test ./...` passes on **both** engines. A `--- SKIP: .../postgres` line means Postgres did not run: set `MISE_TEST_POSTGRES_URL`, or point testcontainers at Docker (with colima: `DOCKER_HOST=unix://$HOME/.colima/default/docker.sock TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=/var/run/docker.sock`).
4. UI changes checked in a browser at phone width (375 px) and desktop.

## Conventions the code won't tell you

- **Two dialects, one schema.** Every migration exists in `sqlite/` and `postgres/` with the same number; `TestSchemaParity` compares them. SQL is written once with `?` placeholders. Bind every timestamp from Go (`store.T`), never `CURRENT_TIMESTAMP`/`now()`.
- **Household scoping.** Every table carries `household_id`; foreign keys are composite `(household_id, id)`. Handlers get a `store.Scoped` from `db.Scope(ctx)`; `ScopeFor`/`System` are lint-restricted to `internal/{jobs,ai,live,auth}`. New scoped reads and writes get a case in the package's `isolation_test.go`.
- **Writes are POST.** Register state-changing routes with `router.write`; Datastar writes use `@post`. CSRF protection only covers non-GET methods.
- **Works without JavaScript.** Every page and form has a plain HTML path (links, form posts with `action` buttons); Datastar enhances it. Web tests exercise both (`b.post` vs `b.datastar`).
- **Live updates.** Domain services publish `live` events after commit; each page's stream re-renders its region per viewer. When a write changes what another page shows, publish that page's event too.
- **Every string is translated.** Use `i18n.T(ctx, key)`; add keys to both `internal/i18n/locales/en.json` and `de.json` (flat keys, same format verbs). `TestKeysUsedExist` type-checks key literals, so pass keys as `i18n.Key` constants and model "no message" as a `bool`, not `""`.
- **Domain owns rules.** `internal/<domain>` holds SQL and validation (returning `FieldErrors` keyed by form field); `internal/web` parses requests and renders; `internal/ui` is templ components and view structs.
- **CSS tokens.** Colours and sizes are custom properties in `internal/ui/static/css/app.css` with light and dark values; phone layout first, `768px` rail and `1200px` sidebar breakpoints; `.only-phone`/`.only-wide` switch at `900px`. Fixed elements above the phone bar offset from `--nav-height`.
- **Tools** are pinned in `mise.toml` (`mise run gen|lint|test|run`). Machine setup lives in a separate dotfiles repo; leave the host environment as it is.
