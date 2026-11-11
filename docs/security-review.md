# Security review

Reviewed 2026-11-10 at `4704560`, by reading the code. No dynamic testing; dependencies were not scanned for known vulnerabilities (`govulncheck` is not part of the pinned tools).

Scope: authentication (passwords, sessions, reset, invites, SSO), CSRF, household isolation, recipe import (SSRF, AI), uploads and image serving, templates and Datastar expressions, response headers, server limits.

## Findings

| ID | Severity | Finding | Status |
|----|----------|---------|--------|
| F1 | Medium | Email change needs no re-authentication | Fixed (password required; accounts without one cannot change it) |
| F2 | Low | SSO links by an unverified, self-entered email | Fixed (linking only from a signed-in session) |
| F3 | Low | No Content-Security-Policy; signed-in pages cacheable | Fixed (CSP, `no-store`); HSTS left to the proxy |
| F4 | Accepted | Admin-configured AI endpoint can reach internal addresses | By design |

### F1: Email change needs no re-authentication

`auth.UpdateProfile` saved a new email address without the current password, without verifying it and without telling the old address. Whoever holds a session (stolen cookie, unlocked device) could:

1. set the email to an address they control and use `/forgot-password`; the reset signs every other session out, so the owner loses the account;
2. with SSO configured, set the email to their own provider address and sign in with SSO. `linkByEmail` links their identity to the account, and the link survives a later password change or reset.

**Fix.** Changing the email address requires the current password (rate limited per user like the password change, since it is a password oracle for the session holder). When SMTP is configured, the old address gets a notice naming the new one. Name and colour changes still need no password. An account without a password (SSO only) has nothing to confirm the change with, so its email change is refused with a message on the email field asking an admin to set a password first, and the account page shows it no password field. The same holds for changing the password, and a password sign-in to such an account is refused like a wrong password; no password is ever checked against an empty hash.

### F2: SSO links by an unverified, self-entered email

On a first SSO sign-in, `linkByEmail` links the provider identity to the one active account whose email matches the provider's verified email. The account's email is whatever its owner typed, never verified. A member can set their email to a housemate's provider address before that housemate first uses SSO; the housemate's identity is then linked to the member's account and signs in as the member from then on.

F1's fix stops this from a stolen session but not from a member acting on their own account.

**Fix.** SSO no longer links by email address. An identity is linked only from a signed-in session: "Link" on the account page (a POST, so CSRF-protected) runs the provider flow with the flow cookie bound to a hash of the session ID, and the callback links the identity to that session's account. It refuses the link when the session changed in between, when the identity is already linked to another account, or when the account already has another identity of the provider. An SSO sign-in with an unlinked identity is refused with a hint to sign in with the password and link from the account page. SSO still never creates accounts. The link form also redirects to the provider, which `form-action` allows (F3).

### F3: Missing defence-in-depth headers

`securityHeaders` set `nosniff`, `Referrer-Policy` and `X-Frame-Options` but no CSP, so an injection bug would run with no restriction. Signed-in pages carried no `Cache-Control`, so after signing out on a shared device the back button could show cached pages.

**Fix.** Every response carries `Content-Security-Policy: default-src 'self'; script-src 'self' 'unsafe-eval'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'`. Datastar evaluates expressions with `Function`, hence `'unsafe-eval'`; inline `style` attributes need `'unsafe-inline'` for styles. Browsers apply `form-action` to the redirects that follow a form submission too, and the SSO button is a form post answered with a redirect to the provider, so with OIDC configured `form-action` also lists the origin of the provider's authorization endpoint. The server learns it from discovery, started in the background at startup so a slow provider does not delay serving (and retried at the first sign-in if the provider was down), and assumes the issuer's origin until then. The only other form that leaves the app, "Link" on the account page (F2), redirects to the same endpoint. Responses to signed-in requests send `Cache-Control: no-store` unless the handler sets its own (images, import photos).

HSTS stays with the TLS-terminating proxy (`docs/operations.md`).

### F4: AI endpoint reaches internal addresses

`/settings/ai` lets an admin point the provider at any URL, including the local network (needed for Ollama), and the connection test shows the provider's error text. With one household per server the admin is the operator. Revisit if the UI ever exposes several households.

## Added after the review

- **API tokens** (`docs/api.md`): bearer only, never the cookie; stored hashed; read/write scope; deleted with the member and on password reset; creation and revocation need a browser session. No per-token rate limit: a 256-bit token cannot be guessed, and sign-in limits do not apply to it.

## Checked without findings

- **Sessions:** 256-bit random tokens, stored as hashes; cookie `HttpOnly`, `SameSite=Lax`, `Secure` on https; a new token at every sign-in; removed members' sessions end; live streams re-check the session.
- **Passwords:** argon2id with stored parameters bounded; constant-time compare; a dummy hash for unknown users; sign-in limited per IP, per username and per pair; reset requests answer the same whether or not an account exists; reset and invite tokens are single use, checked before hashing.
- **CSRF:** `http.CrossOriginProtection` on every non-GET; the router refuses a write on a safe method; the OIDC callback is bound to state, nonce and PKCE in a path-scoped cookie; `next` and `back` parameters accept local paths only.
- **Household isolation:** domain queries filter on `household_id` (imports also on `user_id`); composite foreign keys; images are opened from the household's directory by a validated hash.
- **Recipe import:** the dialled address is checked (no DNS rebinding), with special-purpose ranges, NAT64, 6to4 and Teredo blocked; ports 80 and 443 only; redirects re-checked; size and time capped; no proxy. The proxied import photo is served with `CSP: sandbox`. The model prompt treats page text as untrusted and `fetch_page` stays on the source host.
- **Templates:** no raw HTML; values inside Datastar expressions are URL-encoded or JSON-quoted; recipe source links must be http(s); Datastar is vendored.
- **Uploads:** size, pixel and decode-memory limits; re-encoded to JPEG, which strips metadata.
- **Server:** read, header, write and idle timeouts; `X-Forwarded-For` only from `MISE_TRUSTED_PROXIES`.
