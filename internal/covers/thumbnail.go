package covers

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"math"

	"golang.org/x/image/draw"
)

const (
	// thumbMaxDim caps a thumbnail's longest side. Sized for the web grid on HiDPI
	// screens and OPDS detail previews; covers already within it are served as-is.
	thumbMaxDim = 400
	// thumbQuality trades a little fidelity for much smaller thumbnail files; at
	// this size it is visually indistinguishable from the q95 used for full covers.
	thumbQuality = 85
)

// makeThumbnail decodes a JPEG cover and returns an aspect-preserving JPEG whose
// longest side is at most thumbMaxDim. A cover already within the bound is
// returned unchanged (no re-encode, never upscaled).
func makeThumbnail(jpegData []byte) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(jpegData))
	if err != nil {
		return nil, fmt.Errorf("decode cover for thumbnail: %w", err)
	}
	src := img.Bounds()
	tw, th := fitWithin(src.Dx(), src.Dy(), thumbMaxDim)
	if tw == src.Dx() && th == src.Dy() {
		return jpegData, nil
	}
	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, src, draw.Over, nil)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: thumbQuality}); err != nil {
		return nil, fmt.Errorf("encode thumbnail: %w", err)
	}

	return buf.Bytes(), nil
}

// fitWithin returns the largest size preserving the w:h aspect ratio that fits in
// a maxDim×maxDim box. Sizes already within the box are returned unchanged (no
// upscaling); results are clamped to a minimum of 1px for extreme aspect ratios.
func fitWithin(w, h, maxDim int) (int, int) {
	if w <= maxDim && h <= maxDim {
		return w, h
	}

	if w >= h {
		return maxDim, clampMin1(int(math.Round(float64(h) * float64(maxDim) / float64(w))))
	}

	return clampMin1(int(math.Round(float64(w) * float64(maxDim) / float64(h)))), maxDim
}

func clampMin1(n int) int {
	if n < 1 {
		return 1
	}
	return n
}
