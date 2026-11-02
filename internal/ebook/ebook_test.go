package ebook

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode"

	"github.com/stretchr/testify/suite"
)

// TestEbook is the package's single entry point; every suite is registered here.
func TestEbook(t *testing.T) {
	suite.Run(t, new(epubSuite))
	suite.Run(t, new(fb2Suite))
	suite.Run(t, new(mobiSuite))
	suite.Run(t, new(pdfSuite))
	suite.Run(t, new(dispatchSuite))
	suite.Run(t, new(metadataSuite))
	suite.Run(t, new(errorsSuite))
	suite.Run(t, new(sourceSuite))
	suite.Run(t, new(ebookSuite))
}

// baseSuite resolves fixture paths under testdata/.
type baseSuite struct {
	suite.Suite

	log *slog.Logger
	d   *Dispatcher
}

func (s *baseSuite) SetupTest() {
	s.log = slog.New(slog.DiscardHandler)
	s.d = NewStandardDispatcher()
}

func (s *baseSuite) fixture(name string) string {
	return filepath.Join("testdata", name)
}

// open opens path as a Source, closed at test cleanup.
func (s *baseSuite) open(path string) Source {
	src, c, err := OpenFile(path)
	s.Require().NoError(err)
	s.T().Cleanup(func() { _ = c.Close() })

	return src
}

// readMetadata reads path's metadata through the standard dispatcher.
func (s *baseSuite) readMetadata(path string) Metadata {
	m, err := s.d.Metadata(context.Background(), s.log, s.open(path))
	s.Require().NoError(err)

	return m
}

// readCover reads path's cover through the standard dispatcher.
func (s *baseSuite) readCover(path string) []byte {
	c, err := s.d.Cover(context.Background(), s.log, s.open(path))
	s.Require().NoError(err)

	return c
}

// peakHeapBytes runs fn while sampling the live heap and returns the peak heap
// growth observed. Peak live heap — not cumulative allocation — is what the OOM
// killer measures, so this is the metric that reflects the container crash:
// loading a file whole keeps one large buffer live for the entire read, whereas
// streaming never does. The signal we assert on (tens of MB live vs. a few KB)
// dwarfs sampling jitter.
func (s *baseSuite) peakHeapBytes(fn func()) uint64 {
	runtime.GC() //revive:disable:call-to-gc // settle the heap for a clean peak-growth baseline
	var base runtime.MemStats
	runtime.ReadMemStats(&base)

	var peak atomic.Uint64
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		var ms runtime.MemStats
		for {
			runtime.ReadMemStats(&ms)
			for {
				cur := peak.Load()
				if ms.HeapAlloc <= cur || peak.CompareAndSwap(cur, ms.HeapAlloc) {
					break
				}
			}
			select {
			case <-done:
				return
			case <-time.After(100 * time.Microsecond):
			}
		}
	}()

	fn()
	close(done)
	<-stopped

	if p := peak.Load(); p > base.HeapAlloc {
		return p - base.HeapAlloc
	}

	return 0
}

// ebookSuite holds package-wide guards over the ebook public surface.
type ebookSuite struct {
	suite.Suite
}

// formatWords are the format labels and extensions no exported identifier of
// package ebook may contain as a camelCase word.
var formatWords = map[string]bool{ //nolint:gochecknoglobals // read-only lookup table
	"epub": true, "fb2": true, "fbz": true, "mobi": true, "azw": true, "azw3": true, "pdf": true,
}

// TestPublicSurfaceFormatAgnostic keeps format names out of the exported API:
// callers reach every format through the Dispatcher, never by naming one.
func (s *ebookSuite) TestPublicSurfaceFormatAgnostic() {
	paths, err := filepath.Glob("*.go")
	s.Require().NoError(err)

	fset := token.NewFileSet()
	var violations []string
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		s.Require().NoError(err, path)
		violations = append(violations, exportedFormatNames(fset, file)...)
	}

	s.Empty(violations, "exported identifiers of package ebook must not name a format")
}

// TestPublicSurfaceFormatAgnosticSelfTest proves the guard reports exported
// format-named functions, types, constants and methods of exported types, and
// ignores unexported identifiers and methods of unexported types.
func (s *ebookSuite) TestPublicSurfaceFormatAgnosticSelfTest() {
	const src = `package x

type Reader struct{}
type pdfParser struct{}

func NewPDF() {}
func (Reader) EPUBCover() {}
func (*Reader) Metadata() {}
func (pdfParser) PDFTraits() {}
func newPDF() {}

type FB2Parser struct{}

const FormatAZW3 = "azw3"

var LooksLikeISBN, MaxEntryBytes int
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "x.go", src, parser.SkipObjectResolution)
	s.Require().NoError(err)

	got := exportedFormatNames(fset, file)
	s.Require().Len(got, 4, strings.Join(got, "\n"))
	joined := strings.Join(got, "\n")
	s.Contains(joined, "NewPDF")
	s.Contains(joined, "Reader.EPUBCover")
	s.Contains(joined, "FB2Parser")
	s.Contains(joined, "FormatAZW3")
}

// exportedFormatNames returns "file:line: name" for every exported top-level
// type, func, const or var, and every method of an exported type, whose
// camelCase words include a format label or extension.
func exportedFormatNames(fset *token.FileSet, file *ast.File) []string {
	var out []string
	for _, n := range exportedNames(file) {
		for _, w := range camelWords(n.ident.Name) {
			if formatWords[strings.ToLower(w)] {
				out = append(out, fmt.Sprintf("%s: %s%s", fset.Position(n.ident.Pos()), n.qualifier, n.ident.Name))
				break
			}
		}
	}

	return out
}

// exportedName is one exported identifier; qualifier is "T." for a method of T.
type exportedName struct {
	ident     *ast.Ident
	qualifier string
}

// exportedNames collects the exported top-level identifiers of file and the
// exported methods of its exported types.
func exportedNames(file *ast.File) []exportedName {
	var out []exportedName
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if n, ok := exportedFunc(d); ok {
				out = append(out, n)
			}
		case *ast.GenDecl:
			out = append(out, exportedSpecs(d)...)
		}
	}

	return out
}

// exportedFunc reports an exported function, or an exported method of an
// exported type.
func exportedFunc(d *ast.FuncDecl) (exportedName, bool) {
	if !d.Name.IsExported() {
		return exportedName{}, false
	}
	if d.Recv == nil {
		return exportedName{ident: d.Name}, true
	}
	recv := receiverTypeName(d.Recv.List[0].Type)

	return exportedName{ident: d.Name, qualifier: recv + "."}, ast.IsExported(recv)
}

// exportedSpecs returns the exported type, const and var names of d.
func exportedSpecs(d *ast.GenDecl) []exportedName {
	var out []exportedName
	for _, spec := range d.Specs {
		var idents []*ast.Ident
		switch sp := spec.(type) {
		case *ast.TypeSpec:
			idents = []*ast.Ident{sp.Name}
		case *ast.ValueSpec:
			idents = sp.Names
		}
		for _, id := range idents {
			if id.IsExported() {
				out = append(out, exportedName{ident: id})
			}
		}
	}

	return out
}

// receiverTypeName returns the base type name of a method receiver
// (T, *T, T[P] or *T[P]).
func receiverTypeName(expr ast.Expr) string {
	for {
		switch e := expr.(type) {
		case *ast.StarExpr:
			expr = e.X
		case *ast.IndexExpr:
			expr = e.X
		case *ast.IndexListExpr:
			expr = e.X
		case *ast.Ident:
			return e.Name
		default:
			return ""
		}
	}
}

// camelWords splits an identifier into its camelCase words. A run of capitals
// is one word (its last capital starts the next word when a lower-case letter
// follows), and digits stay with the word they follow: "ParseFB2Description"
// is Parse, FB2, Description; "FormatAZW3" is Format, AZW3.
func camelWords(name string) []string {
	var words []string
	runes := []rune(name)
	start := 0
	for i := 1; i < len(runes); i++ {
		prev, cur := runes[i-1], runes[i]
		next := rune(0)
		if i+1 < len(runes) {
			next = runes[i+1]
		}
		switch {
		case cur == '_':
			words = append(words, string(runes[start:i]))
			start = i + 1
			continue
		case !unicode.IsUpper(cur):
			continue
		case unicode.IsLower(prev), unicode.IsDigit(prev),
			unicode.IsUpper(prev) && unicode.IsLower(next):
			words = append(words, string(runes[start:i]))
			start = i
		}
	}
	words = append(words, string(runes[start:]))

	return slices.DeleteFunc(words, func(w string) bool { return w == "" })
}
