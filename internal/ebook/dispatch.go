package ebook

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
)

// Dispatcher routes a file to the registered Parser that owns its extension.
// It replaces the former package-global parser registry: the composition root
// builds one Dispatcher from an explicit parser list and injects it.
type Dispatcher struct {
	parsers map[string]Parser
}

// NewDispatcher builds a Dispatcher, indexing each parser by every extension it
// declares. It panics on a duplicate extension — a composition-root programming
// error caught at startup (consistent with config.MustParse).
func NewDispatcher(parsers ...Parser) *Dispatcher {
	m := make(map[string]Parser)
	for _, p := range parsers {
		for _, ext := range p.Extensions() {
			if _, dup := m[ext]; dup {
				panic(fmt.Sprintf("ebook: duplicate parser for extension %q", ext))
			}
			m[ext] = p
		}
	}

	return &Dispatcher{parsers: m}
}

// Parse selects the parser for path's extension and runs it. Parsers run on
// untrusted files; pdfcpu in particular can panic on malformed input. Recover so
// one bad file becomes a skippable error instead of crashing the background
// sync/warm goroutines (HTTP handlers are already shielded by net/http's
// per-request recover).
func (d *Dispatcher) Parse(ctx context.Context, log *slog.Logger, path string) (meta Metadata, err error) {
	// Bail before touching the file when the caller (a background sync/warm
	// goroutine) is already cancelled, so a shutdown drains the parse queue
	// promptly instead of parsing every remaining file first.
	if err = ctx.Err(); err != nil {
		return Metadata{}, fmt.Errorf("parse %s: %w", path, err)
	}

	ext := d.fileExt(path)
	p, ok := d.parsers[ext]
	if !ok {
		return Metadata{}, fmt.Errorf("unsupported format: %s", ext)
	}
	defer func() {
		if r := recover(); r != nil {
			meta, err = Metadata{}, fmt.Errorf("parse %s panicked: %v", path, r)
		}
	}()

	log.Debug("--> parse book", slog.String("path", path))
	defer log.Debug("<-- parsed book", slog.String("path", path))

	meta, err = p.Parse(ctx, path)
	if err != nil {
		// The dispatcher contributes the format; callers add the file path, so
		// don't restate it here (avoids a doubled path in propagated errors).
		return Metadata{}, fmt.Errorf("%s parser: %w", d.Format(path), err)
	}

	return meta, nil
}

// Supported reports whether path has an extension a registered parser can parse.
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
	ext = strings.TrimPrefix(ext, ".")
	if ext == "fb2.zip" {
		return FormatFB2
	}

	return ext
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
