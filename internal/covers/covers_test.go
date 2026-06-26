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
	"sync"
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

// fakeState is an in-memory covers.CoverState for store tests.
type fakeState struct {
	mu     sync.Mutex
	states map[int64]int8
	getErr error
}

func newFakeState() *fakeState { return &fakeState{states: map[int64]int8{}} }

func (f *fakeState) Get(_ context.Context, id int64) (int8, error) {
	if f.getErr != nil {
		return 0, f.getErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.states[id], nil
}

func (f *fakeState) Set(_ context.Context, id int64, s int8) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.states[id] = s

	return nil
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

// bigJPEGBytes encodes a larger image so a torn write would span many disk
// blocks, widening the window in which a partial read could be observed.
func (s *coversTestSuite) bigJPEGBytes() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 512, 512))
	for y := range 512 {
		for x := range 512 {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: uint8(x ^ y), A: 255})
		}
	}

	var buf bytes.Buffer
	s.Require().NoError(jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}))

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
	store, err := NewStore(s.dataDir, nil, newFakeState())
	s.Require().NoError(err)
	s.NotNil(store)
	s.DirExists(filepath.Join(s.dataDir, "covers"))
}

func (s *coversTestSuite) TestPathHasJPEGExtensionInShard() {
	store, err := NewStore(s.dataDir, nil, newFakeState())
	s.Require().NoError(err)
	// Sharded by id/1000; the id is the filename with a .jpeg extension.
	s.Equal("42.jpeg", filepath.Base(store.Path(42)))
	s.Equal("0", filepath.Base(filepath.Dir(store.Path(42))))
}

func (s *coversTestSuite) TestSaveTranscodesPNGToJPEG() {
	store, err := NewStore(s.dataDir, nil, newFakeState())
	s.Require().NoError(err)

	src := s.pngBytes()
	s.Require().NoError(store.Save(42, src))

	got, err := os.ReadFile(store.Path(42))
	s.Require().NoError(err)
	s.True(isJPEG(got), "a PNG cover must be stored as JPEG")
	s.NotEqual(src, got)
}

func (s *coversTestSuite) TestSaveKeepsExistingJPEGAsIs() {
	store, err := NewStore(s.dataDir, nil, newFakeState())
	s.Require().NoError(err)

	src := s.jpegBytes()
	s.Require().NoError(store.Save(1, src))

	got, err := os.ReadFile(store.Path(1))
	s.Require().NoError(err)
	s.Equal(src, got, "an already-JPEG cover must be stored byte-for-byte (no re-encode)")
}

func (s *coversTestSuite) TestSaveRejectsNonImage() {
	store, err := NewStore(s.dataDir, nil, newFakeState())
	s.Require().NoError(err)
	s.Error(store.Save(2, []byte("definitely not an image")))
}

func (s *coversTestSuite) TestSaveOverwrites() {
	store, err := NewStore(s.dataDir, nil, newFakeState())
	s.Require().NoError(err)

	s.Require().NoError(store.Save(1, s.pngBytes()))
	s.Require().NoError(store.Save(1, s.jpegBytes()))

	got, err := os.ReadFile(store.Path(1))
	s.Require().NoError(err)
	s.Equal(s.jpegBytes(), got)
}

func (s *coversTestSuite) TestServeExistingCover() {
	store, err := NewStore(s.dataDir, nil, newFakeState())
	s.Require().NoError(err)
	s.Require().NoError(store.Save(7, s.jpegBytes()))

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/covers/7", http.NoBody)
	w := httptest.NewRecorder()
	store.ServeCover(w, req, 7)

	s.Equal(http.StatusOK, w.Code)
	s.Equal("image/jpeg", w.Header().Get("Content-Type"))
	s.True(isJPEG(w.Body.Bytes()))
}

func (s *coversTestSuite) TestServeMissingCoverReturnsPlaceholder() {
	store, err := NewStore(s.dataDir, nil, newFakeState()) // no extractor
	s.Require().NoError(err)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/covers/999", http.NoBody)
	w := httptest.NewRecorder()
	store.ServeCover(w, req, 999)

	s.Equal(http.StatusOK, w.Code)
	s.Equal("image/jpeg", w.Header().Get("Content-Type"))
	s.Equal(placeholderJPEG, w.Body.Bytes())
}

func (s *coversTestSuite) TestLazyExtractionTranscodesAndCaches() {
	ext := &fakeExtractor{data: s.pngBytes()} // extractor yields a PNG
	store, err := NewStore(s.dataDir, ext, newFakeState())
	s.Require().NoError(err)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/covers/5", http.NoBody)
	w := httptest.NewRecorder()
	store.ServeCover(w, req, 5)

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
	store.ServeCover(httptest.NewRecorder(), req, 5)
	s.Equal(1, ext.calls)
	s.True(store.Has(5))
}

func (s *coversTestSuite) TestServeNoCoverMarksNoneAndWritesNoFile() {
	st := newFakeState()
	store, err := NewStore(s.dataDir, &fakeExtractor{}, st) // extractor returns no cover
	s.Require().NoError(err)

	rr := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/cover", http.NoBody)
	store.ServeCover(rr, req, 42)

	s.Equal(http.StatusOK, rr.Code)
	s.Equal("no-cache", rr.Header().Get("Cache-Control")) // placeholder policy
	s.Equal(placeholderJPEG, rr.Body.Bytes())             // served from memory
	state, _ := st.Get(context.Background(), 42)
	s.Equal(StateNone, state) // marked, not re-parsed
	_, statErr := os.Stat(store.Path(42))
	s.True(os.IsNotExist(statErr)) // NO file on disk
}

func (s *coversTestSuite) TestServeKnownNoneServesPlaceholderWithoutParsing() {
	st := newFakeState()
	s.Require().NoError(st.Set(context.Background(), 7, StateNone))
	ext := &fakeExtractor{}
	store, err := NewStore(s.dataDir, ext, st)
	s.Require().NoError(err)

	rr := httptest.NewRecorder()
	store.ServeCover(rr, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/cover", http.NoBody), 7)

	s.Equal(placeholderJPEG, rr.Body.Bytes())
	s.Equal(0, ext.calls) // known-none must not parse the source
}

func (s *coversTestSuite) TestServeThumbnailKnownNoneServesPlaceholderWithoutParsing() {
	st := newFakeState()
	s.Require().NoError(st.Set(context.Background(), 7, StateNone))
	ext := &fakeExtractor{}
	store, err := NewStore(s.dataDir, ext, st)
	s.Require().NoError(err)

	rr := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/thumbnail", http.NoBody)
	store.ServeThumbnail(rr, req, 7)

	s.Equal(placeholderJPEG, rr.Body.Bytes())
	s.Equal(0, ext.calls) // known-none must not parse the source
}

func (s *coversTestSuite) TestServeRealCoverMarksHasAndCachesFile() {
	st := newFakeState()
	store, err := NewStore(s.dataDir, &fakeExtractor{data: s.jpegBytes()}, st)
	s.Require().NoError(err)

	rr := httptest.NewRecorder()
	store.ServeCover(rr, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/cover", http.NoBody), 9)

	s.Equal("public, max-age=31536000, immutable", rr.Header().Get("Cache-Control"))
	state, _ := st.Get(context.Background(), 9)
	s.Equal(StateHas, state)
	s.True(store.Has(9)) // real cover file on disk
}

func (s *coversTestSuite) TestServeExtractErrorLeavesStateUnknownAndUncached() {
	st := newFakeState()
	ext := &fakeExtractor{err: context.Canceled}
	store, err := NewStore(s.dataDir, ext, st)
	s.Require().NoError(err)

	rr := httptest.NewRecorder()
	store.ServeCover(rr, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/cover", http.NoBody), 5)

	s.Equal(placeholderJPEG, rr.Body.Bytes()) // served, this response only
	state, _ := st.Get(context.Background(), 5)
	s.Equal(StateUnknown, state) // not negative-cached
	_, statErr := os.Stat(store.Path(5))
	s.True(os.IsNotExist(statErr))
}

func (s *coversTestSuite) TestHasLocalCoverByState() {
	st := newFakeState()
	s.Require().NoError(st.Set(context.Background(), 1, StateHas))
	s.Require().NoError(st.Set(context.Background(), 2, StateNone))
	ext := &fakeExtractor{}
	store, err := NewStore(s.dataDir, ext, st)
	s.Require().NoError(err)

	s.True(store.HasLocalCover(context.Background(), 1))
	s.False(store.HasLocalCover(context.Background(), 2))
	s.Equal(0, ext.calls) // state answered both without parsing
}

func (s *coversTestSuite) TestHasLocalCoverWithExtractableCover() {
	ext := &fakeExtractor{data: s.pngBytes()} // source yields a cover, not yet cached
	store, err := NewStore(s.dataDir, ext, newFakeState())
	s.Require().NoError(err)

	s.True(store.HasLocalCover(context.Background(), 3), "an extractable cover counts even before it is cached")
}

func (s *coversTestSuite) TestHasLocalCoverFalseWithoutExtractor() {
	store, err := NewStore(s.dataDir, nil, newFakeState())
	s.Require().NoError(err)

	s.False(store.HasLocalCover(context.Background(), 4), "no cache and no extractor means no local cover")
}

func (s *coversTestSuite) TestHasLocalCoverFalseWithCoverlessExtractor() {
	ext := &fakeExtractor{} // source has no cover
	store, err := NewStore(s.dataDir, ext, newFakeState())
	s.Require().NoError(err)

	s.False(store.HasLocalCover(context.Background(), 5))
}

func (s *coversTestSuite) TestVersionIsZeroWithoutCoverAndMtimeAfterSave() {
	store, err := NewStore(s.dataDir, nil, newFakeState())
	s.Require().NoError(err)

	s.Equal("0", store.Version(42))
	s.Require().NoError(store.Save(42, s.jpegBytes()))
	s.NotEqual("0", store.Version(42))
}

func (s *coversTestSuite) TestServePlaceholderIsNotImmutable() {
	store, err := NewStore(s.dataDir, nil, newFakeState())
	s.Require().NoError(err)

	w := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/covers/7", http.NoBody)
	store.ServeCover(w, r, 7)

	s.Equal("no-cache", w.Header().Get("Cache-Control"))
}

func (s *coversTestSuite) TestServeCachedPlaceholderIsNotImmutable() {
	// A coverless extractor causes lazyExtract to persist a StateNone marker;
	// the second request serves the placeholder via that marker — no file is
	// ever cached and no re-parse occurs — but must still avoid immutable.
	ext := &fakeExtractor{} // no cover available
	store, err := NewStore(s.dataDir, ext, newFakeState())
	s.Require().NoError(err)
	for range 2 {
		w := httptest.NewRecorder()
		r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/covers/8", http.NoBody)
		store.ServeCover(w, r, 8)
		s.Equal("no-cache", w.Header().Get("Cache-Control"))
	}
}

func (s *coversTestSuite) TestServeRealCoverIsImmutable() {
	store, err := NewStore(s.dataDir, nil, newFakeState())
	s.Require().NoError(err)
	s.Require().NoError(store.Save(9, s.jpegBytes()))

	w := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/covers/9", http.NoBody)
	store.ServeCover(w, r, 9)

	s.Equal("public, max-age=31536000, immutable", w.Header().Get("Cache-Control"))
}

func (s *coversTestSuite) TestPlaceholderIsValidJPEG() {
	s.NotEmpty(placeholderJPEG)
	s.True(isJPEG(placeholderJPEG))
}

func (s *coversTestSuite) TestStoreDelete() {
	store, err := NewStore(s.dataDir, nil, newFakeState())
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
	store, err := NewStore(s.dataDir, ext, newFakeState())
	s.Require().NoError(err)

	w := httptest.NewRecorder()
	store.ServeCover(w, httptest.NewRequestWithContext(s.T().Context(), http.MethodGet, "/", http.NoBody), 7)
	s.Equal(http.StatusOK, w.Code)
	s.False(store.Has(7), "error result must not be cached on disk")

	// The error clears; the real cover must now be extractable and cached.
	ext.err = nil
	ext.data = s.jpegBytes()
	w = httptest.NewRecorder()
	store.ServeCover(w, httptest.NewRequestWithContext(s.T().Context(), http.MethodGet, "/", http.NoBody), 7)
	s.True(store.Has(7))
	s.NotEqual(placeholderJPEG, w.Body.Bytes())
}

// Save must replace a cover atomically: a concurrent reader either sees the
// old or the new complete JPEG, never a torn (half-written) file, and no temp
// staging file is left behind.
func (s *coversTestSuite) TestSaveIsAtomicUnderConcurrentReads() {
	store, err := NewStore(s.dataDir, nil, newFakeState())
	s.Require().NoError(err)

	small := s.jpegBytes()
	large := s.bigJPEGBytes() // distinct, much larger payload to widen the torn-read window
	s.Require().NoError(store.Save(3, small))

	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			data, rerr := os.ReadFile(store.Path(3))
			if rerr != nil {
				continue // mid-rename the path can momentarily not exist on some OSes
			}
			// Every observed file must be one of the two complete writes, never a
			// truncated/partial blend — that is what os.WriteFile risked.
			if !bytes.Equal(data, small) && !bytes.Equal(data, large) {
				s.Failf("torn read", "read %d bytes matching neither complete cover", len(data))
				return
			}
		}
	})

	for i := range 50 {
		next := large
		if i%2 == 0 {
			next = small
		}
		s.Require().NoError(store.Save(3, next))
	}
	close(stop)
	wg.Wait()

	// No leftover staging files in the shard directory.
	entries, err := os.ReadDir(filepath.Dir(store.Path(3)))
	s.Require().NoError(err)
	for _, e := range entries {
		s.NotContains(e.Name(), ".tmp", "temp staging file must not leak")
	}
}

// M1: re-saving identical bytes must not rewrite the file (mtime = ?v= buster).
func (s *coversTestSuite) TestHasLocalCoverConservativeOnContextError() {
	ext := &fakeExtractor{err: context.DeadlineExceeded}
	st, err := NewStore(s.dataDir, ext, newFakeState())
	s.Require().NoError(err)

	// No cover on disk; extraction times out → we must NOT report "no local cover"
	// (which would let an online thumbnail overwrite a real, just-uncomputed cover).
	s.True(st.HasLocalCover(context.Background(), 7))
}

func (s *coversTestSuite) TestSaveSkipsIdenticalBytes() {
	store, err := NewStore(s.dataDir, nil, newFakeState())
	s.Require().NoError(err)
	data := s.jpegBytes()
	s.Require().NoError(store.Save(9, data))

	past := time.Now().Add(-time.Hour)
	s.Require().NoError(os.Chtimes(store.Path(9), past, past))
	v := store.Version(9)

	s.Require().NoError(store.Save(9, data))
	s.Equal(v, store.Version(9), "identical save must not bump mtime")
}
