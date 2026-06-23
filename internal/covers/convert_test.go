package covers

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
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

// jpegWithDeclaredDimensions encodes a minimal real JPEG (4x4 pixels) and then
// patches the SOF0 (Start of Frame) segment to declare the given dimensions.
// image.DecodeConfig reads only the header markers and therefore reports the
// patched dimensions without decoding any pixels — exactly the
// decompression-bomb attack surface that the pixel cap must catch.
func jpegWithDeclaredDimensions(t *testing.T, width, height uint16) []byte {
	t.Helper()

	// Encode a tiny real JPEG; this gives us all the required header segments
	// (DQT, SOF0, DHT, …) that Go's JPEG decoder demands before it will parse
	// SOF0 dimensions from image.DecodeConfig.
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(0, 0, color.RGBA{R: 200, G: 100, B: 50, A: 255})
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("jpegWithDeclaredDimensions: encode: %v", err)
	}
	data := buf.Bytes()

	// Walk the JPEG segments to locate SOF0 (marker 0xC0) and patch its
	// height/width fields in-place. Layout inside the segment (after the 2-byte
	// marker):  length(2) | precision(1) | height(2) | width(2) | …
	out := make([]byte, len(data))
	copy(out, data)
	for i := 2; i+3 < len(out); { // skip SOI (2 bytes)
		if out[i] != 0xFF {
			t.Fatalf("jpegWithDeclaredDimensions: expected 0xFF at offset %d", i)
		}
		marker := out[i+1]
		if marker == 0xD9 || marker == 0xDA { // EOI / SOS — no length word
			break
		}
		segLen := int(binary.BigEndian.Uint16(out[i+2 : i+4]))
		if marker == 0xC0 { // SOF0 — patch height at i+5, width at i+7
			binary.BigEndian.PutUint16(out[i+5:i+7], height)
			binary.BigEndian.PutUint16(out[i+7:i+9], width)
			break
		}
		i += 2 + segLen
	}

	return out
}

func (s *coversTestSuite) TestConvertRejectsOversizeJPEG() {
	// 60000x60000 = 3.6 GP, far above maxCoverPixels (40 MP).
	data := jpegWithDeclaredDimensions(s.T(), 60000, 60000)
	_, err := convertToJPEG(data)
	s.Require().Error(err)
	s.Contains(err.Error(), "pixel", "must be rejected by the dimension guard, not an incidental decode error")
}
