# Contract: `/mcp` MCP tools

## Transport

- Streamable HTTP. Stateless (no `Mcp-Session-Id`), POST only, JSON responses.
- Built on `github.com/modelcontextprotocol/go-sdk` v1.8.0.
- Server identity: `{name: "folio", title: "Folio library", version: <build version>}`.
- No authentication in Folio. An external authenticator (Cloudflare Access with OAuth for MCP) is required.
- Every tool is read-only and annotated `readOnlyHint: true`.
- No tool makes outbound network calls.
- Unknown sub-paths under `/mcp/*` return 404. GET and DELETE on `/mcp` return 405, as the SDK does in stateless mode.
- The SDK's localhost (DNS-rebinding) guard is disabled, so a proxy that reaches Folio over loopback while forwarding the public Host is not answered with 403.

The input and output shapes below are the Go structs. The SDK derives their JSON Schemas. A field without `omitempty` is required.

## Server instructions (sent at initialize)

> Folio is a self-hosted ebook catalog; books may be in any language.
> Search matches whole words, without stemming. Use `stem*` prefixes for word forms (e.g. `murder*`, `detective*`), in the language of the books, and use `any_terms` for synonyms. Narrow with `genre`/`language` taken from `list_facets`.
> To judge plot-level fit, read the `excerpt`, or call `get_book` for the full description.
> Always give the user each book's `link`.

## Tools

### `list_libraries`

- **In**: `{}`
- **Out**: `{libraries: [{id, name, type, book_count}]}`

### `list_facets`

- **In**: `{library_id?: int}`. 0 or absent means all libraries.
- **Out**: `{genres: [{name, book_count}], languages: [{code, book_count}]}`
- Genres are sorted by count, descending (ties by name), top 200. Languages are sorted by count, descending (ties by code); empty language codes are left out.

### `search_books`

**In**:

| Field | Type | Notes |
|---|---|---|
| `query` | string? | All words must match, across title, authors, series and description. A trailing `*` means prefix. |
| `any_terms` | string[]? | At least one must match, ANDed with `query`. Each term may use `*`; a multi-word term needs all its words. At most 20 terms. |
| `title`, `author`, `series` | string? | Field-scoped words, ANDed. |
| `genre` | string? | An exact genre name from `list_facets`. |
| `language` | string? | An ISO code, e.g. `ru`. |
| `library_id` | int? | |
| `format` | string? | `fb2`, `epub`, … |
| `year_from`, `year_to` | int? | Inclusive. |
| `limit` | int? | Default 20, max 50 (a larger value is capped). |
| `offset` | int? | Default 0 (a negative value counts as 0). |

At least one of `query`, `any_terms`, `title`, `author`, `series` or `genre` must be given; otherwise the tool returns an error. A value with no word in it (only whitespace, `*` or punctuation) does not count, and such a token is dropped from the match.

Bounds, checked before the search runs; a request over one is a tool error that names it:
- at most 500 characters in each of `query`, `title`, `author`, `series` and each `any_terms` entry;
- at most 40 words across all of those fields;
- at most 10 prefix (`*`) terms;
- at least 3 letters before each `*`.

**Out**: `{total: int, offset: int, books: [BookSummary]}`

- Ranking: by bm25 relevance when there are text terms, otherwise most recently added first. Equal bm25 scores are broken by book id, descending, so offset paging is stable; recency ties fall back to the source date, then book id.

**Errors**: returned as tool errors (`isError: true`) with a message Claude can act on. Examples:
- `"give at least one of query, any_terms, title, author, series or genre"`
- `"genre not found; call list_facets"`
- `"a search takes at most 10 prefix (*) terms"`, `"a prefix term needs at least 3 letters before the *"`
- `"invalid query: <FTS5 detail>"` for an FTS5 syntax error. Every token is quoted, so user input cannot normally produce one.
- Any other database failure is logged and reported only as `"search failed"`.

### `get_book`

- **In**: `{id: int}`
- **Out**: `BookDetail`
- Unknown id: tool error `"book not found"`.

### `similar_books`

- **In**: `{id: int, limit?: int}`. Default 20, max 50.
- **Out**: `{source: BookSummary, candidates: [SimilarCandidate]}`
- The source book itself is never among the candidates.
- If the book has no description, candidates come from series, author and genres only.
- Candidates are merged in reason order (`distinctive_terms`, `same_series`, `same_author`, `shared_genres`), each book once under the first reason that found it, up to `limit`.
- A candidate deleted by a concurrent sync before it is loaded is dropped, so the list may be shorter than `limit`.
- Unknown id: tool error `"book not found"`.
- `distinctive_terms` candidates share words with the source's description in their own description: titles, authors and series are not matched, and a word's distinctiveness is measured over descriptions only. Up to 8 terms are used: the source's top terms by tf·idf, skipping words found in only one description or in all of them.

## Shapes

```text
BookSummary {
  id int, title string, authors []string, series string?, series_number number?,
  genres []string, language string, year int?, excerpt string?, has_description bool,
  link string
}
BookDetail = BookSummary − excerpt + {
  description string?, publisher string?, rating int?,
  identifiers [{type, value}], formats []string, library {id, name}
}
SimilarCandidate = BookSummary + { reason: "distinctive_terms"|"same_series"|"same_author"|"shared_genres",
                                   matched_terms []string? }
```

- `excerpt`: the description with markup stripped, at most 300 characters, cut at a word boundary, with `…` appended when cut.
- `link`: `{PUBLIC_URL}/books/{id}`, or `/books/{id}` when `PUBLIC_URL` is unset.
