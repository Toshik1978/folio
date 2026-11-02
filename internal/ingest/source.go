package ingest

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/ebook"
	"github.com/Toshik1978/folio/internal/libtype"
)

// errMissingEntry reports that a book file's archive no longer holds its inner
// entry.
var errMissingEntry = errors.New("ingest: archive entry not found")

// errPathEscape reports a stored source_path that resolves outside its root.
var errPathEscape = errors.New("ingest: source path escapes its root")

// resolver maps a library's book file to the ebook.Source holding its bytes. It
// is the only reading code that knows the library layout: an archive collection
// (INPX) keeps each book as an entry of a sibling archive
// ("{archive}.zip/{inner}", next to the .inpx), while every other library type
// stores files under library.Path.
type resolver struct {
	// entryLimit caps the in-memory size of an archive entry; production uses
	// ebook.MaxEntryBytes and tests lower it.
	entryLimit int64
}

// newResolver returns the production resolver.
func newResolver() resolver { return resolver{entryLimit: ebook.MaxEntryBytes} }

// open returns a read-only Source over file's bytes. Archive entries are read
// through ac, which keeps each archive open for the caller's lifetime of ac;
// the returned Closer releases only this Source. An archive entry that is gone
// returns errMissingEntry, and a source_path escaping its root returns
// errPathEscape. Nothing is ever written: entries inflate into memory.
func (r resolver) open(library dbq.Library, file dbq.BookFile, ac *archiveCache) (ebook.Source, io.Closer, error) {
	if library.Type == libtype.INPX {
		archivePath, inner, err := r.archiveEntry(library, file)
		if err != nil {
			return ebook.Source{}, nil, err
		}
		f, _, err := ac.entry(archivePath, inner)
		if err != nil {
			return ebook.Source{}, nil, err
		}
		src, closer := ebook.OpenZipEntry(f, r.entryLimit)

		return src, closer, nil
	}

	full := filepath.Join(library.Path, filepath.FromSlash(file.SourcePath))
	if !withinRoot(library.Path, full) {
		return ebook.Source{}, nil, fmt.Errorf("%w: %q", errPathEscape, file.SourcePath)
	}
	src, closer, err := ebook.OpenFile(full)
	if err != nil {
		return ebook.Source{}, nil, fmt.Errorf("open %s: %w", full, err)
	}

	return src, closer, nil
}

// fingerprint identifies the state of a book's files without reading any entry
// data: the hex of the first 16 bytes of the sha256 over one
// "source_path|stamp" line per file, in the order given (callers pass the
// reader's rank order). A plain file's stamp is its os.Stat "size|mtime_unix".
// An archive entry's is its zip header's "UncompressedSize64|Modified" followed
// by the archive's own os.Stat "|size|mtime_unix": the header alone would not
// change when a damaged archive is replaced by a good copy of the same
// release, leaving its books recorded corrupt for good. An entry missing from
// its archive, or held by an archive whose container is corrupt, stamps the
// archive's os.Stat alone. Stat and open failures are returned (retryable).
func (r resolver) fingerprint(library dbq.Library, files []dbq.BookFile, ac *archiveCache) (string, error) {
	h := sha256.New()
	for _, file := range files {
		stamp, err := r.stamp(library, file, ac)
		if err != nil {
			return "", err
		}
		_, _ = fmt.Fprintf(h, "%s|%s\n", file.SourcePath, stamp)
	}

	return hex.EncodeToString(h.Sum(nil)[:16]), nil
}

// stamp returns what file contributes to a fingerprint (see fingerprint).
func (r resolver) stamp(library dbq.Library, file dbq.BookFile, ac *archiveCache) (string, error) {
	if library.Type != libtype.INPX {
		full := filepath.Join(library.Path, filepath.FromSlash(file.SourcePath))
		if !withinRoot(library.Path, full) {
			return "", fmt.Errorf("%w: %q", errPathEscape, file.SourcePath)
		}
		fi, serr := os.Stat(full)
		if serr != nil {
			return "", fmt.Errorf("stat %s: %w", full, serr)
		}

		return statStamp(fi), nil
	}

	archivePath, inner, err := r.archiveEntry(library, file)
	if err != nil {
		return "", err
	}
	f, archive, err := ac.entry(archivePath, inner)
	switch {
	case err == nil:
		return fmt.Sprintf("%d|%d|%s", f.UncompressedSize64, f.Modified.Unix(), statStamp(archive)), nil
	case errors.Is(err, errMissingEntry), errors.Is(err, ebook.ErrMalformed):
		fi, serr := os.Stat(archivePath)
		if serr != nil {
			return "", fmt.Errorf("stat archive %s: %w", archivePath, serr)
		}

		return statStamp(fi), nil
	default:
		return "", err
	}
}

// group returns the key that books sharing an open archive have in common: the
// archive path for an archive-backed file, and "" for a plain file (or a
// source_path that cannot be resolved).
func (r resolver) group(library dbq.Library, file dbq.BookFile) string {
	if library.Type != libtype.INPX {
		return ""
	}
	archivePath, _, err := r.archiveEntry(library, file)
	if err != nil {
		return ""
	}

	return archivePath
}

// archiveEntry splits an archive-backed source_path into the sibling archive's
// path and the inner entry name.
func (resolver) archiveEntry(library dbq.Library, file dbq.BookFile) (archivePath, inner string, err error) {
	archiveName, inner, found := strings.Cut(file.SourcePath, "/")
	if !found || archiveName == "" || inner == "" {
		return "", "", fmt.Errorf("malformed archive source path %q", file.SourcePath)
	}
	root := filepath.Dir(library.Path)
	archivePath = filepath.Join(root, filepath.FromSlash(archiveName))
	if !withinRoot(root, archivePath) {
		return "", "", fmt.Errorf("%w: %q", errPathEscape, file.SourcePath)
	}

	return archivePath, inner, nil
}

// withinRoot reports whether full names a path strictly inside root, so a
// stored source_path such as "../x.fb2" cannot reach outside the library.
func withinRoot(root, full string) bool {
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == "." || filepath.IsAbs(rel) {
		return false
	}

	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// archiveCache opens each archive at most once for its owner and keeps it open
// until close. An archive is read as os.Open → ebook.TagIO → zip.NewReader, never
// zip.OpenReader, so a disk read error while an entry inflates matches
// ebook.ErrSourceIO (retryable) instead of being taken for a corrupt entry. A
// container zip.NewReader rejects for any other reason matches
// ebook.ErrMalformed (broken corrupt). It
// is not safe for concurrent use; each caller owns one.
type archiveCache struct {
	archives map[string]*cachedArchive

	// onOpen, when set, is called with the path of every archive the cache
	// opens; tests count opens through it.
	onOpen func(path string)
}

// cachedArchive is one opened archive (or the error that opening it returned).
type cachedArchive struct {
	file    *os.File
	info    os.FileInfo
	entries map[string]*zip.File
	err     error
}

// newArchiveCache returns an empty cache.
func newArchiveCache() *archiveCache {
	return &archiveCache{archives: map[string]*cachedArchive{}}
}

// entry returns the inner entry of the archive at path, and the archive's
// os.Stat, opening the archive on first use. A missing entry returns
// errMissingEntry.
func (c *archiveCache) entry(path, inner string) (*zip.File, os.FileInfo, error) {
	a, ok := c.archives[path]
	if !ok {
		if c.onOpen != nil {
			c.onOpen(path)
		}
		a = openArchive(path)
		c.archives[path] = a
	}
	if a.err != nil {
		return nil, nil, a.err
	}
	f, ok := a.entries[inner]
	if !ok {
		return nil, nil, fmt.Errorf("%w: %s in %s", errMissingEntry, inner, path)
	}

	return f, a.info, nil
}

// close closes every archive the cache opened.
func (c *archiveCache) close() {
	for _, a := range c.archives {
		if a.file != nil {
			_ = a.file.Close()
		}
	}
	clear(c.archives)
}

// openArchive opens path read-only and indexes its entries by name.
func openArchive(path string) *cachedArchive {
	f, err := os.Open(path)
	if err != nil {
		return &cachedArchive{err: fmt.Errorf("open archive %s: %w", path, err)}
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return &cachedArchive{err: fmt.Errorf("stat archive %s: %w", path, err)}
	}
	zr, err := zip.NewReader(ebook.TagIO(f), fi.Size())
	if err != nil {
		_ = f.Close()
		if !errors.Is(err, ebook.ErrSourceIO) {
			// The container itself is damaged: broken, not a passing I/O fault.
			err = fmt.Errorf("%w: %w", ebook.ErrMalformed, err)
		}

		return &cachedArchive{err: fmt.Errorf("read archive %s: %w", path, err)}
	}
	entries := make(map[string]*zip.File, len(zr.File))
	for _, zf := range zr.File {
		if _, dup := entries[zf.Name]; !dup {
			entries[zf.Name] = zf
		}
	}

	return &cachedArchive{file: f, info: fi, entries: entries}
}

// statStamp is the "size|mtime_unix" a stat'ed file contributes to a fingerprint.
func statStamp(fi os.FileInfo) string {
	return fmt.Sprintf("%d|%d", fi.Size(), fi.ModTime().Unix())
}
