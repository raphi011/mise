# MCP server

mise is an [MCP](https://modelcontextprotocol.io) server at `/mcp` (streamable HTTP, stateless), so AI apps can read and change the household's recipes, plan and lists. The address is shown under **Settings › Account & security › Apps and API tokens**.

## Connecting

**Apps with OAuth** (Claude, ChatGPT, Cursor, MCP Inspector): add `https://mise.example.com/mcp` as a custom connector. The app registers itself, opens mise's consent page, and the member picks **Read** or **Read and write** for their active household. Approved apps are listed on the same settings page, where they can be disconnected.

**Apps with a header**: send an [API token](api.md#tokens) instead.

```sh
claude mcp add --transport http mise https://mise.example.com/mcp --header "Authorization: Bearer $MISE_TOKEN"
```

OAuth needs the public base URL (`MISE_BASE_URL` or the one entered at setup) to be the address apps use; it is the issuer and the resource the tokens are for.

## Tools

Read: `whoami` (member, household, scope, today's date), `search_recipes`, `get_recipe`, `get_plan`, `list_shopping_lists`, `get_shopping_list`.

Read and write also: `create_recipe`, `update_recipe`, `delete_recipe`, `set_recipe_photo`, `remove_recipe_photo`, `plan_meal`, `remove_meal`, `add_to_shopping_list`, `check_entry`, `uncheck_entry`, `remove_entry`.

They do what the matching [REST endpoints](api.md#endpoints) do. Ingredients are lines like `"400 g spaghetti"`; `get_shopping_list` and `add_to_shopping_list` default to the household's first list.

`set_recipe_photo` takes the image as `image_base64` (bare or a `data:` URL) or as `image_url`, an https link mise downloads: only from public addresses on ports 443 or 80 (checked on every connection, redirects included), JPEG, PNG or WebP, up to 15 MB within 15 seconds. An MCP request carries at most 1 MiB, so base64 photos stay under about 750 KB; send larger ones by URL or through the REST endpoint.

## OAuth details

mise is its own OAuth 2.1 authorization server, advertised through the protected-resource metadata in the `401` from `/mcp`:

| | |
|---|---|
| `GET /.well-known/oauth-protected-resource/mcp` | Resource metadata (RFC 9728) |
| `GET /.well-known/oauth-authorization-server` | Server metadata (RFC 8414) |
| `POST /oauth/register` | Dynamic client registration (RFC 7591); public clients only; 10 per IP at once, then one a minute |
| `GET /oauth/authorize` | Consent page; `response_type=code` with PKCE `S256` required |
| `POST /oauth/token` | `authorization_code` and `refresh_token` grants |
| `POST /oauth/revoke` | Token revocation (RFC 7009) |

- Scopes are `read` and `write`; the member's choice on the consent page wins over what the app asked for.
- Redirect URIs are https, http on a loopback address (any port matches), or an app scheme like `cursor://`. A client with one redirect URI may leave `redirect_uri` out of the authorization request, and then out of the token request too.
- Codes last 10 minutes and work once. Access tokens (`mise_at_...`) last an hour and also open the REST API; refresh tokens (`mise_rt_...`) rotate on every use and expire after 90 days unused. Replaying a used code, or the refresh token that was just replaced, ends the grant.
- Like API tokens, grants end when the member is removed, leaves the household, or has their password reset. Clients nobody approves are deleted after a day, and clients without grants 90 days after their last approval.
