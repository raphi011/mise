# mise

Self-hosted recipes, meal plan and shared shopping lists. One Go binary, SQLite or Postgres.

Status: early development. See [docs/plan.md](docs/plan.md) for the design and phases.

## Run

```sh
mise run run            # generate templ, serve on :8080 with ./data
```

or with Docker:

```sh
docker build -t mise .
docker run -p 8080:8080 -v mise-data:/data mise
```

The container runs as uid 65532 (distroless `nonroot`). Use a named volume as
above, or a host directory owned by that uid
(`sudo chown 65532:65532 ./data`), for `/data`.

## Configuration

| Variable | Default | |
|---|---|---|
| `MISE_DB` | `sqlite://$MISE_DATA_DIR/mise.db` | or `postgres://user:pass@host/db` |
| `MISE_DATA_DIR` | `./data` | images, SQLite file, generated secrets |
| `MISE_LISTEN` | `:8080` | |
| `MISE_BASE_URL` | | public URL; needed behind a proxy that rewrites `Host` |
| `MISE_TRUSTED_PROXIES` | | CIDRs whose `X-Forwarded-For` is trusted |
| `MISE_LOG_FORMAT` | `json` | `json` or `text` |
| `MISE_LOG_LEVEL` | `info` | |
| `MISE_SMTP_HOST` | | SMTP server; enables invite and password-reset emails |
| `MISE_SMTP_PORT` | `587` | |
| `MISE_SMTP_TLS` | `starttls` | `starttls`, `tls` (implicit, usually port 465) or `none` |
| `MISE_SMTP_USERNAME`, `MISE_SMTP_PASSWORD` | | when the server needs auth |
| `MISE_SMTP_FROM` | | sender, e.g. `mise <mise@example.com>`; required with a host |

Every variable has a matching flag, e.g. `-db`, `-listen`. `mise-server migrate` applies migrations and exits; `/healthz` and `/readyz` are for health checks.

## Operate

`mise-server backup <dir>` archives the SQLite database and the images. See
[docs/operations.md](docs/operations.md) for reverse proxy setup (SSE,
HTTP/2, trusted proxies), backup and restore on both engines, and email.

## Develop

Tools are pinned in `mise.toml`; templ runs via `go tool templ`.

```sh
mise run gen    # templ generate
mise run lint
mise run test
```

Tests run against SQLite and Postgres. Postgres comes from `MISE_TEST_POSTGRES_URL` (an admin URL; each test creates and drops its own database) or a testcontainers instance when Docker is running. Without either, Postgres tests are skipped locally and fail in CI.

Generated `*_templ.go` files are committed; CI checks they are current.

`internal/ui/static/js/datastar.js` is the Datastar v1.0.4 release bundle with
its trailing `sourceMappingURL` comment removed (the source map is not
vendored). Static assets are served under content-hashed names, so replacing
the file changes its URL.
