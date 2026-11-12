# mise

Self-hosted recipes, meal plan and shopping lists: one Go binary, server-rendered with templ + Datastar, SQLite or Postgres.

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
- **Signed history.** The cluster's ArgoCD deploys tags from this repo under a strict GPG policy: every ancestor of a release tag must verify with Raphael's key, and a signed tag or follow-up cannot repair an untrusted ancestor. Never merge with `gh pr merge` or GitHub's merge buttons; GitHub signs those with its own key. After checks pass, merge locally (`git merge --no-ff -S`, or rebase with `git rebase --exec 'git commit --amend --no-edit -S'`) and push. Before tagging, `git log --format='%h %G? %s' <last-tag>..HEAD` must show `G` on every commit.
- **Releases.** Bump `version` and `appVersion` in `charts/mise/Chart.yaml` in a signed commit (`Chart: x.y.z`), push `main`, then push a signed tag `git tag -s vX.Y.Z`. The release workflow publishes the image and chart. Deploying is a separate change: bump `targetRevision` in `argocd/apps/mise/application.yaml` of `~/Git/turingpi-k8s`, following its `AGENTS.md`. Two migrations with the same number break startup, so check `internal/store/migrations/*/` for collisions when merging parallel branches.
- **Tools** are pinned in `mise.toml` (`mise run gen|lint|test|run`). Machine setup lives in a separate dotfiles repo; leave the host environment as it is.
