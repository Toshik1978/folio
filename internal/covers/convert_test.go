package covers

// convertToJPEG tests live on coversTestSuite (one runner per the package's
// single-concern suite layout).

func (s *coversTestSuite) TestConvertToJPEGPassesThroughJPEG() {
	src := s.jpegBytes()
	out, err := convertToJPEG(src)
	s.Require().NoError(err)
	s.Equal(src, out, "already-JPEG input must be returned unchanged (no re-encode)")
}

func (s *coversTestSuite) TestConvertToJPEGConvertsPNG() {
	out, err := convertToJPEG(s.pngBytes())
	s.Require().NoError(err)
	s.True(isJPEG(out))
}

func (s *coversTestSuite) TestConvertToJPEGConvertsGIF() {
	out, err := convertToJPEG(s.gifBytes())
	s.Require().NoError(err)
	s.True(isJPEG(out))
}

func (s *coversTestSuite) TestConvertToJPEGRejectsEmpty() {
	_, err := convertToJPEG(nil)
	s.Error(err)
}

func (s *coversTestSuite) TestConvertToJPEGRejectsNonImage() {
	_, err := convertToJPEG([]byte("not an image at all"))
	s.Error(err)
}
