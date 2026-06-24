package covers

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
)

// CoverExtractor lazily produces a book's cover bytes from its source file on a
// cache miss. The concrete implementation (in the ingest package) parses ebook
// files; covers stays a leaf package by depending only on this interface.
type CoverExtractor interface {
	Cover(ctx context.Context, bookID int64) (data []byte, ok bool, err error)
}

type Store struct {
	dir       string
	extractor CoverExtractor
}

// NewStore opens (creating if needed) the cover cache under dataDir. extractor
// enables lazy extraction on cache misses; pass nil to fall back to the
// placeholder for every miss.
func NewStore(dataDir string, extractor CoverExtractor) (*Store, error) {
	dir := filepath.Join(dataDir, "covers")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create covers directory: %w", err)
	}
	return &Store{dir: dir, extractor: extractor}, nil
}

// Save transcodes data to JPEG (a no-op for already-JPEG bytes) and caches it,
// so every cover on disk is a JPEG and serving never has to sniff the type.
// Identical bytes are not rewritten: a rewrite bumps the file mtime, which is
// part of the ?v= cache-buster, so re-saving an unchanged cover would needlessly
// invalidate every client's cached copy.
func (s *Store) Save(bookID int64, data []byte) error {
	jpegData, err := convertToJPEG(data)
	if err != nil {
		return fmt.Errorf("save as jpeg: %w", err)
	}
	if existing, rerr := os.ReadFile(s.Path(bookID)); rerr == nil && bytes.Equal(existing, jpegData) {
		return nil
	}

	return s.writeFile(bookID, jpegData)
}

func (s *Store) Delete(bookID int64) error {
	if err := os.Remove(s.Path(bookID)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove cover %d: %w", bookID, err)
	}
	if err := os.Remove(s.ThumbPath(bookID)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove thumbnail %d: %w", bookID, err)
	}

	return nil
}

// ThumbToken identifies the thumbnail rendering spec (max dimension + quality) for
// the ?v= cache buster. It changes when those constants change, so a thumbnail URL
// keyed on it invalidates every client's cached thumbnail when the rendering
// parameters change — even though the cover bytes, and thus Version, are unchanged.
func (s *Store) ThumbToken() string {
	return fmt.Sprintf("t%dq%d", thumbMaxDim, thumbQuality)
}

// Version returns the cover-file component of the ?v= cache buster: the cached
// file's mtime (unix), or "0" when no file exists yet. Combined with the
// metadata content_hash it changes whenever the cover *bytes* change — covering
// a placeholder later upgraded to a real cover and a better edition's cover
// saved by a later sync, neither of which touches the metadata hash.
func (s *Store) Version(bookID int64) string {
	info, err := os.Stat(s.Path(bookID))
	if err != nil {
		return "0"
	}

	return strconv.FormatInt(info.ModTime().Unix(), 10)
}

// Has reports whether a cover is already cached for bookID.
func (s *Store) Has(bookID int64) bool {
	_, err := os.Stat(s.Path(bookID))
	return err == nil
}

// HasLocalCover reports whether bookID has a real cover sourced from the book's
// own library files — either a non-placeholder cover already cached, or one the
// extractor can pull from the source (e.g. a PDF's page-1 render). It lets
// callers keep a good local cover instead of overwriting it with a lower-quality
// online cover (a Google Books thumbnail). A cached placeholder does not count.
func (s *Store) HasLocalCover(ctx context.Context, bookID int64) bool {
	if data, err := os.ReadFile(s.Path(bookID)); err == nil {
		return !bytes.Equal(data, placeholderJPEG)
	}
	if s.extractor == nil {
		return false
	}
	data, ok, err := s.extractor.Cover(ctx, bookID)

	return err == nil && ok && len(data) > 0
}

func (s *Store) Path(bookID int64) string {
	return filepath.Join(s.shardDir(bookID), strconv.FormatInt(bookID, 10)+".jpeg")
}

// ThumbPath returns the on-disk path of bookID's cached thumbnail, sharded
// identically to Path.
func (s *Store) ThumbPath(bookID int64) string {
	return filepath.Join(s.shardDir(bookID), strconv.FormatInt(bookID, 10)+".thumb.jpeg")
}

func (s *Store) ServeCover(w http.ResponseWriter, r *http.Request, bookID int64) {
	// Real covers are content-addressed by the caller's ?v=<hash>-<mtime> buster
	// and served immutable. Placeholder responses are served no-cache instead: a
	// real cover may appear later (lazy extraction, a better edition's sync)
	// without the URL changing for clients that already cached the placeholder.
	path := s.Path(bookID)
	if info, err := os.Stat(path); err == nil {
		s.serveCached(w, r, path, info.Size())
		return
	}
	if data, ok := s.lazyExtract(r.Context(), bookID); ok {
		s.serveBytes(w, data)
		return
	}
	// No cached cover and no extractor configured to produce one.
	s.servePlaceholder(w)
}

// ServeThumbnail serves a book's downscaled cover thumbnail. A stored thumbnail is
// served immutable (its bytes are pinned by the caller's ?v= buster). With none
// yet, it falls back to the full cover — read from disk or lazily extracted —
// served no-cache so the smaller thumbnail wins once generated, and best-effort
// generates that thumbnail as a side effect (self-heal). A book with no cover and
// none extractable gets the placeholder.
func (s *Store) ServeThumbnail(w http.ResponseWriter, r *http.Request, bookID int64) {
	thumbPath := s.ThumbPath(bookID)
	if _, err := os.Stat(thumbPath); err == nil {
		serveImmutableFile(w, r, thumbPath)
		return
	}
	data, ok := s.coverBytesForThumb(r.Context(), bookID)
	if !ok {
		s.servePlaceholder(w)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

// shardDir returns the directory holding bookID's cached files (cover + thumbnail),
// bucketed by bookID/1000 to keep any single directory small.
func (s *Store) shardDir(bookID int64) string {
	return filepath.Join(s.dir, strconv.FormatInt(bookID/1000, 10))
}

// coverBytesForThumb returns the full cover bytes to serve when no thumbnail is
// cached yet, and self-heals the thumbnail for next time. A cover already on disk
// is read and (best-effort) thumbnailed; otherwise a one-time lazy extraction
// writes the cover and thumbnail via writeFile. The placeholder is reported as a
// miss so the caller serves it no-cache.
func (s *Store) coverBytesForThumb(ctx context.Context, bookID int64) ([]byte, bool) {
	if data, err := os.ReadFile(s.Path(bookID)); err == nil {
		if bytes.Equal(data, placeholderJPEG) {
			return nil, false
		}
		s.writeThumbnail(bookID, data)
		return data, true
	}
	data, ok := s.lazyExtract(ctx, bookID)
	if !ok || bytes.Equal(data, placeholderJPEG) {
		return nil, false
	}

	return data, true
}

// serveImmutableFile serves an on-disk JPEG with the year-long immutable policy.
// Used for both real covers and thumbnails, whose bytes are pinned by the caller's
// ?v= cache buster.
func serveImmutableFile(w http.ResponseWriter, r *http.Request, path string) {
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("Content-Type", "image/jpeg")
	http.ServeFile(w, r, path)
}

// serveCached serves an on-disk cover, detecting a cached placeholder (the
// lazy-extraction negative cache) by size + bytes so it gets the placeholder
// cache policy rather than a year-long immutable entry.
func (s *Store) serveCached(w http.ResponseWriter, r *http.Request, path string, size int64) {
	if size == int64(len(placeholderJPEG)) {
		if data, err := os.ReadFile(path); err == nil && bytes.Equal(data, placeholderJPEG) {
			s.servePlaceholder(w)
			return
		}
	}
	serveImmutableFile(w, r, path)
}

// serveBytes serves freshly-extracted bytes with the cache policy they merit.
func (s *Store) serveBytes(w http.ResponseWriter, data []byte) {
	if bytes.Equal(data, placeholderJPEG) {
		s.servePlaceholder(w)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

// writeFile caches already-JPEG bytes for bookID, creating the shard directory.
// The bytes are staged in a sibling temp file and atomically renamed into place,
// so a concurrent ServeCover read never observes a torn (half-written) JPEG.
func (s *Store) writeFile(bookID int64, jpegData []byte) error {
	if err := s.atomicWrite(s.Path(bookID), jpegData); err != nil {
		return err
	}
	s.writeThumbnail(bookID, jpegData)
	return nil
}

// writeThumbnail derives and caches a downscaled thumbnail beside the cover.
// Best-effort: any failure leaves cover serving intact (ServeThumbnail falls back
// to the full cover). The placeholder negative-cache write gets no thumbnail.
func (s *Store) writeThumbnail(bookID int64, jpegData []byte) {
	if bytes.Equal(jpegData, placeholderJPEG) {
		return
	}
	thumb, err := makeThumbnail(jpegData)
	if err != nil {
		return
	}
	_ = s.atomicWrite(s.ThumbPath(bookID), thumb)
}

// atomicWrite stages data in a sibling temp file and atomically renames it into
// place at path (creating the shard dir), so a concurrent reader never observes a
// torn file.
func (s *Store) atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create covers subdirectory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file %s: %w", path, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write temp file %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close temp file %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("rename temp file %s: %w", path, err)
	}

	return nil
}

// lazyExtract attempts a one-time extraction of a missing cover, transcoding it
// to JPEG and caching it. A book whose source parsed fine but carries no cover
// caches the placeholder (a negative cache), so a cover-dominant grid doesn't
// re-parse the source on every render. An extraction *error* — typically a
// browser-aborted request cancelling ctx mid-parse, or a transient I/O failure —
// serves the placeholder for this response only and caches nothing: caching it
// would permanently mask a perfectly good cover. The returned bytes are always
// JPEG, matching the served type.
func (s *Store) lazyExtract(ctx context.Context, bookID int64) ([]byte, bool) {
	if s.extractor == nil {
		return nil, false
	}

	data, ok, err := s.extractor.Cover(ctx, bookID)
	if err != nil {
		return placeholderJPEG, true // serve, but do not negative-cache an error
	}
	if !ok || len(data) == 0 {
		data = placeholderJPEG
	}

	jpegData, err := convertToJPEG(data)
	if err != nil {
		// Undecodable cover bytes from a successful parse: deterministic, cacheable.
		jpegData = placeholderJPEG
	}
	// Best-effort cache; serving still succeeds if the write fails.
	_ = s.writeFile(bookID, jpegData)

	return jpegData, true
}

func (s *Store) servePlaceholder(w http.ResponseWriter) {
	// no-cache = revalidate each time, so a cover that appears later under the
	// same URL is picked up; the body is tiny and revalidation keeps it cheap.
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.Itoa(len(placeholderJPEG)))
	_, _ = w.Write(placeholderJPEG)
}
