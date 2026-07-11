package ebook

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	pdfcpuapi "github.com/pdfcpu/pdfcpu/pkg/api"
	pdfcpucore "github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func parsePDF(ctx context.Context, path string) (Metadata, error) {
	f, err := os.Open(path)
	if err != nil {
		return Metadata{}, fmt.Errorf("open pdf: %w", err)
	}
	defer f.Close()

	// pdfcpu's ReadContext has no context hook and can be slow on a large or
	// hostile file, so guard the two heavy phases (this read and the cover
	// extraction below) with a cancellation check on either side.
	if err = ctx.Err(); err != nil {
		return Metadata{}, fmt.Errorf("read pdf: %w", err)
	}
	doc, err := pdfcpuapi.ReadContext(f, model.NewDefaultConfiguration())
	if err != nil {
		return Metadata{}, fmt.Errorf("read pdf: %w", err)
	}

	m := Metadata{Pages: doc.PageCount}

	if doc.Info == nil {
		return m, nil
	}
	infoDict, err := doc.DereferenceDict(*doc.Info)
	if err != nil {
		return m, nil // missing info dict is not fatal
	}

	m.Title = pdfString(doc.XRefTable, infoDict, "Title")
	m.Authors = pdfAuthors(doc.XRefTable, infoDict)
	m.Annotation = pdfString(doc.XRefTable, infoDict, "Subject")
	m.Year = ParseYear(pdfString(doc.XRefTable, infoDict, "CreationDate"))

	m.Cover = coverFromContext(ctx, doc)

	return m, nil
}

// coverFromContext extracts a usable cover from page 1 of an already-parsed PDF.
// Digital books almost always embed their cover as the page-1 image, so this
// avoids pulling in a heavy vector renderer. It reuses ctx — no second parse of
// the file — and skips image kinds that are never covers (stencil masks, page
// thumbnails).
//
// Known limitation: tiny decorative images (rules, logos) are not skipped.
// pdfcpu 0.12.1 reports Width/Height as 0 on extracted images, so a
// declared-dimension floor isn't usable here; a real filter would have to
// decode and measure the image.
func coverFromContext(ctx context.Context, doc *model.Context) []byte {
	// Validate+Optimize+ExtractPageImages is the second heavy pdfcpu phase; skip
	// it when the caller has already been cancelled.
	if ctx.Err() != nil {
		return nil
	}

	// ExtractPageImages needs a validated, optimized cross-reference table.
	// We run those passes on the context we already read (rather than re-parsing
	// the file): if either fails we simply return no cover, while the text
	// metadata pulled from the lenient read above is preserved by the caller.
	if err := pdfcpuapi.ValidateContext(doc); err != nil {
		return nil
	}
	if err := pdfcpuapi.OptimizeContext(doc); err != nil {
		return nil
	}
	imgs, err := pdfcpucore.ExtractPageImages(doc, 1, false)
	if err != nil {
		return nil
	}

	return firstUsableCover(imgs)
}

// firstUsableCover returns the bytes of the first page-1 image that is a real
// cover candidate (skipping stencil masks and page thumbnails), chosen
// deterministically by ascending object number so a randomized map iteration
// order cannot pick a different image run to run.
func firstUsableCover(imgs map[int]model.Image) []byte {
	objNrs := make([]int, 0, len(imgs))
	for objNr := range imgs {
		objNrs = append(objNrs, objNr)
	}
	slices.Sort(objNrs)

	for _, objNr := range objNrs {
		img := imgs[objNr]
		if img.IsImgMask || img.Thumb {
			continue
		}
		if data, err := io.ReadAll(img); err == nil && len(data) > 0 {
			return data
		}
	}

	return nil
}

func pdfString(xref *model.XRefTable, d types.Dict, key string) string {
	obj, found := d.Find(key)
	if !found {
		return ""
	}
	s, err := xref.DereferenceStringOrHexLiteral(obj, model.V10, nil)
	if err != nil {
		return ""
	}

	return strings.TrimSpace(s)
}

func pdfAuthors(xref *model.XRefTable, d types.Dict) []string {
	if author := pdfString(xref, d, "Author"); author != "" {
		return []string{author}
	}
	return nil
}
