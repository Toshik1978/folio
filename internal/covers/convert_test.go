package covers

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
)

// convertToJPEG tests live on coversTestSuite (one runner per the package's
// single-concern suite layout).

// bombHeaderPNG builds a minimal valid PNG consisting of just the signature and
// an IHDR chunk declaring the given dimensions. image.DecodeConfig reads the
// header (and so the dimensions) without decoding any pixels, which is exactly
// how a decompression bomb is detected before allocation.
func bombHeaderPNG(width, height uint32) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A})

	ihdr := make([]byte, 0, 17)
	ihdr = append(ihdr, 'I', 'H', 'D', 'R')
	var dims [8]byte
	binary.BigEndian.PutUint32(dims[0:4], width)
	binary.BigEndian.PutUint32(dims[4:8], height)
	ihdr = append(ihdr, dims[:]...)
	ihdr = append(ihdr, 8, 2, 0, 0, 0) // 8-bit depth, truecolor (non-paletted)

	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(ihdr)-4))
	buf.Write(length[:])
	buf.Write(ihdr)

	var crc [4]byte
	binary.BigEndian.PutUint32(crc[:], crc32.ChecksumIEEE(ihdr))
	buf.Write(crc[:])

	return buf.Bytes()
}

func (s *coversTestSuite) TestConvertToJPEGRejectsOversizedDimensions() {
	// 30000x30000 = 900 MP, far above maxCoverPixels; decoding it would allocate
	// ~3.6 GB and can OOM the low-spec target hosts.
	_, err := convertToJPEG(bombHeaderPNG(30000, 30000))
	s.Require().Error(err)
	s.Contains(err.Error(), "pixel", "must be rejected by the dimension guard, before image.Decode")
}

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
