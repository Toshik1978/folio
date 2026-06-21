package ingest

import (
	"archive/zip"
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/samber/lo"

	"github.com/Toshik1978/folio/internal/db/dbq"
)

// INPXParser imports an INPX inventory: a ZIP bundle of ".inp" index files. Each
// ".inp" line describes one book whose content lives in a sibling ".zip"
// archive next to the .inpx. Metadata comes entirely from the index — no book
// archive is unzipped here. source_path is stored as "{archive}.zip/{inner}"
// so downloads can later seek directly into the ZIP.
type INPXParser struct {
	log *slog.Logger
}

// INP field indices (MyHomeLib / flibusta layout).
const (
	inpAuthor = iota
	inpGenre
	inpTitle
	inpSeries
	inpSerNo
	inpFile
	inpSize
	inpLibID
	inpDel
	inpExt
	inpDate
	inpLang
)

// inpMinFields is the field count needed to import a record. Trailing fields
// (date, lang, rating, keywords) are optional and may be absent, so only the
// fields up to EXT are required.
const inpMinFields = inpExt + 1

// NewINPXParser builds the INPX inventory parser.
func NewINPXParser(log *slog.Logger) *INPXParser { return &INPXParser{log} }

// Checkpoint fingerprints the .inpx bundle, which is regenerated wholesale, so
// the engine can skip an unchanged inventory.
func (*INPXParser) Checkpoint(library dbq.Library) (string, error) {
	return fileCheckpoint(library.Path)
}

func (*INPXParser) Sync(
	ctx context.Context,
	library dbq.Library,
	db *sql.DB,
	covers CoverStore,
	r Reporter,
) (Result, error) {
	zr, err := zip.OpenReader(library.Path)
	if err != nil {
		return Result{}, fmt.Errorf("open inpx: %w", err)
	}
	defer func() { _ = zr.Close() }()

	return runReconcile(ctx, db, covers, library, r, func(ctx context.Context, rc *reconciler) error {
		for _, f := range zr.File {
			if !strings.EqualFold(filepath.Ext(f.Name), ".inp") {
				continue
			}
			if err := ingestINP(ctx, rc, f, library.ID); err != nil {
				return err
			}
		}

		return nil
	})
}

// ingestINP reads one ".inp" index file and upserts each of its book records.
func ingestINP(ctx context.Context, recon *reconciler, f *zip.File, libraryID int64) error {
	archive := strings.TrimSuffix(filepath.Base(f.Name), filepath.Ext(f.Name)) + ".zip"

	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("open %s: %w", f.Name, err)
	}
	data, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		return fmt.Errorf("read %s: %w", f.Name, err)
	}

	for line := range strings.SplitSeq(string(data), "\n") {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("inpx import canceled: %w", err)
		}
		rec, ok := parseINPLine(line, libraryID, archive)
		if !ok {
			continue
		}
		if err := recon.upsert(ctx, rec); err != nil {
			return err
		}
	}

	return nil
}

// parseINPLine parses one ".inp" record. It returns ok=false for blank lines,
// malformed records, and entries flagged as deleted.
func parseINPLine(line string, libraryID int64, archive string) (bookRecord, bool) {
	line = strings.TrimRight(line, "\r\x04")
	if line == "" {
		return bookRecord{}, false
	}

	fields := strings.Split(line, "\x04")
	if len(fields) < inpMinFields {
		return bookRecord{}, false
	}
	if strings.TrimSpace(fields[inpDel]) == "1" {
		return bookRecord{}, false
	}

	ext := strings.ToLower(strings.TrimSpace(fields[inpExt]))
	file := strings.TrimSpace(fields[inpFile])
	if ext == "" || file == "" {
		return bookRecord{}, false
	}

	title := lo.CoalesceOrEmpty(strings.TrimSpace(fields[inpTitle]), file)

	authors := parseINPAuthors(fields[inpAuthor])
	lang := normalizeLang(inpOptional(fields, inpLang))

	rec := bookRecord{
		LibraryID:      libraryID,
		LibraryKey:     groupKey(title, authors, lang),
		DeriveIdentity: true,
		Title:          title,
		Authors:        authors,
		Genres:         splitColon(fields[inpGenre]),
		Series:         strings.TrimSpace(fields[inpSeries]),
		Language:       lang,
		SourcePath:     archive + "/" + file + "." + ext,
		FileFormat:     ext,
		FileSize:       parseInt64(fields[inpSize]),
	}
	if n, err := strconv.ParseFloat(strings.TrimSpace(fields[inpSerNo]), 64); err == nil && n != 0 {
		rec.SeriesNumber = nullFloat(n, true)
	}
	rec.AddedAt = parseINPXDate(inpOptional(fields, inpDate))

	return rec, true
}

// inpOptional returns the trimmed value of an optional trailing field, or "" when
// the record is too short to include it.
func inpOptional(fields []string, idx int) string {
	if idx >= len(fields) {
		return ""
	}

	return strings.TrimSpace(fields[idx])
}

// inpDateFormats are the date layouts seen in INP records. Exporters disagree on
// column order, so Y-M-D is tried first and Y-D-M second. Go rejects month > 12,
// so a value like 2021-31-05 unambiguously falls through to Y-D-M. A value where
// both fields are <= 12 (e.g. 2021-05-06) is inherently ambiguous and parses as
// Y-M-D — a known, unresolvable limitation that can silently skew added_at (and
// thus the "Newest" sort) for a Y-D-M source. No error is raised.
var inpDateFormats = []string{"2006-01-02", "2006-02-01"}

// parseINPXDate parses an INP date column to a unix timestamp; 0 when blank or
// unrecognized.
func parseINPXDate(str string) int64 {
	str = strings.TrimSpace(str)
	if str == "" {
		return 0
	}
	for _, f := range inpDateFormats {
		if t, err := time.Parse(f, str); err == nil {
			return t.Unix()
		}
	}

	return 0
}

// parseINPAuthors splits the ":"-separated author list and reorders each
// "Last,First,Middle" tuple into "First Middle Last".
func parseINPAuthors(field string) []string {
	var out []string
	for _, a := range splitColon(field) {
		parts := strings.Split(a, ",")
		var names []string
		for i := 1; i < len(parts); i++ { // first, middle...
			if p := strings.TrimSpace(parts[i]); p != "" {
				names = append(names, p)
			}
		}
		if last := strings.TrimSpace(parts[0]); last != "" { // ...last
			names = append(names, last)
		}
		if name := strings.Join(names, " "); name != "" {
			out = append(out, name)
		}
	}

	return out
}

func splitColon(field string) []string {
	var out []string
	for p := range strings.SplitSeq(field, ":") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}

	return out
}

func parseInt64(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}
