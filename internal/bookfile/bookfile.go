// Package bookfile streams a stored book's bytes over HTTP. It is shared by the
// REST API and the OPDS catalog, which cannot import each other. Source files
// are read read-only; folder/calibre books are served from disk (sendfile),
// while INPX books are streamed straight out of their sibling ZIP archive.
package bookfile

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/ebook"
	"github.com/Toshik1978/folio/internal/libtype"
)

// Serve writes the file referenced by (library, file) to w with the appropriate
// Content-Type and Content-Disposition headers. It returns an error (already
// written to w as an HTTP status) when the file cannot be served.
//
// Downloads are exempted from the server-wide WriteTimeout: a multi-hundred-MB
// book over a slow mobile link (the OPDS use case) legitimately exceeds any
// fixed deadline, and both download routes stream through here. Other routes
// keep the 60s bound. Best-effort: recorders/proxies without deadline support
// just keep the global timeout.
func Serve(w http.ResponseWriter, r *http.Request, library dbq.Library, file dbq.BookFile) error {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	w.Header().Set("Content-Type", ContentType(file.FileFormat))
	if library.Type == libtype.INPX {
		return serveFromZip(w, library, file)
	}

	return serveFromDisk(w, r, library, file)
}

// serveFromDisk serves a folder/calibre file directly from disk.
func serveFromDisk(w http.ResponseWriter, r *http.Request, library dbq.Library, file dbq.BookFile) error {
	full := filepath.Join(library.Path, filepath.FromSlash(file.SourcePath))
	if !withinRoot(library.Path, full) {
		http.Error(w, "Invalid Book Path", http.StatusBadRequest)
		return fmt.Errorf("path escapes library root: %s", file.SourcePath)
	}
	w.Header().Set("Content-Disposition", contentDisposition(path.Base(file.SourcePath)))
	http.ServeFile(w, r, full)

	return nil
}

// serveFromZip serves an INPX file by seeking into its sibling ZIP archive
// (source_path is stored as "{archive}.zip/{inner}").
func serveFromZip(w http.ResponseWriter, library dbq.Library, file dbq.BookFile) error {
	archiveName, inner, found := strings.Cut(file.SourcePath, "/")
	if !found {
		http.Error(w, "Malformed Source Path", http.StatusInternalServerError)
		return fmt.Errorf("malformed inpx source path: %s", file.SourcePath)
	}
	// The archive is a sibling of the .inpx index; archiveName comes from the
	// stored source_path, so guard against a "../" escaping that directory —
	// symmetric with serveFromDisk's check.
	root := filepath.Dir(library.Path)
	archivePath := filepath.Join(root, archiveName)
	if !withinRoot(root, archivePath) {
		http.Error(w, "Invalid Book Path", http.StatusBadRequest)
		return fmt.Errorf("archive path escapes library root: %s", file.SourcePath)
	}

	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		http.Error(w, "Failed to Open Archive", http.StatusInternalServerError)
		return fmt.Errorf("open archive %s: %w", archivePath, err)
	}
	defer func() { _ = zr.Close() }()

	entry, err := zr.Open(inner)
	if err != nil {
		http.Error(w, "File Not Found in Archive", http.StatusNotFound)
		return fmt.Errorf("open inner %s: %w", inner, err)
	}
	defer func() { _ = entry.Close() }()

	w.Header().Set("Content-Disposition", contentDisposition(path.Base(inner)))
	// Advertise the entry's uncompressed size so clients get a real progress
	// bar. Range is NOT supported on this path (the entry streams out of a
	// DEFLATE archive; Range requests get a 200 full body) — only plain-file
	// downloads (serveFromDisk → http.ServeFile) can resume. Stat() reports the
	// authoritative size that io.Copy will write, so the header can't drift
	// from the body.
	if info, statErr := entry.Stat(); statErr == nil {
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	}
	if _, err := io.Copy(w, entry); err != nil {
		return fmt.Errorf("stream archive entry %s: %w", inner, err)
	}

	return nil
}

// ContentType maps a file format label to its MIME type.
func ContentType(format string) string {
	switch strings.ToLower(format) {
	case ebook.FormatEPUB:
		return "application/epub+zip"
	case ebook.FormatFB2:
		return "application/x-fictionbook+xml"
	case ebook.FormatMOBI, ebook.FormatAZW, ebook.FormatAZW3:
		return "application/x-mobipocket-ebook"
	case ebook.FormatPDF:
		return "application/pdf"
	default:
		return "application/octet-stream"
	}
}

// withinRoot reports whether full resolves inside root, guarding against any
// "../" stored in a source path.
func withinRoot(root, full string) bool {
	rel, err := filepath.Rel(root, full)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// contentDisposition builds an attachment header with both an ASCII fallback
// (filename=) and an RFC 5987 encoded form (filename*=) so readers display
// non-ASCII names (e.g. a Cyrillic catalog) correctly instead of mangling them.
func contentDisposition(filename string) string {
	return `attachment; filename="` + asciiFallback(filename) +
		`"; filename*=UTF-8''` + encodeRFC5987(filename)
}

// asciiFallback strips quotes/backslashes/control characters and replaces any
// non-ASCII rune with '_' for the legacy filename= parameter.
func asciiFallback(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '"' || r == '\\' || r < 0x20:
			// drop quotes, backslashes, and control characters — a trailing '\'
			// would escape the closing quote of the legacy filename="…" param
		case r > 0x7e:
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}

	return b.String()
}

// encodeRFC5987 percent-encodes s per RFC 5987's attr-char set for the
// filename* value-chars.
func encodeRFC5987(s string) string {
	const upperhex = "0123456789ABCDEF"
	var b strings.Builder
	for i := range len(s) {
		c := s[i]
		if isAttrChar(c) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(upperhex[c>>4])
		b.WriteByte(upperhex[c&0x0f])
	}

	return b.String()
}

// isAttrChar reports whether c may appear unescaped in an RFC 5987 ext-value.
func isAttrChar(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte("!#$&+-.^_`|~", c) >= 0
}
