# Networking & Security

> Folio's authentication model, the OPDS auth path, and how to deploy it safely.
>
> **Key point:** Folio authenticates **only the OPDS catalog** at the application
> layer. The web UI, REST API and `/mcp` endpoint have **no built-in authentication** — you must
> place an authenticator in front of them. Cloudflare Access is used below as one
> worked example; any reverse-proxy SSO, forward-auth, or network-level control
> works just as well.

---

## The Security Model in One Table

| Surface | Paths | Auth provided by Folio | What you must add |
| :--- | :--- | :--- | :--- |
| **Web UI + REST API** | `/`, `/api/*`, SPA routes | **None.** Unauthenticated. | **Mandatory** external authenticator (reverse-proxy SSO, forward-auth, VPN/LAN isolation, …). |
| **OPDS catalog** | `/opds/*` (except cover) | **HTTP Basic Auth** (bcrypt, configured at runtime). | Nothing required; works for mobile reading apps that can't do browser SSO. |
| **OPDS cover** | `/opds/books/{id}/cover`, `/opds/books/{id}/cover/thumbnail` | None (intentionally public). | Nothing — covers are low-sensitivity and many readers fetch them without forwarding credentials. |
| **MCP endpoint** | `/mcp` | **None.** Unauthenticated. | **Mandatory** external authenticator that speaks OAuth for MCP clients (e.g. Cloudflare Access). See [MCP endpoint](#mcp-endpoint-mcp-and-claudeai). |

The rest of this document explains each row and the cross-cutting hardening
(CSRF guard, header handling, outbound connections).

> ⚠️ **Direct/LAN exposure exposes an unauthenticated admin API.** With nothing in
> front, `/api` lets anyone who can reach the port call `PUT /api/settings`
> (which sets the OPDS credentials), `POST /api/libraries/{id}/purge`,
> `POST /api/sync`, etc. The OPDS Basic Auth on `/opds*` does **not** protect
> `/api`. Treat an external authenticator on `/api` as **required**, not optional.

---

## Why split OPDS off from everything else?

Mobile reading apps (Moon+ Reader, KyBook, KOReader, …) connect to the OPDS feed
directly and **cannot perform browser-based SSO flows** — they have no way to
complete an interactive identity-provider redirect. So OPDS needs an
authentication scheme those clients *can* speak: HTTP Basic Auth, which Folio
implements itself.

The web UI and API, by contrast, are browser surfaces where a real SSO/proxy
authenticator is the right tool. Folio deliberately does **not** reimplement
session management, identity providers, or user databases — that is the job of
the layer in front.

This leads to a two-tier deployment: route `/opds*` straight to Folio (it
authenticates itself), and put everything else behind your authenticator.

```
                         ┌──► /opds*            ──► Folio (HTTP Basic Auth)
[ Public Traffic ] ──────┤
                         └──► everything else   ──► [ Authenticator ] ──► Folio
                                                     (SSO / forward-auth / …)
```

---

## OPDS Basic Auth & the Cover Exception

Folio enforces its own authentication for OPDS feeds and book downloads using
`auth.Authenticator.Middleware` (`internal/auth/auth.go`), injected into the OPDS
handler as the `opds.Authenticator` interface. The cover endpoints are routed
separately and left unauthenticated to accommodate reading-client limitations.

```go
func (h *Handler) Register(r chi.Router) {
    // 1. Unauthenticated cover endpoints (supports reader image-loader limitations)
    r.Get("/books/{id}/cover", h.serveCover)
    r.Get("/books/{id}/cover/thumbnail", h.serveThumbnail)

    // 2. Protected catalog endpoints
    r.Group(func(pr chi.Router) {
        pr.Use(h.authn.Middleware) // auth.Authenticator (bcrypt; timing-equalized + success-cached, see below)
        pr.Get("/", h.root)
        pr.Get("/authors", h.authors)
        pr.Get("/series", h.series)
        pr.Get("/genres", h.genres)
        pr.Get("/opensearch.xml", h.openSearch)
        pr.Get("/search", h.search)
        pr.Get("/books/{id}/files/{fileID}", h.downloadBook)
    })
}
```

- **Credentials:** Stored in the `settings` table (password hashed) and configured
  solely through the admin API (`PUT /api/settings`). There is **no**
  environment-variable seed (`OPDS_USER`/`OPDS_PASS` do not exist).
  - **No-credentials behavior:** If no credentials are configured, the middleware
    **rejects every protected route with `401`** (the unconfigured branch returns
    a bare `401` without a `WWW-Authenticate` header). The catalog is therefore
    **closed, not open** — OPDS won't serve feeds or downloads until a credential
    is set. A **severe startup security warning** (`WarnIfUnprotected`) flags the
    empty state, but it does *not* fall through to unauthenticated serving. The
    only always-public routes are the cover and cover-thumbnail endpoints.
- **Verification (`verifyCredentials`):** the username is compared in constant
  time, and a username mismatch still runs a full bcrypt compare against a
  process-local dummy hash — so a wrong username costs the same ~100ms as a wrong
  password and response timing can't be used as a username oracle (both the dummy
  and all stored hashes use `bcrypt.DefaultCost`).
- **Success cache:** reading apps fetch feeds, cover metadata, and files in
  bursts, and bcrypt costs ~100ms per verify by design. The last *successful*
  `(user, password, stored-hash)` triple is therefore cached as a single SHA-256
  key (compared in constant time); repeat requests skip bcrypt entirely.
  Embedding the stored hash in the key invalidates the cache implicitly on
  credential rotation, and `SetCredentials` (called by `PUT /api/settings`) also
  clears it explicitly via the Authenticator's internal `invalidate`, together
  with the credential cache.
- **Basic Auth Realm:** The realm string `"OPDS Library Manager"` is sent in the
  `WWW-Authenticate` header.
- **Cover authentication exception:** Many popular reading apps (e.g. Moon+
  Reader, KyBook) use separate native image-loading frameworks to pull cover
  images asynchronously. These sub-frameworks do not forward the HTTP Basic Auth
  credentials supplied when registering the OPDS feed. If the cover endpoint
  required auth, covers would fail to load (broken-image icons). Excluding
  `/opds/books/{id}/cover` (and its `/thumbnail` variant, which feeds advertise
  as the OPDS thumbnail link) from Basic Auth lets clients render covers cleanly;
  since covers are not highly sensitive metadata, exposing them is an acceptable
  trade-off for feed usability.

---

## Web UI / API: bring your own authenticator

Folio applies **no** application-level auth middleware to `/api/*` or SPA routes.
Authentication is delegated entirely to the layer in front of Folio. Common
options, any of which is sufficient:

- **Reverse-proxy SSO / forward-auth** — Authelia, Authentik, oauth2-proxy,
  Pomerium, Cloudflare Access, Tailscale Funnel + tsnet, etc. The proxy
  authenticates the user and only then forwards the request to Folio.
- **Reverse-proxy Basic Auth** — nginx/Caddy/Traefik `basic_auth` on everything
  except `/opds*`. Low-tech but effective for a single user.
- **Network isolation** — bind Folio to localhost or a private interface and
  reach it over a VPN (WireGuard, Tailscale). No public ingress at all.

Whatever you choose, the contract is the same: **no unauthenticated request
should ever reach `/api` or the SPA.** Folio assumes this and does not
second-guess it.

### Cross-Site Request Protection (CSRF)

The REST API issues no CSRF tokens — and it doesn't need to, but **not for the
reason it's tempting to assume.** Folio sets no session cookie of its own, so the
shorthand "auth isn't cookie-based, therefore no CSRF" gets cited. That shorthand
is wrong whenever a **cookie-based authenticator** sits in front (most SSO
proxies, including Cloudflare Access, set an auth cookie):

- A cookie-based authenticator sets an auth cookie scoped to the app hostname.
  That cookie is exactly the ambient credential a CSRF attack relies on: the
  browser attaches it to cross-site requests automatically.
- Nothing in Folio enforces "same-origin" by default. There is **no CORS
  configuration and no `Origin` check** in the request path by default; CORS
  would not prevent a cross-origin request from being *sent*, only from being
  *read*.

So, absent any app-level control, the only thing standing between a logged-in
user and a forged state-changing request would be the auth cookie's `SameSite`
attribute plus browsers preflighting `PUT`/`DELETE` — an **implicit dependency on
the proxy's cookie policy and on browser defaults**, not something Folio controls.

Folio therefore enforces its own **token-less, origin-based CSRF guard** on
`/api`, independent of any cookie policy and of whether a proxy sits in front.
Two middlewares are mounted on the `/api` group in `internal/server/`
(`server.go` → `middleware.go`), so every current and future handler is covered:

| Middleware | What it does |
| :--- | :--- |
| `sameSiteGuard(publicURL)` | On state-changing methods (`POST`/`PUT`/`PATCH`/`DELETE`), trusts `Sec-Fetch-Site` when present — allows `same-origin`/`same-site`/`none` (user-initiated: typed URL, bookmark), rejects `cross-site` **and any unrecognized value** with `403`. When `Sec-Fetch-Site` is absent, falls back to `Origin`: a present `Origin` must equal the configured `PUBLIC_URL` origin or the request's own scheme+host; an absent `Origin` is treated as a non-browser client and allowed. |
| `formBodyGuard` | Defense-in-depth for the narrow gap where a request carries neither `Sec-Fetch-Site` nor `Origin`: rejects state-changing requests whose `Content-Type` is one of the three CORS "simple request" form types (`application/x-www-form-urlencoded`, `multipart/form-data`, `text/plain`) with `415`. This closes the trick of smuggling a JSON payload under a form content type. Bodyless writes (e.g. `POST .../sync`), `application/json`, and raw image uploads (`PUT .../cover`) are unaffected. |

**Scope and limits — read before relying on this:**

- **It is a CSRF guard, not authentication.** Non-browser clients (curl, scripts,
  other servers) send neither `Sec-Fetch-Site` nor `Origin` and pass straight
  through, by design — otherwise the OPDS/CLI/automation paths would break.
- **It does not protect a direct deployment.** With no authenticator in front,
  `/api` has **no authentication at all** (see the warning at the top). An
  external authenticator on `/api` is **mandatory**; CSRF is a footnote next to
  that.
- Because the guards key off `PUBLIC_URL` for the `Origin` fallback, set
  `PUBLIC_URL` on any reverse-proxied/tunneled deployment (it is also recommended
  for the OpenSearch canonical host, below).

---

## Header Handling

| Header | Source | Usage |
| :--- | :--- | :--- |
| `X-Forwarded-Proto` | Reverse proxy | `proxyHeaders` middleware sets `request.URL.Scheme` (used as the scheme fallback when building the absolute OPDS OpenSearch URL). Only the literal values `http`/`https` are honored — on direct connections the header is client-controllable, so anything else falls back to the TLS-derived scheme. |
| `X-Forwarded-Host` | — | **Not trusted.** Deliberately ignored — see below. |

> The request logger (chi's `middleware.Logger`, dev-only) records
> `r.RemoteAddr` as-is; there is no `middleware.RealIP` / `CF-Connecting-IP`
> parsing in the current code.

### Canonical host: `PUBLIC_URL` (not `X-Forwarded-Host`)

Because `/opds*` is reachable directly, any caller can set `X-Forwarded-Host`. If
Folio reflected it into the absolute URLs it advertises (the OPDS OpenSearch
`template`), a caller could poison those URLs. So `proxyHeaders` **does not**
honor `X-Forwarded-Host`.

The canonical external base URL is configured instead, via the optional
`PUBLIC_URL` env var (e.g. `https://folio.example.com`):

- **Set** (recommended for any reverse-proxied / tunneled deployment) —
  `PUBLIC_URL` is authoritative for the OpenSearch template, immune to header
  forgery.
- **Unset** (local / direct access) — Folio falls back to the request `Host`
  plus the `X-Forwarded-Proto` scheme. Safe there because no untrusted proxy
  sits in front.

---

## Outbound Connections

Historically Folio made **no outbound network calls** — every catalog source is a
local, read-only file or SQLite DB. It now has three kinds of outbound
connection, all optional and best-effort: Google Books (metadata enrichment),
the user-initiated cover search and cover-from-URL actions, and Telegram (owner
notifications, off by default).

### Google Books

HTTPS requests to `www.googleapis.com/books/v1` for on-view metadata enrichment
and the Fix Match feature (`internal/googlebooks`, stdlib `net/http`).

- **What's sent:** a book's ISBN, or its title + first author, and the chosen
  volume id on Fix Match. No catalog contents or credentials.
- **When:** lazily, at most once per book (guarded by `enrichment_checked`), plus
  explicit user-initiated Fix Match searches. **The trigger is "no annotation"
  (`needsEnrichment`), not "is a PDF":** *every* annotation-less book makes one
  outbound lookup on its first detail view — including a well-catalogued EPUB that
  merely lacks a `<description>`. Plan for this egress breadth, not just PDFs.
- **Auth/quota:** the optional `GOOGLE_KEY` is sent as the `key` query param; an
  empty key uses Google's anonymous quota.
- **Failure mode:** best-effort. The network spend (lookup **and** cover fetch)
  is bounded by a **5s context timeout** (`enrichTimeout`) on the user-facing
  detail path, so a slow Google response can't hang the view; the 8s per-HTTP
  client timeout is a secondary bound. Persistence then runs on its **own 3s
  budget** (`persistTimeout`, on a context detached from the request), so a
  Google answer that arrives near the deadline — or a client disconnect — can't
  roll back a commit whose data would just be re-fetched on the next view. A
  failed or slow lookup leaves the book with the metadata it already had and
  never blocks catalog browsing. Transport failures and failed Fix-Match
  searches are logged at **Warn**, not Error: an online miss is expected and
  must not count toward the Telegram error summary.
- **Rate limiting (429):** a `429 Too Many Requests` from Google trips a
  process-level **cooldown** (`rateLimitCooldown`, 5 min) during which further
  enrichment/Fix-Match requests short-circuit to `ErrRateLimited` *without* an
  HTTP call. The affected books stay un-enriched (not negatively cached), so they
  are retried once the window passes — a quota wall never amplifies into per-view
  hammering.

A deployment that must stay fully offline can block egress to `googleapis.com` at
the network layer.

### Cover search and cover from URL

Only on an explicit user action in the book detail's cover picker:

- **Cover search** (`GET /api/books/{id}/cover/search`, `internal/metasearch`)
  queries Amazon (`www.amazon.com`), Goodreads (`www.goodreads.com`), Open
  Library (`openlibrary.org`) and Google Books for cover candidates, sending the
  book's title, first author and ISBN (a typed query replaces the title and
  drops the author). Each provider has an 8s per-request timeout; a failing
  provider is skipped.
- **Set cover from URL** (`POST /api/books/{id}/cover` with `{"url":…}`) makes
  the server download that image (a picked candidate or a URL the user typed).
  Only `http`/`https` URLs are fetched, the body is size-capped, and hosts that
  resolve to private, loopback or link-local addresses are refused (SSRF guard,
  `internal/api/edit.go`).

### Telegram (owner notifications)

HTTPS requests to `api.telegram.org` (`internal/notify`, stdlib `net/http`) that
deliver messages to the Telegram channel the owner configured. **Opt-in and off
by default:** nothing is sent until the owner saves a bot token and a channel ID
and enables notifications (`/api/settings/telegram`).

- **What's sent:** a message prefixed with the instance identity (so several
  instances can share one channel) and its text. The kinds are: the test message
  sent from the settings screen; recovered crashes; a library run that failed
  and the later run that recovered; newly unreadable book files; a sync that
  removed many books; and an error summary. A message carries library names, run kinds, counts,
  error or panic texts (each capped at 300 characters; they may include file
  paths inside a library), one code frame for a crash, book IDs or links to
  the instance, and short reason labels.
- **Error summary:** Error-level log records not already reported as a crash
  or a failed run are grouped into one `<N> error(s) in the last 5 minutes`
  message per window, listing at most 5 entries. An entry is the record's time,
  its log message and its `error` text (as is, capped at 300 characters, so it
  may include file paths inside a library); no other log attribute is used. A
  completed run that left books for retry, or whose description fill after a
  sync failed, adds one line naming the library. The summary never carries book
  titles, book contents, credentials, or the bot token. An empty window sends
  nothing; an open window is flushed on shutdown.
- **Never sent:** book titles, authors or descriptions taken from catalog
  metadata, book contents, per-book read detail, raw request paths or queries,
  credentials, or the bot token.
- **Content rule** (constitution v1.1.0, Principle III exception): a message may
  carry library names, error texts (including file paths inside a library), and
  book IDs or links to the instance. It never carries book titles taken from
  catalog metadata, book contents, credentials, tokens, or request headers; a
  title that is part of a file name inside an allowed file path is permitted.
- **Failure mode:** best-effort. Each request has a 10s HTTP timeout; a
  delivery makes up to 3 attempts within a 30s budget. Messages go through a
  bounded in-memory queue, so producers never block or do I/O, and when the
  queue is full the excess is dropped. A delivery failure is logged as a
  warning and never breaks a user-facing request.
- **Blocking it:** leave notifications disabled (the default), or block egress to
  `api.telegram.org` at the network layer.

---

## Example: Cloudflare Access

The author runs Folio behind a Cloudflare Tunnel with Cloudflare Access as the
authenticator. This is **one concrete way** to satisfy the "bring your own
authenticator" requirement above — not a dependency of the project. Any
equivalent SSO/forward-auth proxy maps onto the same two policies.

All public traffic routes through a Cloudflare Tunnel to the Folio container,
with two Access policies:

```
                                  ┌──► [ Policy 1: Require Auth ] ──► MFA/SSO, then app
[ Public Traffic ] ──► [ CF ] ────┤
                                  └──► [ Policy 2: Bypass /opds* ] ──► Direct to app
                                                                        (HTTP Basic Auth)
```

| Policy | Scope | Action | Effect |
| :--- | :--- | :--- | :--- |
| **1 — Default** | `*` (all paths) | Require authentication (SSO/MFA) | The web UI and `/api/*` are reachable only after the identity provider authenticates the user. |
| **2 — OPDS Bypass** | `/opds*` | Bypass | Reading apps that can't do browser SSO reach OPDS directly; Folio's own Basic Auth protects it. |

After SSO, Cloudflare Access sets a `CF_Authorization` JWT cookie scoped to the
app hostname and injects identity via headers (`CF-Access-JWT-Assertion`). Because
that cookie is the ambient credential, Folio's origin-based CSRF guard (above)
matters even here — it does not rely on Cloudflare's cookie `SameSite` policy.

To replicate this with another proxy: protect everything by default, exempt
`/opds*` from the proxy's own auth, and set `PUBLIC_URL` to your external origin.

---

## MCP endpoint (`/mcp`) and Claude.ai

`/mcp` serves a read-only Model Context Protocol server (see
[API.md](./API.md#mcp-endpoint-mcp)) so Claude.ai can search the catalog. Folio
performs **no authentication** on it, so an external authenticator is
**mandatory**. **Never expose `/mcp` publicly without one**: anyone who can reach
it can search and read every book's metadata.

It is deliberately outside the `/api` CSRF guard (`sameSiteGuard` and
`formBodyGuard` are not mounted on it): Claude.ai posts JSON-RPC from its own
servers, with no `Origin` or a foreign one, and authentication is external.
For the same reason the MCP SDK's localhost Host-header guard is disabled
(`DisableLocalhostProtection`): a proxy such as cloudflared reaching Folio over
loopback with the public `Host` would otherwise get `403 invalid Host header`.

Claude.ai connects to a custom connector over OAuth, so the authenticator must
act as an OAuth authorization server for MCP clients. Cloudflare Access can do
this; the worked setup below assumes Folio is already served through a
Cloudflare Tunnel at `https://<host>`.

1. **Set `PUBLIC_URL`** to `https://<host>`, so the `link` returned for each book
   is absolute and clickable in Claude.ai (otherwise it is a bare `/books/{id}`).
2. In the Cloudflare dashboard open **Zero Trust → Access → Applications → Add an
   application** and choose **Self-hosted**.
3. Add a public hostname for the application with host `<host>` and path `/mcp`,
   so it covers `<host>/mcp`. Keep it separate from the Access application that
   guards the web UI (or make sure that application's scope does not shadow it).
4. In the application's settings, enable Access's OAuth support for MCP servers
   (managed OAuth for the application). The exact label depends on the current
   dashboard; look for the option that lets MCP/OAuth clients authenticate to a
   self-hosted application.
5. Add an **Access policy** with action *Allow* that includes only the intended users
   (for example, their email addresses) and your identity provider. Anyone not
   matching the policy is denied.
6. In Claude.ai, go to the connectors settings, **add a custom connector** and set
   its URL to `https://<host>/mcp`. Claude.ai starts the OAuth flow; sign in
   through Access. The five tools then become available in chats.

Check it from a shell: an unauthenticated `curl -i https://<host>/mcp` must be
redirected to or rejected by Access, not answered by Folio.

---

## Security Considerations

1. **External authenticator required for `/api`** — Folio provides no auth for the
   web UI/API; a direct deployment exposes an unauthenticated admin API. Put an
   authenticator in front (see [Web UI / API](#web-ui--api-bring-your-own-authenticator)).
2. **No secrets in source** — Env-injected secrets (e.g. `GOOGLE_KEY`) come from
   environment variables or Docker secrets, never source. OPDS Basic Auth
   credentials are not env vars at all — they are set at runtime via
   `PUT /api/settings` and stored hashed (bcrypt) in the `settings` table.
3. **Non-root container** — The Docker image runs as `nonroot` (UID/GID 65532,
   built into Distroless). The binary is owned by `root` (read-only). Only the
   `/data` directory is writable.
4. **Static binary** — `CGO_ENABLED=0` produces a statically linked binary with no
   glibc dependencies. Reduces attack surface in the Distroless runtime image.
5. **Read-only source access** — The application never writes to mounted library
   volumes.
6. **Minimal egress** — The outbound dependencies are the Google Books API, the
   user-initiated cover search / cover-from-URL fetches and, when the owner opts
   in, the Telegram Bot API (see
   [Outbound Connections](#outbound-connections)); all are best-effort. Google
   Books and the cover providers carry no catalog contents or credentials. Telegram is the owner
   notification exception: messages to the channel the owner configured may carry
   library names, error texts (including file paths inside a library), and book
   IDs or links to the instance, but never book titles taken from catalog
   metadata, book contents, credentials, or tokens (a title that is part of a
   file name inside an allowed file path is permitted). It is off by default.
7. **CSRF guard on `/api`** — State-changing API calls are protected by an
   origin-based, token-less CSRF guard (`sameSiteGuard` + `formBodyGuard`) that
   does not depend on any proxy's cookie `SameSite` policy. It is *not* a
   substitute for authentication.
8. **External authenticator required for `/mcp`** — Folio provides no auth for the
   MCP endpoint and does not apply the `/api` CSRF guard to it. Front it with an
   OAuth-capable authenticator (see [MCP endpoint](#mcp-endpoint-mcp-and-claudeai))
   and set `PUBLIC_URL`. Every MCP tool is read-only and makes no outbound calls.
9. **Telegram bot token** — The token is stored as plain text in the `settings`
   table under the data directory (like any other setting, it is protected by
   the volume's access control, not encrypted). It is write-only: the API never
   returns it, it is never logged, and it is redacted from every error, including
   Telegram's own error text. It is validated on save so it cannot alter the
   request path of the Bot API URL it is embedded in.
