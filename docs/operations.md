# Operating mise

Running mise behind a reverse proxy, backing it up and restoring it, and
sending email. The variables are listed in the [README](../README.md#configuration);
every one has a matching flag.

## Reverse proxy

mise speaks plain HTTP. Put a reverse proxy in front for TLS. Two things
matter beyond a plain `proxy_pass`:

- **Live updates use Server-Sent Events** on `GET /live`. The response is a
  stream that stays open as long as the tab does, with a heartbeat every 25
  seconds. The proxy must pass it through unbuffered and must not end it on a
  read timeout. mise sends `X-Accel-Buffering: no` on the stream, which nginx
  honours, but turning buffering off for `/live` explicitly costs nothing.
- **Serve HTTP/2 to browsers.** Over HTTP/1.1 a browser opens at most 6
  connections per origin, and every open tab holds one for its stream. With a
  few tabs open the rest of the app stalls. HTTP/2 multiplexes all of them
  over one connection. The proxy can talk HTTP/1.1 to mise.

### Caddy

Caddy serves HTTP/2 (and HTTP/3) by default, flushes `text/event-stream`
responses immediately and sets `X-Forwarded-For` and `X-Forwarded-Proto`.

```caddyfile
mise.example.com {
	reverse_proxy 127.0.0.1:8080
}
```

### nginx

```nginx
server {
    listen 443 ssl;
    listen [::]:443 ssl;
    http2 on;
    server_name mise.example.com;

    ssl_certificate     /etc/letsencrypt/live/mise.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/mise.example.com/privkey.pem;

    # Recipe photo uploads are up to 15 MB.
    client_max_body_size 16m;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Connection "";
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    location = /live {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Connection "";
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_buffering off;
        proxy_cache off;
        proxy_read_timeout 1h;
    }
}
```

`http2 on;` needs nginx 1.25.1 or newer; older versions take
`listen 443 ssl http2;` instead.

### `MISE_BASE_URL`

The public URL, e.g. `https://mise.example.com`. mise uses it for links in
invite and password-reset emails, to decide whether cookies get the `Secure`
attribute, and as a trusted origin for form posts when the proxy rewrites the
`Host` header. If it is unset at first run, the setup page proposes the
origin the request came in on and stores it; setting the variable overrides
the stored value. Set it explicitly behind a proxy.

### `MISE_TRUSTED_PROXIES`

Comma-separated addresses or CIDRs of the proxies in front of mise, e.g.
`127.0.0.1,::1` or `172.16.0.0/12` for a proxy in a Docker network. Only
requests whose direct peer is in this list have their `X-Forwarded-For`
honoured: the client is the rightmost address that is not itself a trusted
proxy. The client address feeds the sign-in and reset rate limits and the
request log, so without this setting every request behind a proxy looks like
it came from the proxy and all users share one rate limit. Leave it empty
when mise is reachable directly: an untrusted `X-Forwarded-For` lets clients
pick their own address.

## Backup

```sh
mise-server backup /var/backups/mise
```

writes `/var/backups/mise/mise-backup-<UTC time>.tar.gz`, e.g.
`mise-backup-20261109T120000Z.tar.gz`, readable by its owner only (it holds
password hashes). It reads the same configuration as `serve` (`MISE_DB`,
`MISE_DATA_DIR`, flags after the directory) and is safe to run while the
server is up: photos uploaded or removed meanwhile are left out or skipped.
It does not run migrations and does not delete older archives. It fails,
rather than writing an empty archive, when the SQLite database does not
exist or was never migrated, so a wrong `MISE_DB` or `MISE_DATA_DIR` shows
up as a failed cron job.

The archive mirrors the data directory:

```
mise.db           SQLite only: a consistent copy, made with VACUUM INTO
images/...        recipe photos
```

With Docker, run it in the container and keep the archive in the volume, or
mount a second volume for backups:

```sh
docker exec mise /mise-server backup /data/backups
```

A daily cron job that keeps 30 days:

```sh
0 3 * * * mise-server backup /var/backups/mise && find /var/backups/mise -name 'mise-backup-*.tar.gz' -mtime +30 -delete
```

### Postgres

On Postgres the archive holds only the images, and the command logs a
warning saying so. Back up the database with `pg_dump` alongside it:

```sh
pg_dump -Fc -d "$MISE_DB" -f /var/backups/mise/mise-$(date -u +%Y%m%dT%H%M%SZ).dump
mise-server backup /var/backups/mise
```

`pg_dump` must be at least as new as the server.

## Restore

Restore with the same or a newer version of mise; on start it applies any
migrations the backup predates.

### SQLite

1. Stop the server.
2. Move the old data directory aside. Restoring into a fresh directory makes
   sure no stale `mise.db-wal` or `mise.db-shm` from the old database is left
   next to the restored one.
3. Unpack the archive into the new data directory:

   ```sh
   mv /srv/mise/data /srv/mise/data.old
   mkdir /srv/mise/data
   tar -xzf mise-backup-20261109T120000Z.tar.gz -C /srv/mise/data
   ```

   If `MISE_DB` points somewhere other than `$MISE_DATA_DIR/mise.db`, move
   `mise.db` there.
4. Start the server.

With Docker the image has no shell; unpack through a throwaway container and
give the files back to the uid mise runs as:

```sh
docker stop mise
docker run --rm -v mise-data:/data -v "$PWD":/backup alpine \
  sh -c 'rm -rf /data/* && tar -xzf /backup/mise-backup-20261109T120000Z.tar.gz -C /data && chown -R 65532:65532 /data'
docker start mise
```

### Postgres

1. Stop the server.
2. Restore the dump into an empty database (or let `--clean` drop the
   existing objects first):

   ```sh
   pg_restore --clean --if-exists --no-owner -d "$MISE_DB" mise-20261109T120000Z.dump
   ```

3. Unpack the images into the data directory:

   ```sh
   tar -xzf mise-backup-20261109T120000Z.tar.gz -C "$MISE_DATA_DIR" images
   ```

4. Start the server.

## Email

With an SMTP server configured, creating an invite for an email address
also emails the link, and people can reset a forgotten password themselves
from the sign-in page; the link is valid for an hour and works once. Without
one, invites are link-only and an admin resets passwords under
Settings › People.

```sh
MISE_SMTP_HOST=smtp.example.com
MISE_SMTP_PORT=587
MISE_SMTP_TLS=starttls
MISE_SMTP_USERNAME=mise@example.com
MISE_SMTP_PASSWORD=...
MISE_SMTP_FROM='mise <mise@example.com>'
```

`MISE_SMTP_TLS` picks how the connection is secured:

| Mode | Usual port | |
|---|---|---|
| `starttls` | 587 | plain connection upgraded with STARTTLS; mise refuses to send if the server does not offer it |
| `tls` | 465 | TLS from the first byte (implicit TLS, "SMTPS") |
| `none` | 25 | no encryption; only for a relay on the same host or a trusted network |

Username and password are set together or not at all. `MISE_SMTP_FROM` is
required and must be an address the server lets you send from. Email links
are built from `MISE_BASE_URL` (or the URL stored at setup); without either,
no email is sent.
