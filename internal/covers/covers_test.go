package covers

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

// fakeExtractor returns a fixed cover (or none / an error) and counts
// invocations, so the one-time extraction, the cover-less caching, and the
// error-not-cached behavior can be asserted.
type fakeExtractor struct {
	data  []byte
	err   error
	calls int
}

func (f *fakeExtractor) Cover(context.Context, int64) ([]byte, bool, error) {
	f.calls++
	if f.err != nil {
		return nil, false, f.err
	}
	if len(f.data) == 0 {
		return nil, false, nil
	}

	return f.data, true, nil
}

func TestCovers(t *testing.T) {
	suite.Run(t, new(coversTestSuite))
}

type coversTestSuite struct {
	suite.Suite

	dataDir string
}

func (s *coversTestSuite) SetupTest() {
	s.dataDir = s.T().TempDir()
}

// sampleImage is a tiny opaque image used to generate real encoded fixtures.
func sampleImage() image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 200, G: 100, B: 50, A: 255})
	img.Set(1, 1, color.RGBA{R: 10, G: 20, B: 30, A: 255})
	return img
}

func (s *coversTestSuite) jpegBytes() []byte {
	var buf bytes.Buffer
	s.Require().NoError(jpeg.Encode(&buf, sampleImage(), nil))
	return buf.Bytes()
}

func (s *coversTestSuite) pngBytes() []byte {
	var buf bytes.Buffer
	s.Require().NoError(png.Encode(&buf, sampleImage()))
	return buf.Bytes()
}

func (s *coversTestSuite) gifBytes() []byte {
	var buf bytes.Buffer
	s.Require().NoError(gif.Encode(&buf, sampleImage(), nil))
	return buf.Bytes()
}

// isJPEG reports whether b starts with the JPEG SOI marker.
func isJPEG(b []byte) bool {
	return len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF
}

func (s *coversTestSuite) TestStoreCreatesCoverDirectory() {
	store, err := NewStore(s.dataDir, nil)
	s.Require().NoError(err)
	s.NotNil(store)
	s.DirExists(filepath.Join(s.dataDir, "covers"))
}

func (s *coversTestSuite) TestPathHasJPEGExtensionInShard() {
	store, err := NewStore(s.dataDir, nil)
	s.Require().NoError(err)
	// Sharded by id/1000; the id is the filename with a .jpeg extension.
	s.Equal("42.jpeg", filepath.Base(store.Path(42)))
	s.Equal("0", filepath.Base(filepath.Dir(store.Path(42))))
}

func (s *coversTestSuite) TestSaveTranscodesPNGToJPEG() {
	store, err := NewStore(s.dataDir, nil)
	s.Require().NoError(err)

	src := s.pngBytes()
	s.Require().NoError(store.Save(42, src))

	got, err := os.ReadFile(store.Path(42))
	s.Require().NoError(err)
	s.True(isJPEG(got), "a PNG cover must be stored as JPEG")
	s.NotEqual(src, got)
}

func (s *coversTestSuite) TestSaveKeepsExistingJPEGAsIs() {
	store, err := NewStore(s.dataDir, nil)
	s.Require().NoError(err)

	src := s.jpegBytes()
	s.Require().NoError(store.Save(1, src))

	got, err := os.ReadFile(store.Path(1))
	s.Require().NoError(err)
	s.Equal(src, got, "an already-JPEG cover must be stored byte-for-byte (no re-encode)")
}

func (s *coversTestSuite) TestSaveRejectsNonImage() {
	store, err := NewStore(s.dataDir, nil)
	s.Require().NoError(err)
	s.Error(store.Save(2, []byte("definitely not an image")))
}

func (s *coversTestSuite) TestSaveOverwrites() {
	store, err := NewStore(s.dataDir, nil)
	s.Require().NoError(err)

	s.Require().NoError(store.Save(1, s.pngBytes()))
	s.Require().NoError(store.Save(1, s.jpegBytes()))

	got, err := os.ReadFile(store.Path(1))
	s.Require().NoError(err)
	s.Equal(s.jpegBytes(), got)
}

func (s *coversTestSuite) TestServeExistingCover() {
	store, err := NewStore(s.dataDir, nil)
	s.Require().NoError(err)
	s.Require().NoError(store.Save(7, s.jpegBytes()))

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/covers/7", http.NoBody)
	w := httptest.NewRecorder()
	store.ServeHTTP(w, req, 7)

	s.Equal(http.StatusOK, w.Code)
	s.Equal("image/jpeg", w.Header().Get("Content-Type"))
	s.True(isJPEG(w.Body.Bytes()))
}

func (s *coversTestSuite) TestServeMissingCoverReturnsPlaceholder() {
	store, err := NewStore(s.dataDir, nil) // no extractor
	s.Require().NoError(err)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/covers/999", http.NoBody)
	w := httptest.NewRecorder()
	store.ServeHTTP(w, req, 999)

	s.Equal(http.StatusOK, w.Code)
	s.Equal("image/jpeg", w.Header().Get("Content-Type"))
	s.Equal(placeholderJPEG, w.Body.Bytes())
}

func (s *coversTestSuite) TestLazyExtractionTranscodesAndCaches() {
	ext := &fakeExtractor{data: s.pngBytes()} // extractor yields a PNG
	store, err := NewStore(s.dataDir, ext)
	s.Require().NoError(err)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/covers/5", http.NoBody)
	w := httptest.NewRecorder()
	store.ServeHTTP(w, req, 5)

	s.Equal(http.StatusOK, w.Code)
	s.Equal("image/jpeg", w.Header().Get("Content-Type"))
	s.True(isJPEG(w.Body.Bytes()), "a lazily-extracted PNG must be served as JPEG, matching the header")
	s.Equal("public, max-age=31536000, immutable", w.Header().Get("Cache-Control"),
		"a lazily-extracted real cover must be cached immutably")
	s.Equal(1, ext.calls)

	cached, err := os.ReadFile(store.Path(5))
	s.Require().NoError(err)
	s.True(isJPEG(cached))

	// Cached: a second request serves from disk without re-extracting.
	store.ServeHTTP(httptest.NewRecorder(), req, 5)
	s.Equal(1, ext.calls)
	s.True(store.Has(5))
}

func (s *coversTestSuite) TestLazyExtractionCachesPlaceholderForCoverlessBook() {
	ext := &fakeExtractor{} // no cover available
	store, err := NewStore(s.dataDir, ext)
	s.Require().NoError(err)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/covers/6", http.NoBody)
	w := httptest.NewRecorder()
	store.ServeHTTP(w, req, 6)
	s.Equal("image/jpeg", w.Header().Get("Content-Type"))
	s.Equal(placeholderJPEG, w.Body.Bytes())

	// The placeholder is cached, so a cover-less book is not re-parsed.
	store.ServeHTTP(httptest.NewRecorder(), req, 6)
	s.Equal(1, ext.calls)
	s.True(store.Has(6))
}

func (s *coversTestSuite) TestHasLocalCoverWithCachedReal() {
	store, err := NewStore(s.dataDir, nil)
	s.Require().NoError(err)
	s.Require().NoError(store.Save(1, s.jpegBytes()))

	s.True(store.HasLocalCover(context.Background(), 1), "a cached non-placeholder cover is a real local cover")
}

func (s *coversTestSuite) TestHasLocalCoverWithCachedPlaceholderIsFalse() {
	ext := &fakeExtractor{} // no cover available -> serving caches the placeholder
	store, err := NewStore(s.dataDir, ext)
	s.Require().NoError(err)
	store.ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/covers/2", http.NoBody), 2)
	s.Require().True(store.Has(2), "placeholder is cached")

	s.False(store.HasLocalCover(context.Background(), 2), "a cached placeholder is not a real local cover")
}

func (s *coversTestSuite) TestHasLocalCoverWithExtractableCover() {
	ext := &fakeExtractor{data: s.pngBytes()} // source yields a cover, not yet cached
	store, err := NewStore(s.dataDir, ext)
	s.Require().NoError(err)

	s.True(store.HasLocalCover(context.Background(), 3), "an extractable cover counts even before it is cached")
}

func (s *coversTestSuite) TestHasLocalCoverFalseWithoutExtractor() {
	store, err := NewStore(s.dataDir, nil)
	s.Require().NoError(err)

	s.False(store.HasLocalCover(context.Background(), 4), "no cache and no extractor means no local cover")
}

func (s *coversTestSuite) TestHasLocalCoverFalseWithCoverlessExtractor() {
	ext := &fakeExtractor{} // source has no cover
	store, err := NewStore(s.dataDir, ext)
	s.Require().NoError(err)

	s.False(store.HasLocalCover(context.Background(), 5))
}

func (s *coversTestSuite) TestVersionIsZeroWithoutCoverAndMtimeAfterSave() {
	store, err := NewStore(s.dataDir, nil)
	s.Require().NoError(err)

	s.Equal("0", store.Version(42))
	s.Require().NoError(store.Save(42, s.jpegBytes()))
	s.NotEqual("0", store.Version(42))
}

func (s *coversTestSuite) TestServePlaceholderIsNotImmutable() {
	store, err := NewStore(s.dataDir, nil)
	s.Require().NoError(err)

	w := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/covers/7", http.NoBody)
	store.ServeHTTP(w, r, 7)

	s.Equal("no-cache", w.Header().Get("Cache-Control"))
}

func (s *coversTestSuite) TestServeCachedPlaceholderIsNotImmutable() {
	// A coverless extractor makes lazyExtract cache the placeholder file; the
	// second request reads that cached file and must still avoid immutable.
	ext := &fakeExtractor{} // no cover available
	store, err := NewStore(s.dataDir, ext)
	s.Require().NoError(err)
	for range 2 {
		w := httptest.NewRecorder()
		r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/covers/8", http.NoBody)
		store.ServeHTTP(w, r, 8)
		s.Equal("no-cache", w.Header().Get("Cache-Control"))
	}
}

func (s *coversTestSuite) TestServeRealCoverIsImmutable() {
	store, err := NewStore(s.dataDir, nil)
	s.Require().NoError(err)
	s.Require().NoError(store.Save(9, s.jpegBytes()))

	w := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/covers/9", http.NoBody)
	store.ServeHTTP(w, r, 9)

	s.Equal("public, max-age=31536000, immutable", w.Header().Get("Cache-Control"))
}

func (s *coversTestSuite) TestPlaceholderIsValidJPEG() {
	s.NotEmpty(placeholderJPEG)
	s.True(isJPEG(placeholderJPEG))
}

func (s *coversTestSuite) TestStoreDelete() {
	store, err := NewStore(s.dataDir, nil)
	s.Require().NoError(err)

	// Deleting a non-existent cover should be a no-op
	s.Require().NoError(store.Delete(12345))

	// Save a cover, then delete it
	s.Require().NoError(store.Save(12345, s.jpegBytes()))
	s.True(store.Has(12345))

	s.Require().NoError(store.Delete(12345))
	s.False(store.Has(12345))
}

// H3: an extractor error (cancelled request, I/O blip) must serve the
// placeholder for this response only — never cache it as "no cover".
func (s *coversTestSuite) TestExtractorErrorIsNotNegativeCached() {
	ext := &fakeExtractor{err: context.Canceled}
	store, err := NewStore(s.dataDir, ext)
	s.Require().NoError(err)

	w := httptest.NewRecorder()
	store.ServeHTTP(w, httptest.NewRequestWithContext(s.T().Context(), http.MethodGet, "/", http.NoBody), 7)
	s.Equal(http.StatusOK, w.Code)
	s.False(store.Has(7), "error result must not be cached on disk")

	// The error clears; the real cover must now be extractable and cached.
	ext.err = nil
	ext.data = s.jpegBytes()
	w = httptest.NewRecorder()
	store.ServeHTTP(w, httptest.NewRequestWithContext(s.T().Context(), http.MethodGet, "/", http.NoBody), 7)
	s.True(store.Has(7))
	s.NotEqual(placeholderJPEG, w.Body.Bytes())
}

// A successful parse that finds no cover still negative-caches the placeholder.
func (s *coversTestSuite) TestNoCoverStillNegativeCaches() {
	ext := &fakeExtractor{}
	store, err := NewStore(s.dataDir, ext)
	s.Require().NoError(err)

	w := httptest.NewRecorder()
	store.ServeHTTP(w, httptest.NewRequestWithContext(s.T().Context(), http.MethodGet, "/", http.NoBody), 8)
	s.Equal(http.StatusOK, w.Code)
	s.True(store.Has(8), "deterministic no-cover must cache the placeholder")
}

// M1: re-saving identical bytes must not rewrite the file (mtime = ?v= buster).
func (s *coversTestSuite) TestSaveSkipsIdenticalBytes() {
	store, err := NewStore(s.dataDir, nil)
	s.Require().NoError(err)
	data := s.jpegBytes()
	s.Require().NoError(store.Save(9, data))

	past := time.Now().Add(-time.Hour)
	s.Require().NoError(os.Chtimes(store.Path(9), past, past))
	v := store.Version(9)

	s.Require().NoError(store.Save(9, data))
	s.Equal(v, store.Version(9), "identical save must not bump mtime")
}
