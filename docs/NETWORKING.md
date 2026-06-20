# Networking & Security

> Cloudflare Access, OPDS auth bypass, and the security model.
>
> **Status:** Design — Cloudflare configuration is external to the codebase.

---

## Traffic Flow

```
                                  ┌──► [ Policy 1: Access Rules ] ──► Requires MFA/SSO
[ Public Traffic ] ──► [ CF ] ────┤
                                  └──► [ Policy 2: Bypass /opds* ] ──► Direct to app
                                                                         (Basic Auth)
```

All public traffic to the application domain routes through a Cloudflare Tunnel.

---

## Cloudflare Access Policies

### Policy 1: Default (Web UI + API)

| Setting | Value |
| :--- | :--- |
| Scope | `*` (all paths) |
| Action | Require authentication |
| Provider | SSO / MFA |
| Protects | `/`, `/api/*`, all SPA routes |

The browser UI and REST API are fully protected. Users must authenticate via Cloudflare's identity provider integration before any request reaches the Go backend.

### Policy 2: OPDS Bypass

| Setting | Value |
| :--- | :--- |
| Scope | `/opds*` |
| Action | Bypass |
| Effect | Requests skip Cloudflare Access entirely |

Mobile reading apps (Moon+ Reader, KyBook) cannot perform browser-based SSO flows. The `/opds*` path is exempted from Cloudflare Access so that these clients can connect directly.

---

## Application-Level Authentication

### OPDS Basic Auth & Cover Bypass

Because `/opds*` bypasses Cloudflare Access, the application enforces its own authentication for feeds and book downloads using the `auth.Authenticator.Middleware` (`internal/auth/auth.go`), injected into the OPDS handler as the `opds.Authenticator` interface. However, the cover image endpoint is routed separately to bypass authentication to accommodate reading client limitations.

```go
func (h *Handler) Register(r chi.Router) {
    // 1. Unauthenticated cover endpoint (supports Moon+ Reader image limitations)
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

- **Credentials:** Stored in the `settings` table (password hashed) and configured solely through the admin API (`PUT /api/settings`). There is **no** environment-variable seed (`OPDS_USER`/`OPDS_PASS` do not exist).
  - **No-credentials behavior**: If no credentials are configured, the middleware **rejects every protected route with `401`** (the unconfigured branch returns a bare `401` without a `WWW-Authenticate` header). The catalog is therefore **closed, not open** — OPDS won't serve feeds or downloads until a credential is set. A **severe startup security warning** (`WarnIfUnprotected`) flags the empty state, but it does *not* fall through to unauthenticated serving. The only always-public route is the cover endpoint.
- **Verification (`verifyCredentials`):** the username is compared in constant time, and a username mismatch still runs a full bcrypt compare against a process-local dummy hash — so a wrong username costs the same ~100ms as a wrong password and response timing can't be used as a username oracle (both the dummy and all stored hashes use `bcrypt.DefaultCost`).
- **Success cache:** reading apps fetch feeds, covers metadata, and files in bursts, and bcrypt costs ~100ms per verify by design. The last *successful* `(user, password, stored-hash)` triple is therefore cached as a single SHA-256 key (compared in constant time); repeat requests skip bcrypt entirely. Embedding the stored hash in the key invalidates the cache implicitly on credential rotation, and `SetCredentials` (called by `PUT /api/settings`) also clears it explicitly via the Authenticator's internal `invalidate`, together with the credential cache.
- **Basic Auth Realm:** The realm string `"OPDS Library Manager"` is sent in the `WWW-Authenticate` header.
- **OPDS Cover Authentication Exception:** Many popular reading apps (e.g. Moon+ Reader, KyBook) use separate native image loading frameworks to pull cover images asynchronously from the catalog. These sub-frameworks do not forward the HTTP Basic Auth credentials supplied when registering the OPDS repository feed. If the cover endpoint requires auth, covers fail to load (rendering broken image icons).
- **Security Mitigation:** Excluding `/opds/books/{id}/cover` from Basic Auth allows client apps to render covers cleanly. Since book covers are not highly sensitive metadata, exposing them publicly represents a secure and acceptable trade-off to ensure feed usability.

### Web UI / API

No application-level auth middleware is applied to `/api/*` or SPA routes. Authentication is delegated entirely to Cloudflare Access, which injects authenticated user identity via headers (`CF-Access-JWT-Assertion`).

---

## Header Handling

| Header | Source | Usage |
| :--- | :--- | :--- |
| `X-Forwarded-Proto` | Cloudflare / proxy | `proxyHeaders` middleware sets `request.URL.Scheme` (used as the scheme fallback when building the absolute OPDS OpenSearch URL). Only the literal values `http`/`https` are honored — on direct connections the header is client-controllable, so anything else falls back to the TLS-derived scheme |
| `X-Forwarded-Host` | — | **Not trusted.** Deliberately ignored — see below. |

> The request logger (chi's `middleware.Logger`, dev-only) records `r.RemoteAddr` as-is; there
> is no `middleware.RealIP` / `CF-Connecting-IP` parsing in the current code.

### Canonical host: `PUBLIC_URL` (not `X-Forwarded-Host`)

Because `/opds*` bypasses Cloudflare Access, any caller can set
`X-Forwarded-Host`. If the app reflected it into the absolute URLs it
advertises (the OPDS OpenSearch `template`), a caller could poison those URLs.
So `proxyHeaders` **does not** honor `X-Forwarded-Host`.

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

## Security Considerations

1. **No secrets in source** — Env-injected secrets (e.g. `GOOGLE_KEY`) come from environment variables or Docker secrets, never source. OPDS Basic Auth credentials are not env vars at all — they are set at runtime via `PUT /api/settings` and stored hashed (bcrypt) in the `settings` table.
2. **Non-root container** — The Docker image runs as `nonroot` (UID/GID 65532, built into Distroless). The binary is owned by `root` (read-only). Only the `/data` directory is writable.
3. **Static binary** — `CGO_ENABLED=0` produces a statically linked binary with no glibc dependencies. Reduces attack surface in the Distroless runtime image.
4. **Read-only source access** — The application never writes to mounted library volumes.
5. **Minimal egress** — The only outbound dependency is the Google Books API (see [Outbound Connections](#outbound-connections)); it carries no catalog contents or credentials and is best-effort.
