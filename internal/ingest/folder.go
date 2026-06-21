package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/samber/lo"

	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/ebook"
)

// FolderParser imports books from an arbitrary directory tree. It diffs the current
// filesystem against the database by relative path + file size, parsing
// metadata only for new or changed files and pruning books whose files have
// disappeared.
type FolderParser struct {
	log    *slog.Logger
	parser bookParser
}

// NewFolderParser builds the plain-folder parser.
func NewFolderParser(log *slog.Logger, parser bookParser) *FolderParser { //nolint:iface
	return &FolderParser{log: log, parser: parser}
}

func (f *FolderParser) Sync(
	ctx context.Context,
	library dbq.Library,
	db *sql.DB,
	covers CoverStore,
	r Reporter,
) (Result, error) {
	return runReconcile(ctx, db, covers, library, r, func(ctx context.Context, rc *reconciler) error {
		w := &folderWalk{log: f.log, parser: f.parser, rc: rc, root: library.Path, libraryID: library.ID}
		if err := filepath.WalkDir(library.Path, func(path string, d fs.DirEntry, err error) error {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return fmt.Errorf("folder walk canceled: %w", ctxErr)
			}
			return w.visit(ctx, path, d, err)
		}); err != nil {
			return fmt.Errorf("walk library %s: %w", library.Path, err)
		}

		return nil
	})
}

// folderWalk carries the state threaded through a directory scan.
type folderWalk struct {
	log       *slog.Logger
	parser    bookParser
	rc        *reconciler
	root      string
	libraryID int64
}

// visit reconciles one directory entry: unchanged files (known path + same size)
// are marked seen without re-parsing; new or changed files are parsed and upserted.
func (w *folderWalk) visit(ctx context.Context, path string, d fs.DirEntry, err error) error {
	if err != nil {
		return err
	}
	if d.IsDir() || !w.parser.Supported(path) {
		return nil
	}

	rel, err := filepath.Rel(w.root, path)
	if err != nil {
		return fmt.Errorf("relativize %s: %w", path, err)
	}
	info, err := d.Info()
	if err != nil {
		return fmt.Errorf("file info %s: %w", path, err)
	}

	mtime := info.ModTime().Unix()
	if prev, ok := w.rc.prev[rel]; ok && prev.FileSize == info.Size() && prev.Mtime == mtime {
		w.rc.markSeen(rel) // unchanged size and mod time: do not re-parse
		return nil
	}

	meta, err := w.parser.Parse(ctx, w.log, path)
	if err != nil {
		return nil //nolint:nilerr // skip unparseable files, keep walking the tree
	}

	return w.rc.upsert(ctx, recordFromMeta(w.libraryID, rel, path, info.Size(), mtime, w.parser.Format(path), meta))
}

// fromEbookIdentifiers converts parser identifiers into importer records.
func fromEbookIdentifiers(ids []ebook.Identifier) []identifier {
	if len(ids) == 0 {
		return nil
	}
	out := make([]identifier, 0, len(ids))
	for _, id := range ids {
		out = append(out, identifier{Type: id.Type, Value: id.Value})
	}

	return out
}

func recordFromMeta(
	libraryID int64, rel, path string, size, mtime int64, format string, meta ebook.Metadata,
) bookRecord {
	title := lo.CoalesceOrEmpty(
		strings.TrimSpace(meta.Title),
		strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)),
	)
	lang := normalizeLang(meta.Language)

	return bookRecord{
		LibraryID:      libraryID,
		LibraryKey:     groupKey(title, meta.Authors, lang),
		DeriveIdentity: true,
		Title:          title,
		Authors:        meta.Authors,
		Genres:         meta.Genres,
		Annotation:     meta.Annotation,
		Series:         meta.Series,
		SeriesNumber:   nullFloat(meta.SeriesNumber, meta.SeriesNumber != 0),
		Language:       lang,
		Publisher:      meta.Publisher,
		Year:           meta.Year,
		Pages:          meta.Pages,
		Identifiers:    fromEbookIdentifiers(meta.Identifiers),
		SourcePath:     rel,
		FileFormat:     format,
		FileSize:       size,
		Mtime:          mtime,
		// A plain folder has no recorded add-timestamp, so the file mod time is
		// the best chronological proxy. Without it added_at falls back to the
		// sync-run time, pinning every folder book to the top of "Newest".
		AddedAt: mtime,
		Cover:   meta.Cover,
	}
}
