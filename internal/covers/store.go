package covers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
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

// Cover-extraction state for a book, the single source of truth for whether a
// book has a cover. It replaces the on-disk placeholder negative cache.
const (
	StateUnknown int8 = 0 // never parsed for a cover
	StateHas     int8 = 1 // a real cover is cached on disk
	StateNone    int8 = 2 // parsed; source carries no cover (serve placeholder)
)

// CoverState reads and records a book's cover-extraction state. Implemented by
// *ingest.CoverState; covers stays a leaf package by depending only on this
// interface rather than importing db.
type CoverState interface {
	Get(ctx context.Context, bookID int64) (int8, error)
	Set(ctx context.Context, bookID int64, state int8) error
}

type Store struct {
	dir       string
	extractor CoverExtractor
	state     CoverState
}

// NewStore opens (creating if needed) the cover cache under dataDir. extractor
// enables lazy extraction on a cache miss; state records the outcome so a
// cover-less book is served the in-memory placeholder without re-parsing.
func NewStore(dataDir string, extractor CoverExtractor, state CoverState) (*Store, error) {
	dir := filepath.Join(dataDir, "covers")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create covers directory: %w", err)
	}
	return &Store{dir: dir, extractor: extractor, state: state}, nil
}

// Save transcodes data to JPEG (a no-op for already-JPEG bytes) and caches it,
// so every cover on disk is a JPEG and serving never has to sniff the type.
// Identical bytes are not rewritten: a rewrite bumps the file mtime, which is
// part of the ?v= cache-buster, so re-saving an unchanged cover would needlessly
// invalidate every client's cached copy.
func (s *Store) Save(bookID int64, data []byte) error {
	jpegData, decoded, err := convertToJPEGImage(data)
	if err != nil {
		return fmt.Errorf("save as jpeg: %w", err)
	}
	if existing, rerr := os.ReadFile(s.Path(bookID)); rerr == nil && bytes.Equal(existing, jpegData) {
		return nil
	}
	if err := s.writeFile(bookID, jpegData, decoded); err != nil {
		return err
	}
	// A real cover is now on disk; record it so HasLocalCover and the serve path
	// treat it as authoritative without re-reading or re-parsing.
	_ = s.state.Set(context.Background(), bookID, StateHas)

	return nil
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

// HasLocalCover reports whether bookID has a real cover from its own library
// files. StateHas is yes, StateNone is no; only an unknown book falls back to a
// budget-bounded extractor probe. It lets callers keep a good local cover
// instead of overwriting it with a lower-quality online thumbnail.
func (s *Store) HasLocalCover(ctx context.Context, bookID int64) bool {
	switch st, err := s.state.Get(ctx, bookID); {
	case err == nil && st == StateHas:
		return true
	case err == nil && st == StateNone:
		return false
	}
	if s.extractor == nil {
		return false
	}
	data, ok, err := s.extractor.Cover(ctx, bookID)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true // undetermined within budget; assume a cover may exist
	}

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
	// A real cover on disk is content-addressed by the caller's ?v= buster and
	// served immutable. Otherwise cover_state decides: a known cover-less book
	// gets the in-memory placeholder (no file ever written); an unknown book is
	// parsed once via lazyExtract, which records the result.
	if _, err := os.Stat(s.Path(bookID)); err == nil {
		serveImmutableFile(w, r, s.Path(bookID))
		return
	}
	if st, err := s.state.Get(r.Context(), bookID); err == nil && st == StateNone {
		s.servePlaceholder(w)
		return
	}
	if data, ok := s.lazyExtract(r.Context(), bookID); ok {
		s.serveBytes(w, data)
		return
	}
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
		s.writeThumbnail(bookID, data, nil)
		return data, true
	}
	if st, err := s.state.Get(ctx, bookID); err == nil && st == StateNone {
		return nil, false
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

// writeFile caches already-JPEG bytes for bookID and derives its thumbnail. decoded
// is the already-decoded image when the caller had to decode (non-JPEG source),
// letting writeThumbnail skip a second decode; pass nil when only bytes are known.
func (s *Store) writeFile(bookID int64, jpegData []byte, decoded image.Image) error {
	if err := s.atomicWrite(s.Path(bookID), jpegData); err != nil {
		return err
	}
	s.writeThumbnail(bookID, jpegData, decoded)

	return nil
}

// writeThumbnail derives and caches a downscaled thumbnail beside the cover.
// Best-effort. When decoded is non-nil it is reused (no second decode); otherwise
// the JPEG bytes are decoded. The placeholder negative-cache write gets no thumbnail.
func (s *Store) writeThumbnail(bookID int64, jpegData []byte, decoded image.Image) {
	if bytes.Equal(jpegData, placeholderJPEG) {
		return
	}
	var (
		thumb []byte
		err   error
	)
	if decoded != nil {
		thumb, err = resizeToThumb(decoded, jpegData)
	} else {
		thumb, err = makeThumbnail(jpegData)
	}
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

// lazyExtract performs a one-time extraction of a missing cover. A real cover is
// transcoded to JPEG, cached, and marked StateHas. A source that parsed cleanly
// but carries no cover is marked StateNone and written NOWHERE — the in-memory
// placeholder is returned. A parse error records no state and caches nothing, so
// a later view retries rather than permanently masking a good cover.
func (s *Store) lazyExtract(ctx context.Context, bookID int64) ([]byte, bool) {
	if s.extractor == nil {
		return nil, false
	}

	data, ok, err := s.extractor.Cover(ctx, bookID)
	if err != nil {
		return placeholderJPEG, true // serve once; do not record state
	}
	if !ok || len(data) == 0 {
		_ = s.state.Set(ctx, bookID, StateNone) // best-effort negative mark
		return placeholderJPEG, true
	}

	jpegData, err := convertToJPEG(data)
	if err != nil {
		// Undecodable bytes from a successful parse: deterministic, mark none.
		_ = s.state.Set(ctx, bookID, StateNone)
		return placeholderJPEG, true
	}
	if err := s.writeFile(bookID, jpegData, nil); err == nil {
		_ = s.state.Set(ctx, bookID, StateHas)
	}

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
