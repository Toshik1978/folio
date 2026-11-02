// Package mcpserver implements Folio's read-only MCP server, mounted behind an
// external authenticator (Cloudflare Access) at /mcp.
package mcpserver

import (
	"database/sql"
	"log/slog"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// serverInstructions is sent to the client at initialize. Keep in sync with
// specs/001-mcp-book-discovery/contracts/mcp-tools.md.
const serverInstructions = "Folio is a self-hosted ebook catalog; books may be in any language.\n" +
	"Search matches whole words, without stemming. " +
	"Use `stem*` prefixes for word forms (e.g. `murder*`, `detective*`), in the language of the books, " +
	"and use `any_terms` for synonyms. " +
	"Narrow with `genre`/`language` taken from `list_facets`.\n" +
	"To judge plot-level fit, read the `excerpt`, or call `get_book` for the full description.\n" +
	"Always give the user each book's `link`."

// service holds the dependencies the MCP tools hang off.
type service struct {
	log       *slog.Logger
	db        *sql.DB
	publicURL string
}

// newMCPServer builds the MCP server with its identity and instructions.
func newMCPServer(log *slog.Logger, database *sql.DB, publicURL, version string) *mcp.Server {
	svc := &service{log: log, db: database, publicURL: publicURL}
	srv := mcp.NewServer(
		&mcp.Implementation{Name: "folio", Title: "Folio library", Version: version},
		&mcp.ServerOptions{Instructions: serverInstructions},
	)
	svc.registerTools(srv)

	return srv
}

// New returns the stateless streamable-HTTP MCP handler. Authentication is
// not performed here; an external authenticator must front the endpoint.
func New(log *slog.Logger, database *sql.DB, publicURL, version string) http.Handler {
	srv := newMCPServer(log, database, publicURL, version)

	return mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{
			Stateless:    true,
			JSONResponse: true,
			// Folio sits behind cloudflared or another reverse proxy that may
			// reach it over loopback while forwarding the public Host. The SDK's
			// DNS-rebinding guard would answer 403 to that. /mcp is read-only and
			// exposes nothing the unauthenticated /api GET routes do not.
			DisableLocalhostProtection: true,
		},
	)
}
