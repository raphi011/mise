# mise

Self-hosted recipes, meal plan and shared shopping lists for your household. One Go binary, SQLite or Postgres, no JavaScript build.

<p>
  <img src="docs/screenshots/plan-desktop-light.png" alt="The week plan on a desktop" width="640">
  <img src="docs/screenshots/recipes-phone-light.png" alt="The recipe library on a phone" width="170">
</p>

## Features

- **Recipes.** Photos, tags, favourites, search by name or ingredient, "goes well with" links, a bin with undo, public read-only links you can turn off again. Scale servings on the page; amounts show in each viewer's units (metric or US).
- **Import.** From a link (schema.org recipes import without any model) or pasted text. An optional AI model, off by default, handles pages without structured data: Anthropic, any OpenAI-compatible API, or a local Ollama.
- **Meal plan.** A week and a month view, drag meals between days, suggestions for recipes you have not cooked in a while.
- **Shopping lists.** Add a meal's ingredients to a list in one tap, or type "2 lemons". Lists update live on every device, so two people can shop from one.
- **Cook mode.** Full-screen steps with ingredient amounts inline and step timers; the screen stays on.
- **Households.** Invite members by link or email; password sign-in plus optional single sign-on (OpenID Connect).
- **Works everywhere.** Installable on phones, light and dark theme, English and German, usable without JavaScript (Datastar only enhances).

<p>
  <img src="docs/screenshots/recipes-desktop-light.png" alt="The recipe library" width="49%">
  <img src="docs/screenshots/recipe-desktop-light.png" alt="A recipe" width="49%">
  <img src="docs/screenshots/groceries-desktop-light.png" alt="The shopping list" width="49%">
  <img src="docs/screenshots/settings-desktop-light.png" alt="Household settings" width="49%">
</p>
<p>
  <img src="docs/screenshots/recipe-phone-light.png" alt="A recipe on a phone" width="13.5%">
  <img src="docs/screenshots/plan-phone-light.png" alt="The plan on a phone" width="13.5%">
  <img src="docs/screenshots/groceries-phone-light.png" alt="The shopping list on a phone" width="13.5%">
  <img src="docs/screenshots/cook-phone.png" alt="Cook mode on a phone" width="13.5%">
  <img src="docs/screenshots/recipes-phone-dark.png" alt="The library on a phone in dark mode" width="13.5%">
  <img src="docs/screenshots/settings-phone-light.png" alt="Settings on a phone" width="13.5%">
  <img src="docs/screenshots/share-phone.png" alt="A public recipe link opened on a phone" width="13.5%">
</p>

## Quick start

The image is published for `linux/amd64` and `linux/arm64`. The first visit opens a setup page that creates your household and the first account.

```sh
docker run -d --name mise -p 8080:8080 -v mise-data:/data ghcr.io/raphi011/mise:latest
```

Then open <http://localhost:8080>. The container runs as uid 65532 (distroless `nonroot`). Use a named volume as above, or a host directory owned by that uid (`sudo chown 65532:65532 ./data`), for `/data`.

Pin a version (`ghcr.io/raphi011/mise:1.2.3`, or `:1.2` for patch updates) rather than `latest` for anything you care about; `:edge` follows the main branch.

### Docker Compose

With Postgres instead of the default SQLite file:

```yaml
services:
  mise:
    image: ghcr.io/raphi011/mise:latest
    restart: unless-stopped
    ports: ["8080:8080"]
    environment:
      MISE_DB: postgres://mise:change-me@db/mise?sslmode=disable
      MISE_BASE_URL: https://mise.example.com
    volumes: [mise-data:/data] # photos and generated secrets
    depends_on:
      db: { condition: service_healthy }
  db:
    image: postgres:18-alpine
    restart: unless-stopped
    environment: { POSTGRES_USER: mise, POSTGRES_PASSWORD: change-me, POSTGRES_DB: mise }
    volumes: [pg-data:/var/lib/postgresql/data]
    healthcheck:
      test: [CMD, pg_isready, -U, mise]
      interval: 5s
volumes:
  mise-data:
  pg-data:
```

Drop the `db` service and `MISE_DB` to use SQLite in the `mise-data` volume.

### Kubernetes

The Helm chart is published as an OCI artifact. It runs one replica with `Recreate` (live updates are in-process); see [`charts/mise/values.yaml`](charts/mise/values.yaml) for the database, SMTP, single sign-on and volume settings.

```sh
helm install mise oci://ghcr.io/raphi011/charts/mise \
  --set baseURL=https://mise.example.com
```

Add `--version x.y.z` to pin a release; the chart's default image tag is its app version.

## Configuration

| Variable | Default | |
|---|---|---|
| `MISE_DB` | `sqlite://$MISE_DATA_DIR/mise.db` | or `postgres://user:pass@host/db` |
| `MISE_DATA_DIR` | `./data` (`/data` in the image) | images, SQLite file, generated secrets |
| `MISE_LISTEN` | `:8080` | |
| `MISE_BASE_URL` | | public URL, e.g. `https://mise.example.com`; needed behind a proxy that rewrites `Host`. No path: mise runs at the root of its own host |
| `MISE_EXTRA_BASE_URLS` | | comma-separated further public URLs, e.g. a tailnet name; single sign-on works on each (register each callback at the provider) |
| `MISE_TRUSTED_PROXIES` | | CIDRs whose `X-Forwarded-For` is trusted |
| `MISE_SECRET_KEY` | generated into `$MISE_DATA_DIR/secret.key` | base64, 32 bytes; encrypts the AI provider key |
| `MISE_LOG_FORMAT` | `json` | `json` or `text` |
| `MISE_LOG_LEVEL` | `info` | |
| `MISE_SMTP_HOST` | | SMTP server; enables invite and password-reset emails |
| `MISE_SMTP_PORT` | `587` | |
| `MISE_SMTP_TLS` | `starttls` | `starttls`, `tls` (implicit, usually port 465) or `none` |
| `MISE_SMTP_TLS_SKIP_VERIFY` | `false` | accept a self-signed server certificate (trusted-network relays only) |
| `MISE_SMTP_USERNAME`, `MISE_SMTP_PASSWORD` | | when the server needs auth |
| `MISE_SMTP_FROM` | | sender, e.g. `mise <mise@example.com>`; required with a host |
| `MISE_OIDC_ISSUER` | | OpenID Connect issuer URL; enables "Sign in with ..." next to passwords |
| `MISE_OIDC_CLIENT_ID` | | client ID registered at the provider |
| `MISE_OIDC_CLIENT_SECRET`, `MISE_OIDC_CLIENT_SECRET_FILE` | | client secret, or a file holding it; set one |
| `MISE_OIDC_NAME` | `SSO` | provider name on the sign-in button |

Every variable has a matching flag, e.g. `-db`, `-listen`. `mise-server migrate` applies migrations and exits; `/healthz` and `/readyz` are for health checks.

## Operate

`mise-server backup <dir>` archives the SQLite database and the images. [docs/operations.md](docs/operations.md) covers reverse proxy setup (SSE, HTTP/2, trusted proxies), backup and restore on both engines, email and single sign-on.

## Develop

Design, schema and every product decision are in [docs/plan.md](docs/plan.md); contributor conventions are in [AGENTS.md](AGENTS.md). Tools are pinned in `mise.toml`; templ runs via `go tool templ`.

```sh
mise run run    # generate templ, serve on :8080 with ./data
mise run gen    # templ generate
mise run lint
mise run test
```

Tests run against SQLite and Postgres. Postgres comes from `MISE_TEST_POSTGRES_URL` (an admin URL; each test creates and drops its own database) or a testcontainers instance when Docker is running. Without either, Postgres tests are skipped locally and fail in CI.

Generated `*_templ.go` files are committed; CI checks they are current.

`internal/ui/static/js/datastar.js` is the Datastar v1.0.5 release bundle with its trailing `sourceMappingURL` comment removed (the source map is not vendored). Static assets are served under content-hashed names, so replacing the file changes its URL.

### Releases

Pushes to `main` publish `ghcr.io/raphi011/mise:edge`. A `vX.Y.Z` tag publishes the image (`X.Y.Z`, `X.Y`, `latest`) and the chart at version `X.Y.Z` to `oci://ghcr.io/raphi011/charts`, once CI passes.

### Screenshots

The images in `docs/screenshots` are regenerated against a scratch instance: start the server with an empty `MISE_DATA_DIR` and `MISE_LISTEN=127.0.0.1:8090`, run `python3 docs/screenshots/seed.py http://127.0.0.1:8090`, then `node docs/screenshots/shoot.mjs http://127.0.0.1:8090 docs/screenshots` (needs Chrome and ImageMagick; it also shrinks the PNGs). The recipe photos are in `docs/screenshots/photos`, credited in [CREDITS.md](docs/screenshots/CREDITS.md).
