package ebook

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
)

// Dispatcher routes a file to the registered Parser that owns its extension.
// It replaces the former package-global parser registry: the composition root
// builds one Dispatcher from an explicit parser list and injects it.
type Dispatcher struct {
	parsers  map[string]Parser // by extension key, e.g. ".fb2.zip"
	labels   map[string]Parser // by stored format label, e.g. "fb2", "azw3"
	reporter CrashReporter     // optional; told about recovered parser panics
}

// CrashReporter is told about a parser panic the Dispatcher recovered. It
// never receives the source's file name: that is catalog content. ReportCrash
// reports whether the crash was queued as a message.
type CrashReporter interface {
	ReportCrash(place, detail string, recovered any, stack []byte) bool
}

// Option configures a Dispatcher built by NewStandardDispatcher.
type Option func(*Dispatcher)

// WithCrashReporter makes the Dispatcher report each recovered parser panic.
func WithCrashReporter(r CrashReporter) Option {
	return func(d *Dispatcher) { d.reporter = r }
}

// NewDispatcher builds a Dispatcher, indexing each parser by every extension it
// declares. It panics on a duplicate extension — a composition-root programming
// error caught at startup (consistent with config.MustParse).
func NewDispatcher(parsers ...Parser) *Dispatcher {
	m := make(map[string]Parser)
	labels := make(map[string]Parser)
	for _, p := range parsers {
		for _, ext := range p.Extensions() {
			if _, dup := m[ext]; dup {
				panic(fmt.Sprintf("ebook: duplicate parser for extension %q", ext))
			}
			m[ext] = p
			labels[formatLabel(ext)] = p
		}
	}

	return &Dispatcher{parsers: m, labels: labels}
}

// NewStandardDispatcher builds a Dispatcher over the standard parser set
// (EPUB, FB2, MOBI family, PDF). The composition root and tests use it.
func NewStandardDispatcher(opts ...Option) *Dispatcher {
	d := NewDispatcher(epubParser{}, fb2Parser{}, mobiParser{}, pdfParser{})
	for _, opt := range opts {
		opt(d)
	}

	return d
}

// Metadata selects the parser for src.Name's extension and reads src's
// metadata. See run for the cancellation, panic and error-classification rules.
func (d *Dispatcher) Metadata(ctx context.Context, log *slog.Logger, src Source) (Metadata, error) {
	var meta Metadata
	err := d.run(ctx, log, src, "metadata", func(p Parser) error {
		var err error
		meta, err = p.Metadata(ctx, src)
		return err //nolint:wrapcheck // run classifies and wraps the parser error
	})
	if err != nil {
		return Metadata{}, err
	}

	return meta, nil
}

// Cover selects the parser for src.Name's extension and reads src's cover
// image, returning (nil, nil) when the file has none. See run for the
// cancellation, panic and error-classification rules.
func (d *Dispatcher) Cover(ctx context.Context, log *slog.Logger, src Source) ([]byte, error) {
	var cover []byte
	err := d.run(ctx, log, src, "cover", func(p Parser) error {
		var err error
		cover, err = p.Cover(ctx, src)
		return err //nolint:wrapcheck // run classifies and wraps the parser error
	})
	if err != nil {
		return nil, err
	}

	return cover, nil
}

// Rank returns the preference rank of the stored format label (e.g. "epub",
// "azw3"); higher is preferred among a book's files. Unknown labels rank 0.
func (d *Dispatcher) Rank(format string) int {
	if p, ok := d.labels[strings.ToLower(format)]; ok {
		return p.Traits().Rank
	}

	return 0
}

// Describes reports whether files of the stored format label are read for
// descriptions. Unknown labels report false.
func (d *Dispatcher) Describes(format string) bool {
	if p, ok := d.labels[strings.ToLower(format)]; ok {
		return p.Traits().Describes
	}

	return false
}

// Extensions returns every registered extension key, sorted.
func (d *Dispatcher) Extensions() []string {
	exts := make([]string, 0, len(d.parsers))
	for ext := range d.parsers {
		exts = append(exts, ext)
	}
	slices.Sort(exts)

	return exts
}

// Supported reports whether path has an extension a registered parser can read.
func (d *Dispatcher) Supported(path string) bool {
	_, ok := d.parsers[d.fileExt(path)]
	return ok
}

// Format returns the normalized file-format label for path (e.g. "epub", "fb2",
// "mobi"). The ".fb2.zip" wrapper normalizes to "fb2". Returns "" for
// unsupported extensions.
func (d *Dispatcher) Format(path string) string {
	ext := d.fileExt(path)
	if _, ok := d.parsers[ext]; !ok {
		return ""
	}

	return formatLabel(ext)
}

// run routes src to its parser and runs op on it. It returns a wrapped
// ctx.Err() before touching src, recovers a parser panic into *PanicError,
// passes ErrSourceIO, ErrTooLarge, context errors and errors that already match
// ErrMalformed through, and wraps any other parser error so it matches
// ErrMalformed. Parsers read untrusted bytes (pdfcpu in particular can panic on
// malformed input), so one bad file must become a classifiable error rather
// than crash a background goroutine.
func (d *Dispatcher) run(
	ctx context.Context,
	log *slog.Logger,
	src Source,
	what string,
	op func(Parser) error,
) (err error) {
	if err = ctx.Err(); err != nil {
		return fmt.Errorf("read %s of %s: %w", what, src.Name, err)
	}

	ext := d.fileExt(src.Name)
	p, ok := d.parsers[ext]
	if !ok {
		return fmt.Errorf("read %s of %s: unsupported format %q", what, src.Name, ext)
	}
	format := formatLabel(ext)
	defer func() {
		if r := recover(); r != nil {
			stack := debug.Stack()
			err = &PanicError{Value: r}
			if d.reporter != nil {
				d.reporter.ReportCrash("book-file reading", format+" parser, "+what+" read", r, stack)
			}
		}
	}()

	log.Debug("--> read book "+what, slog.String("name", src.Name))
	defer log.Debug("<-- read book "+what, slog.String("name", src.Name))

	err = op(p)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrSourceIO), errors.Is(err, ErrTooLarge), errors.Is(err, ErrMalformed),
		errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%s parser: %w", format, err)
	default:
		return fmt.Errorf("%s parser: %w: %w", format, ErrMalformed, err)
	}
}

// formatLabel maps a registered extension key to its stored format label:
// the extension without its dot, except that the ".fb2.zip" wrapper is "fb2".
func formatLabel(ext string) string {
	if ext == extFB2Zip {
		return formatFB2
	}

	return strings.TrimPrefix(ext, ".")
}

// ContentType maps a stored format label to its MIME type, falling back to
// application/octet-stream for unknown labels.
func ContentType(format string) string {
	switch strings.ToLower(format) {
	case formatEPUB:
		return "application/epub+zip"
	case formatFB2:
		return "application/x-fictionbook+xml"
	case formatMOBI, formatAZW, formatAZW3:
		return "application/x-mobipocket-ebook"
	case formatPDF:
		return "application/pdf"
	default:
		return "application/octet-stream"
	}
}

func (d *Dispatcher) fileExt(path string) string {
	base := strings.ToLower(filepath.Base(path))
	i := 0
	for {
		dot := strings.Index(base[i:], ".")
		if dot < 0 {
			break
		}
		i += dot
		if _, ok := d.parsers[base[i:]]; ok {
			return base[i:]
		}
		i++
	}

	return strings.ToLower(filepath.Ext(path))
}
