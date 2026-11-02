# Contract: ebook reading layer (Go-internal)

This contract covers the package `internal/ebook`. It is the only package on the **reading** path that names formats. `bookfile` (downloads) keeps its own archive lookup and is out of scope, except that its MIME lookup delegates to `ebook.ContentType`. The sketch below shows signatures only; the bodies are the implementation's.

## Source

```go
// Source is a book's bytes, readable at any offset, with a known size.
// Name carries the file name (or archive entry name) used for routing.
type Source struct {
    Name string
    R    io.ReaderAt
    Size int64
}

// OpenFile opens a plain file read-only. R is TagIO(*os.File); Size from Stat.
func OpenFile(path string) (Source, io.Closer, error)

// OpenZipEntry returns a Source over a zip entry that inflates lazily: it
// inflates sequentially only up to the furthest offset read and keeps the
// inflated prefix in memory. Holding more than limit bytes returns ErrTooLarge.
// Production passes MaxEntryBytes; tests pass a small limit.
// Safe for concurrent ReadAt. The returned Closer releases the decompressor and the buffer.
func OpenZipEntry(f *zip.File, limit int64) (Source, io.Closer)

// TagIO wraps an underlying file so its read errors match ErrSourceIO.
func TagIO(r io.ReaderAt) io.ReaderAt

const MaxEntryBytes int64 = 64 << 20
```

## Parser and Dispatcher

```go
type Traits struct {
    Rank      int  // higher = preferred file of a book (EPUB 4, FB2 3, MOBI family 2, PDF 1)
    Describes bool // false = never read for descriptions (PDF)
}

// Parser is format-agnostic and exported only so tests can register fakes.
// The concrete parsers (fb2Parser, epubParser, mobiParser, pdfParser) are unexported.
type Parser interface {
    Extensions() []string
    Traits() Traits
    // Metadata never decodes cover images. Metadata has no Cover field.
    Metadata(ctx context.Context, src Source) (Metadata, error)
    // Cover returns (nil, nil) when the file has no cover.
    Cover(ctx context.Context, src Source) ([]byte, error)
}

type Dispatcher struct{ /* parsers by extension */ }

func NewDispatcher(parsers ...Parser) *Dispatcher
func NewStandardDispatcher() *Dispatcher          // the standard parser set; used by main.go and tests
func (d *Dispatcher) Extensions() []string        // every registered extension, e.g. for guard tests
func (d *Dispatcher) Metadata(ctx context.Context, log *slog.Logger, src Source) (Metadata, error)
func (d *Dispatcher) Cover(ctx context.Context, log *slog.Logger, src Source) ([]byte, error)
func (d *Dispatcher) Supported(name string) bool
func (d *Dispatcher) Format(name string) string   // stored label, ".fb2.zip" → "fb2"
func (d *Dispatcher) Rank(format string) int      // by stored label; 0 = unknown
func (d *Dispatcher) Describes(format string) bool

func ContentType(format string) string            // MIME type by stored label; octet-stream if unknown
```

The Dispatcher, for both `Metadata` and `Cover`:
1. Returns `ctx.Err()` (wrapped) before touching the source.
2. Routes by `src.Name` extension. An unsupported extension returns an error, and callers check `Supported` first.
3. Recovers a panic into `*PanicError`.
4. Passes `ErrSourceIO`, `ErrTooLarge` and ctx errors through. Any other parser error is wrapped so that it matches `ErrMalformed`.

The FB2 header-only read, the `.fb2.zip` unwrapping and the FB2 streaming cover scan are unexported details of `fb2Parser`.

**Public surface rule**: no exported identifier of `internal/ebook` (type, function, method of an exported type, constant, variable) may have a camelCase word equal to a format label or extension (`epub`, `fb2`, `fbz`, `mobi`, `azw`, `azw3`, `pdf`). The format labels become unexported constants. An `ebookSuite` AST test enforces this.

**Cover operation**: `Cover` returns `(nil, nil)` when the file declares no cover, or when the declared cover reference points at nothing (FB2 href with no matching `<binary>`, MOBI cover offset outside the records). It returns an error, which the Dispatcher wraps as `ErrMalformed`, when the declared cover payload cannot be extracted: FB2 base64 that is invalid or truncated, or an EPUB manifest cover item whose zip entry is missing or fails to inflate. Extracted bytes that are not a decodable image are returned as-is; `covers` marks them none (`cover_state = 2`).

## Errors and outcome inputs

```go
var (
    ErrSourceIO  = errors.New("ebook: source read failed")    // retryable
    ErrTooLarge  = errors.New("ebook: entry exceeds size cap") // broken: too_large
    ErrMalformed = errors.New("ebook: malformed content")      // broken: corrupt
)

type PanicError struct{ Value any }                            // broken: crashed

type Reason string
const (
    ReasonCorrupt      Reason = "corrupt"
    ReasonMissingEntry Reason = "missing_entry" // raised by ingest's resolver
    ReasonTooLarge     Reason = "too_large"
    ReasonCrashed      Reason = "crashed"
)

// ReasonOf reports the broken reason for err. ok is false for retryable or
// unknown errors (ErrSourceIO, context errors, open errors).
func ReasonOf(err error) (Reason, bool)
```

## Reader version

```go
// ReaderVersion identifies the current reading logic. Bump it in the same
// change as a reader fix that could make a previously broken file readable.
const ReaderVersion = 1
```

## Consumer side (`internal/ingest`, declared there)

```go
type bookReader interface {
    Supported(name string) bool
    Format(name string) string
    Rank(format string) int
    Describes(format string) bool
    Metadata(ctx context.Context, log *slog.Logger, src ebook.Source) (ebook.Metadata, error)
    Cover(ctx context.Context, log *slog.Logger, src ebook.Source) ([]byte, error)
}
```

- `ingest`'s resolver is the only code on the reading path that maps `(library, book_file)` to a `Source` + fingerprint, and the only reading code that knows archive collections keep entries in sibling archives (`bookfile` downloads are out of scope).
- `ingest` exports `ReadStatus` (`ReadDone`, `ReadBroken`, `ReadRetryable`) and `ReadResult{Status, Reason, Detail, Fingerprint, Known, Err}`, so `FileExtractor.Description(ctx, bookID) (ebook.Metadata, ReadResult)` can be faked outside `ingest`.
- `covers.CoverExtractor` (`Cover(ctx, bookID) ([]byte, bool, error)`) is unchanged. Broken and retryable cover reads both return an error, so `cover_state` is not set.

## Removed

- `ebook.ParseFB2Description`
- `Parser.Parse` and `Dispatcher.Parse`
- `NewEPUB`/`NewFB2`/`NewMOBI`/`NewPDF` and the exported parser types (now unexported)
- the exported `Format*` constants (now unexported; `bookfile.ContentType` uses `ebook.ContentType`)

`Metadata.Cover` is **kept**: online providers (`metasearch`, `api/enrich.go`, `api/match.go`) fill it. Ebook parsers' `Metadata` never sets it.
- `ingest.Extractor.materialize`, `parseFromZip`, `tmpDir`
- `DeepIndexer.parseFB2`, `inpxItem`, `parseEntry`, `deepIndexArchive`
- `acceptNonPDF`, `isPDF`
