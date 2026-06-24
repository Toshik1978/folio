package covers

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
)

// decodeDims decodes JPEG bytes and returns their width and height.
func (s *coversTestSuite) decodeDims(data []byte) (int, int) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	s.Require().NoError(err)
	return cfg.Width, cfg.Height
}

func (s *coversTestSuite) TestMakeThumbnailDownscalesPreservingAspect() {
	// 600x900 (2:3). Longest side 900 > 400, so it downscales to 267x400.
	img := image.NewRGBA(image.Rect(0, 0, 600, 900))
	var buf bytes.Buffer
	s.Require().NoError(jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}))

	thumb, err := makeThumbnail(buf.Bytes())
	s.Require().NoError(err)
	w, h := s.decodeDims(thumb)
	s.Equal(400, h, "longest side capped at 400")
	s.Equal(267, w, "width scaled to preserve 2:3 aspect")
}

func (s *coversTestSuite) TestMakeThumbnailLeavesSmallCoverUnchanged() {
	src := s.jpegBytes() // 2x2, well within the cap
	thumb, err := makeThumbnail(src)
	s.Require().NoError(err)
	s.Equal(src, thumb, "covers within the cap are returned byte-for-byte")
}

func (s *coversTestSuite) TestMakeThumbnailRejectsUndecodable() {
	_, err := makeThumbnail([]byte("not an image"))
	s.Require().Error(err)
}

func (s *coversTestSuite) TestSaveWritesThumbnail() {
	st, err := NewStore(s.dataDir, nil)
	s.Require().NoError(err)
	s.Require().NoError(st.Save(1, s.bigJPEGBytes())) // 512x512 > 400

	data, err := os.ReadFile(st.ThumbPath(1))
	s.Require().NoError(err)
	w, h := s.decodeDims(data)
	s.Equal(400, w) // 512x512 square downscales to 400x400
	s.Equal(400, h)
}

func (s *coversTestSuite) TestDeleteRemovesThumbnail() {
	st, err := NewStore(s.dataDir, nil)
	s.Require().NoError(err)
	s.Require().NoError(st.Save(1, s.bigJPEGBytes()))
	s.Require().NoError(st.Delete(1))

	_, err = os.Stat(st.ThumbPath(1))
	s.Require().True(os.IsNotExist(err), "thumbnail removed with the cover")
}

func (s *coversTestSuite) TestSavePlaceholderWritesNoThumbnail() {
	st, err := NewStore(s.dataDir, nil)
	s.Require().NoError(err)
	s.Require().NoError(st.Save(1, placeholderJPEG))

	_, err = os.Stat(st.ThumbPath(1))
	s.Require().True(os.IsNotExist(err), "placeholder writes get no thumbnail")
}

func (s *coversTestSuite) TestServeThumbnailServesStoredThumb() {
	st, err := NewStore(s.dataDir, nil)
	s.Require().NoError(err)
	s.Require().NoError(st.Save(1, s.bigJPEGBytes()))

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/thumb", http.NoBody)
	st.ServeThumbnail(rec, req, 1)

	s.Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Header().Get("Cache-Control"), "immutable")
	w, h := s.decodeDims(rec.Body.Bytes())
	s.Equal(400, w)
	s.Equal(400, h)
}

func (s *coversTestSuite) TestServeThumbnailFallsBackToCoverAndSelfHeals() {
	st, err := NewStore(s.dataDir, nil)
	s.Require().NoError(err)
	s.Require().NoError(st.Save(1, s.bigJPEGBytes()))
	// Simulate a pre-existing cover whose thumbnail was never generated.
	s.Require().NoError(os.Remove(st.ThumbPath(1)))

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/thumb", http.NoBody)
	st.ServeThumbnail(rec, req, 1)

	s.Equal(http.StatusOK, rec.Code)
	s.Equal("no-cache", rec.Header().Get("Cache-Control"), "fallback must revalidate, never immutable")
	_, err = os.Stat(st.ThumbPath(1))
	s.Require().NoError(err, "thumbnail self-healed for the next request")
}

func (s *coversTestSuite) TestServeThumbnailLazyExtractsWhenNoCover() {
	ext := &fakeExtractor{data: s.bigJPEGBytes()}
	st, err := NewStore(s.dataDir, ext)
	s.Require().NoError(err)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/thumb", http.NoBody)
	st.ServeThumbnail(rec, req, 7)

	s.Equal(http.StatusOK, rec.Code)
	s.Equal("no-cache", rec.Header().Get("Cache-Control"))
	_, err = os.Stat(st.ThumbPath(7))
	s.Require().NoError(err, "extraction wrote both cover and thumbnail")
}

func (s *coversTestSuite) TestServeThumbnailPlaceholderWhenNothing() {
	st, err := NewStore(s.dataDir, nil) // no extractor
	s.Require().NoError(err)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/thumb", http.NoBody)
	st.ServeThumbnail(rec, req, 99)

	s.Equal(http.StatusOK, rec.Code)
	s.Equal("no-cache", rec.Header().Get("Cache-Control"))
	s.Equal(placeholderJPEG, rec.Body.Bytes())
}

func (s *coversTestSuite) TestMakeThumbnailRejectsOversizedDimensions() {
	// 30000x30000 = 900 MP, far above maxCoverPixels; decoding it would allocate
	// ~3.6 GB. The guard must reject it from the header, before image.Decode.
	_, err := makeThumbnail(bombHeaderPNG(30000, 30000))
	s.Require().Error(err)
	s.Contains(err.Error(), "pixel", "must be rejected by the dimension guard")
}
