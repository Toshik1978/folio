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

// convertToJPEG decodes an image from a byte slice in memory and returns new slice as a JPEG.
func convertToJPEG(inputData []byte) ([]byte, error) {
	if len(inputData) == 0 {
		return nil, errors.New("input data is empty")
	}

	// Check the image format using only the header metadata
	// If it is already a JPEG, bypass decoding/encoding completely
	configReader := bytes.NewReader(inputData)
	_, formatName, err := image.DecodeConfig(configReader)
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
