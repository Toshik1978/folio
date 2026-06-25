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

// convertToJPEG decodes an image and returns it as JPEG bytes.
func convertToJPEG(inputData []byte) ([]byte, error) {
	out, _, err := convertToJPEGImage(inputData)

	return out, err
}

// convertToJPEGImage returns the JPEG bytes and, when a full decode happened
// (non-JPEG input), the decoded image so the caller can derive a thumbnail without
// decoding the JPEG a second time. For the already-JPEG fast path decoded is nil
// (no decode occurred).
func convertToJPEGImage(inputData []byte) (jpegBytes []byte, decoded image.Image, err error) {
	if len(inputData) == 0 {
		return nil, nil, errors.New("input data is empty")
	}

	cfg, formatName, cfgErr := image.DecodeConfig(bytes.NewReader(inputData))
	if cfgErr == nil && int64(cfg.Width)*int64(cfg.Height) > maxCoverPixels {
		return nil, nil, fmt.Errorf("image dimensions %dx%d exceed %d pixel limit",
			cfg.Width, cfg.Height, maxCoverPixels)
	}
	if cfgErr == nil && formatName == "jpeg" {
		return inputData, nil, nil
	}

	img, formatName, decErr := image.Decode(bytes.NewReader(inputData))
	if decErr != nil {
		return nil, nil, fmt.Errorf("failed to decode image data: %w", decErr)
	}

	var outputBuffer bytes.Buffer
	if encErr := jpeg.Encode(&outputBuffer, img, &jpeg.Options{Quality: 95}); encErr != nil {
		return nil, nil, fmt.Errorf("failed to encode image to jpeg (source format was %s): %w", formatName, encErr)
	}

	return outputBuffer.Bytes(), img, nil
}
