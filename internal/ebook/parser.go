package ebook

import "context"

// Parser reads the file formats it declares via Extensions. It is exported only
// so tests can register fakes; the concrete parsers are unexported and
// NewStandardDispatcher assembles them.
//
// Each implementation owns one logical format (which may map to several
// extensions, e.g. the MOBI family owns three). The Dispatcher, not the Parser,
// derives the stored format label, so adding extensions here never changes
// book_files.file_format semantics.
type Parser interface {
	// Extensions returns the lower-case extension keys this parser owns,
	// including the leading dot and any multi-dot wrapper (e.g. ".fb2.zip").
	Extensions() []string
	// Traits describes the format for ranking a book's files and for deciding
	// whether it is read for descriptions.
	Traits() Traits
	// Metadata reads the bibliographic metadata of src. It never decodes cover
	// images and never sets Metadata.Cover. Panic recovery, error
	// classification and debug logging are the Dispatcher's responsibility.
	Metadata(ctx context.Context, src Source) (Metadata, error)
	// Cover returns the cover image bytes of src, or (nil, nil) when the file
	// declares no cover.
	Cover(ctx context.Context, src Source) ([]byte, error)
}

// Traits describes a parser's format for ranking and description purposes.
type Traits struct {
	// Rank orders the files of one book: higher is preferred
	// (EPUB 4, FB2 3, MOBI family 2, PDF 1).
	Rank int
	// Describes is false when the format is never read for descriptions (PDF).
	Describes bool
}

// epubParser reads EPUB files.
type epubParser struct{}

// Extensions returns the file extensions handled by epubParser.
func (epubParser) Extensions() []string { return []string{".epub"} }

// Traits describes the EPUB format.
func (epubParser) Traits() Traits { return Traits{Rank: 4, Describes: true} }

// Metadata reads the OPF package of src. Metadata.Cover is never set; use Cover.
func (epubParser) Metadata(ctx context.Context, src Source) (Metadata, error) {
	return epubReadMetadata(ctx, src)
}

// Cover returns the declared cover image of src, or (nil, nil) when it has none.
// A declared cover whose entry is missing or unreadable is an error.
func (epubParser) Cover(ctx context.Context, src Source) ([]byte, error) {
	return epubReadCover(ctx, src)
}

// fb2Parser reads FB2 files, including the ".fb2.zip" wrapper variant.
type fb2Parser struct{}

// Extensions returns the file extensions handled by fb2Parser.
func (fb2Parser) Extensions() []string { return []string{".fb2", extFB2Zip} }

// Traits describes the FB2 format.
func (fb2Parser) Traits() Traits { return Traits{Rank: 3, Describes: true} }

// Metadata reads only the FB2 header (or that of the first .fb2 of a .fb2.zip)
// from src. Metadata.Cover is never set; use Cover.
func (fb2Parser) Metadata(ctx context.Context, src Source) (Metadata, error) {
	return fb2ReadMetadata(ctx, src)
}

// Cover returns the declared cover image of src, or (nil, nil) when it has none.
func (fb2Parser) Cover(ctx context.Context, src Source) ([]byte, error) {
	return fb2ReadCover(ctx, src)
}

// mobiParser reads MOBI-family files (.mobi, .azw, .azw3).
type mobiParser struct{}

// Extensions returns the file extensions handled by mobiParser.
func (mobiParser) Extensions() []string { return []string{".mobi", ".azw", ".azw3"} }

// Traits describes the MOBI format.
func (mobiParser) Traits() Traits { return Traits{Rank: 2, Describes: true} }

// Metadata reads the title and EXTH block of src. Metadata.Cover is never set;
// use Cover.
func (mobiParser) Metadata(ctx context.Context, src Source) (Metadata, error) {
	return mobiReadMetadata(ctx, src)
}

// Cover returns the EXTH 201 cover image of src, or (nil, nil) when it has none.
func (mobiParser) Cover(ctx context.Context, src Source) ([]byte, error) {
	return mobiReadCover(ctx, src)
}

// pdfParser reads PDF files.
type pdfParser struct{}

// Extensions returns the file extensions handled by pdfParser.
func (pdfParser) Extensions() []string { return []string{".pdf"} }

// Traits describes the PDF format.
func (pdfParser) Traits() Traits { return Traits{Rank: 1, Describes: false} }

// Metadata reads the info dict and page count of src. Metadata.Cover is never
// set; use Cover.
func (pdfParser) Metadata(ctx context.Context, src Source) (Metadata, error) {
	return pdfReadMetadata(ctx, src)
}

// Cover returns the first usable page-1 image of src, or (nil, nil) when it has none.
func (pdfParser) Cover(ctx context.Context, src Source) ([]byte, error) {
	return pdfReadCover(ctx, src)
}
