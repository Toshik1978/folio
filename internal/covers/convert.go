package covers

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // Support GIF format
	"image/jpeg"
	_ "image/png" // Support PNG format

	_ "golang.org/x/image/bmp"  // Support BMP format
	_ "golang.org/x/image/tiff" // Support TIFF format
	_ "golang.org/x/image/webp" // Support WEBP format
)

// maxCoverPixels caps the decoded area (width × height) of a non-JPEG cover. A
// small, highly-compressed image can declare enormous dimensions (a
// decompression bomb); image.Decode would then allocate width×height×4 bytes and
// can OOM the low-spec target hosts (NAS, Raspberry Pi, minimal VPS). 40 MP is far
// above any real book cover yet bounds the worst-case allocation.
const maxCoverPixels = 40_000_000

// convertToJPEG decodes an image from a byte slice in memory and returns new slice as a JPEG.
func convertToJPEG(inputData []byte) ([]byte, error) {
	if len(inputData) == 0 {
		return nil, errors.New("input data is empty")
	}

	// Check the image format and dimensions using only the header metadata.
	cfg, formatName, err := image.DecodeConfig(bytes.NewReader(inputData))
	// Reject decompression bombs from the header alone, before either the
	// JPEG fast-path return or image.Decode allocates the full pixel buffer.
	// A header we cannot read falls through to image.Decode below, which
	// returns the real decode error.
	if err == nil && int64(cfg.Width)*int64(cfg.Height) > maxCoverPixels {
		return nil, fmt.Errorf("image dimensions %dx%d exceed %d pixel limit",
			cfg.Width, cfg.Height, maxCoverPixels)
	}
	// If it is already a JPEG and within the pixel cap, bypass
	// decoding/encoding completely (no allocation).
	if err == nil && formatName == "jpeg" {
		return inputData, nil
	}

	// Otherwise do decode
	img, formatName, err := image.Decode(bytes.NewReader(inputData))
	if err != nil {
		return nil, fmt.Errorf("failed to decode image data: %w", err)
	}

	var outputBuffer bytes.Buffer
	err = jpeg.Encode(&outputBuffer, img, &jpeg.Options{Quality: 95})
	if err != nil {
		return nil, fmt.Errorf("failed to encode image to jpeg (source format was %s): %w", formatName, err)
	}

	return outputBuffer.Bytes(), nil
}
