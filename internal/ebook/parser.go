package ebook

import (
	"context"
	"strings"
)

// Parser extracts metadata from the file formats it declares via Extensions.
// Each implementation owns one logical format (which may map to several
// extensions, e.g. MOBI owns .mobi/.azw/.azw3). The Dispatcher, not the Parser,
// derives the stored format label, so adding extensions here never changes
// book_files.file_format semantics.
type Parser interface {
	// Extensions returns the lower-case extension keys this parser owns,
	// including the leading dot and any multi-dot wrapper (e.g. ".fb2.zip").
	Extensions() []string
	// Parse extracts metadata from a single file. Panic recovery and debug
	// logging are the Dispatcher's responsibility; Parse stays pure.
	Parse(ctx context.Context, path string) (Metadata, error)
}

// EPUBParser parses EPUB files.
type EPUBParser struct{}

// NewEPUB returns the EPUB format parser.
func NewEPUB() EPUBParser { return EPUBParser{} }

// Extensions returns the file extensions handled by EPUBParser.
func (EPUBParser) Extensions() []string { return []string{".epub"} }

// Parse extracts metadata from an EPUB file.
func (EPUBParser) Parse(ctx context.Context, p string) (Metadata, error) { return parseEPUB(ctx, p) }

// FB2Parser parses FB2 files, including the ".fb2.zip" wrapper variant.
type FB2Parser struct{}

// NewFB2 returns the FB2 parser, which owns both plain ".fb2" and the
// ".fb2.zip" wrapper and routes between them internally.
func NewFB2() FB2Parser { return FB2Parser{} }

// Extensions returns the file extensions handled by FB2Parser.
func (FB2Parser) Extensions() []string { return []string{".fb2", extFB2Zip} }

// Parse extracts metadata from an FB2 or FB2.ZIP file.
func (FB2Parser) Parse(ctx context.Context, p string) (Metadata, error) {
	if strings.HasSuffix(strings.ToLower(p), extFB2Zip) {
		return parseFB2Zip(ctx, p)
	}
	return parseFB2(ctx, p)
}

// MOBIParser parses MOBI-family files (.mobi, .azw, .azw3).
type MOBIParser struct{}

// NewMOBI returns the MOBI-family parser (.mobi/.azw/.azw3).
func NewMOBI() MOBIParser { return MOBIParser{} }

// Extensions returns the file extensions handled by MOBIParser.
func (MOBIParser) Extensions() []string { return []string{".mobi", ".azw", ".azw3"} }

// Parse extracts metadata from a MOBI-family file.
func (MOBIParser) Parse(ctx context.Context, p string) (Metadata, error) { return parseMOBI(ctx, p) }

// PDFParser parses PDF files.
type PDFParser struct{}

// NewPDF returns the PDF parser.
func NewPDF() PDFParser { return PDFParser{} }

// Extensions returns the file extensions handled by PDFParser.
func (PDFParser) Extensions() []string { return []string{".pdf"} }

// Parse extracts metadata from a PDF file.
func (PDFParser) Parse(ctx context.Context, p string) (Metadata, error) { return parsePDF(ctx, p) }
