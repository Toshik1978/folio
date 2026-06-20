package ingest

import (
	"archive/zip"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/ebook"
	"github.com/Toshik1978/folio/internal/libtype"
)

// bookParser is the subset of *ebook.Dispatcher the ingest package needs:
// extension-keyed parsing plus the format-label and support probes. Defined at
// the consumer so tests can inject a fake.
type bookParser interface {
	Parse(ctx context.Context, log *slog.Logger, path string) (ebook.Metadata, error)
	Supported(path string) bool
	Format(path string) string
}

// Extractor lazily recovers cover images and annotations from a book's library
// files (e.g. an EPUB inside a ZIP archive or a standalone file). It is used by
// the API for on-demand extraction and by the optional background
// sync engine's cover-warming pass. All library files are read read-only.
type Extractor struct {
	db     *sql.DB
	log    *slog.Logger
	tmpDir string
	parser bookParser
}

// NewExtractor returns an Extractor reading from the folio database. INPX inner
// files are materialized under dataDir/tmp (not the system temp dir) to honor
// the "writable state restricted to the data dir" invariant.
func NewExtractor(db *sql.DB, log *slog.Logger, dataDir string, parser bookParser) *Extractor { //nolint:iface
	return &Extractor{db: db, log: log, tmpDir: filepath.Join(dataDir, "tmp"), parser: parser}
}

// acceptAnyFormat lets parse consider every file format.
func acceptAnyFormat(string) bool { return true }

// acceptNonPDF skips PDF files. PDFs are expensive to parse and never carry a
// usable annotation in this pipeline, so a PDF-only book resolves to "no
// annotation" without ever opening the file.
func acceptNonPDF(format string) bool { return !strings.EqualFold(format, ebook.FormatPDF) }

// Cover returns the book's cover image bytes; ok is false when none exists.
func (e *Extractor) Cover(ctx context.Context, bookID int64) ([]byte, bool, error) {
	meta, ok, err := e.parse(ctx, bookID, acceptAnyFormat)
	if err != nil || !ok || len(meta.Cover) == 0 {
		return nil, false, err
	}
	return meta.Cover, true, nil
}

// Backfill returns the full metadata parsed from the book's source file, with
// identifiers cleaned/deduped to match what sync persists. PDFs are skipped (see
// acceptNonPDF), so a PDF-only book returns ok=false without being parsed. The
// caller persists whichever fields it needs (annotation, identifiers, ...).
func (e *Extractor) Backfill(ctx context.Context, bookID int64) (ebook.Metadata, bool, error) {
	meta, ok, err := e.parse(ctx, bookID, acceptNonPDF)
	if err != nil || !ok {
		return ebook.Metadata{}, false, err
	}
	meta.Identifiers = cleanedEbookIdentifiers(meta.Identifiers)

	return meta, true, nil
}

// parse loads the book and its library, then resolves each of the book's files
// (in format-preference order) that accept admits, returning the first parsed
// metadata. ok is false when the book/library is gone or no admitted file yields
// a parseable result.
func (e *Extractor) parse(
	ctx context.Context, bookID int64, accept func(format string) bool,
) (ebook.Metadata, bool, error) {
	q := dbq.New(e.db)

	book, err := q.GetBook(ctx, bookID)
	if errors.Is(err, sql.ErrNoRows) {
		return ebook.Metadata{}, false, nil
	}
	if err != nil {
		return ebook.Metadata{}, false, fmt.Errorf("get book: %w", err)
	}
	library, err := q.GetLibrary(ctx, book.LibraryID)
	if err != nil {
		return ebook.Metadata{}, false, fmt.Errorf("get library: %w", err)
	}
	files, err := q.ListFilesForBook(ctx, bookID)
	if err != nil {
		return ebook.Metadata{}, false, fmt.Errorf("list files: %w", err)
	}

	for i := range files {
		if !accept(files[i].FileFormat) {
			continue
		}
		meta, ok, err := e.parseFile(ctx, library, files[i])
		if err != nil {
			return ebook.Metadata{}, false, err
		}
		if ok {
			return meta, true, nil
		}
	}

	return ebook.Metadata{}, false, nil
}

// parseFile resolves a single file on disk (or inside its INPX ZIP) and parses it.
func (e *Extractor) parseFile(
	ctx context.Context, source dbq.Library, file dbq.BookFile,
) (ebook.Metadata, bool, error) {
	if source.Type == libtype.INPX {
		return e.parseFromZip(ctx, source, file)
	}

	path := filepath.Join(source.Path, filepath.FromSlash(file.SourcePath))
	if !e.parser.Supported(path) {
		return ebook.Metadata{}, false, nil
	}
	meta, err := e.parser.Parse(ctx, e.log, path)
	if err != nil {
		return ebook.Metadata{}, false, fmt.Errorf("parse %s: %w", path, err)
	}

	return meta, true, nil
}

// parseFromZip extracts the INPX inner entry ("{archive}.zip/{inner}") and
// parses it via a temporary file (ebook parsers operate on paths).
func (e *Extractor) parseFromZip(
	ctx context.Context, source dbq.Library, file dbq.BookFile,
) (ebook.Metadata, bool, error) {
	archiveName, inner, found := strings.Cut(file.SourcePath, "/")
	if !found {
		return ebook.Metadata{}, false, nil
	}
	archivePath := filepath.Join(filepath.Dir(source.Path), archiveName)

	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return ebook.Metadata{}, false, fmt.Errorf("open archive %s: %w", archivePath, err)
	}
	defer func() { _ = zr.Close() }()

	rc, err := zr.Open(inner)
	if err != nil {
		return ebook.Metadata{}, false, nil // entry no longer present
	}
	defer func() { _ = rc.Close() }()

	tmp, err := e.materialize(rc, file.FileFormat)
	if err != nil {
		return ebook.Metadata{}, false, err
	}
	defer func() { _ = os.Remove(tmp) }()

	if !e.parser.Supported(tmp) {
		return ebook.Metadata{}, false, nil
	}
	meta, err := e.parser.Parse(ctx, e.log, tmp)
	if err != nil {
		return ebook.Metadata{}, false, fmt.Errorf("parse inner %s: %w", inner, err)
	}

	return meta, true, nil
}

// materialize writes r to a temp file named with the given format extension so
// the ebook dispatcher selects the right parser. The file lives under the data
// dir's tmp subdirectory (created on demand). The caller removes it.
func (e *Extractor) materialize(r io.Reader, format string) (string, error) {
	if err := os.MkdirAll(e.tmpDir, 0o755); err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}
	f, err := os.CreateTemp(e.tmpDir, "folio-cover-*."+format)
	if err != nil {
		return "", fmt.Errorf("temp file: %w", err)
	}
	defer func() { _ = f.Close() }()

	if _, err := io.Copy(f, r); err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("write temp: %w", err)
	}

	return f.Name(), nil
}
