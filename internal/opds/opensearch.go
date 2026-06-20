package opds

import (
	"encoding/xml"
	"log/slog"
	"net/http"
	"strings"

	"github.com/samber/lo"
)

// openSearchDescription is the OpenSearch description document advertised by the
// catalog so readers know how to build search URLs.
type openSearchDescription struct {
	XMLName     xml.Name `xml:"http://a9.com/-/spec/opensearch/1.1/ OpenSearchDescription"`
	ShortName   string   `xml:"ShortName"`
	Description string   `xml:"Description"`
	InputEnc    string   `xml:"InputEncoding"`
	URL         osURL    `xml:"Url"`
}

type osURL struct {
	Type     string `xml:"type,attr"`
	Template string `xml:"template,attr"`
}

// openSearch handles GET /opds/opensearch.xml.
func (h *Handler) openSearch(w http.ResponseWriter, r *http.Request) {
	desc := openSearchDescription{
		ShortName:   "Folio",
		Description: "Search the Folio library",
		InputEnc:    "UTF-8",
		URL: osURL{
			Type:     typeAcquisition,
			Template: h.searchTemplate(r),
		},
	}

	w.Header().Set("Content-Type", typeOpenSearch)
	if _, err := w.Write([]byte(xml.Header)); err != nil {
		h.log.Error("write opensearch header", slog.Any("error", err))
		return
	}

	enc := xml.NewEncoder(w)
	defer func() { _ = enc.Close() }()

	enc.Indent("", "  ")
	if err := enc.Encode(desc); err != nil {
		h.log.Error("encode opensearch", slog.Any("error", err))
	}
}

// searchTemplate builds the absolute OpenSearch URL template advertised to
// readers. It prefers the configured canonical base URL (h.publicURL), which is
// immune to a forged X-Forwarded-Host. When unset (local/direct access) it
// falls back to the request: scheme from proxyHeaders (r.URL.Scheme) and host
// from r.Host — note r.URL.Host is empty on inbound server requests.
func (h *Handler) searchTemplate(r *http.Request) string {
	base := lo.CoalesceOrEmpty(h.publicURL, lo.CoalesceOrEmpty(r.URL.Scheme, "http")+"://"+r.Host)
	return strings.TrimRight(base, "/") + opdsPrefix + "/search?q={searchTerms}"
}
