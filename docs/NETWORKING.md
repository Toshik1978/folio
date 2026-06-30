# Networking & Security

> Folio's authentication model, the OPDS auth path, and how to deploy it safely.
>
> **Key point:** Folio authenticates **only the OPDS catalog** at the application
> layer. The web UI and REST API have **no built-in authentication** — you must
> place an authenticator in front of them. Cloudflare Access is used below as one
> worked example; any reverse-proxy SSO, forward-auth, or network-level control
> works just as well.

---

## The Security Model in One Table

| Surface | Paths | Auth provided by Folio | What you must add |
| :--- | :--- | :--- | :--- |
| **Web UI + REST API** | `/`, `/api/*`, SPA routes | **None.** Unauthenticated. | **Mandatory** external authenticator (reverse-proxy SSO, forward-auth, VPN/LAN isolation, …). |
| **OPDS catalog** | `/opds/*` (except cover) | **HTTP Basic Auth** (bcrypt, configured at runtime). | Nothing required; works for mobile reading apps that can't do browser SSO. |
| **OPDS cover** | `/opds/books/{id}/cover` | None (intentionally public). | Nothing — covers are low-sensitivity and many readers fetch them without forwarding credentials. |

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
handler as the `opds.Authenticator` interface. The cover endpoint is routed
separately and left unauthenticated to accommodate reading-client limitations.

```go
func (h *Handler) Register(r chi.Router) {
    // 1. Unauthenticated cover endpoint (supports reader image-loader limitations)
    r.Get("/books/{id}/cover", h.serveCover)

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
    only always-public route is the cover endpoint.
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
  `/opds/books/{id}/cover` from Basic Auth lets clients render covers cleanly;
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
local, read-only file or SQLite DB. The Google Books integration adds the one
egress path: HTTPS requests to `www.googleapis.com/books/v1` for on-view metadata
enrichment and the Fix Match feature (`internal/googlebooks`, stdlib `net/http`).

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
  never blocks catalog browsing.
- **Rate limiting (429):** a `429 Too Many Requests` from Google trips a
  process-level **cooldown** (`rateLimitCooldown`, 5 min) during which further
  enrichment/Fix-Match requests short-circuit to `ErrRateLimited` *without* an
  HTTP call. The affected books stay un-enriched (not negatively cached), so they
  are retried once the window passes — a quota wall never amplifies into per-view
  hammering.

A deployment that must stay fully offline can block egress to `googleapis.com` at
the network layer.

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
6. **Minimal egress** — The only outbound dependency is the Google Books API (see
   [Outbound Connections](#outbound-connections)); it carries no catalog contents
   or credentials and is best-effort.
7. **CSRF guard on `/api`** — State-changing API calls are protected by an
   origin-based, token-less CSRF guard (`sameSiteGuard` + `formBodyGuard`) that
   does not depend on any proxy's cookie `SameSite` policy. It is *not* a
   substitute for authentication.
