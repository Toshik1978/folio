package covers

import (
	"bytes"
	"image"
	"image/jpeg"
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
