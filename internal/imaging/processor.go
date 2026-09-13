// Package imaging gère le traitement d'images de PXL :
//   - Détection des dimensions (width/height) via image.DecodeConfig
//   - Génération de miniatures avec resize Lanczos (via disintegration/imaging)
//   - Support des formats JPEG, PNG, GIF, WebP
//
// Le Processor est utilisé par le ThumbnailWorker pour la génération asynchrone.
package imaging

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"

	"github.com/disintegration/imaging"
	_ "golang.org/x/image/webp"
)

// Processor gère la détection de dimensions et la génération de miniatures.
// Utilise la bibliothèque pure Go disintegration/imaging (pas de cgo).
type Processor struct {
	quality   int // JPEG quality 1-100
	maxPixels int64
}

func NewProcessor(quality int, pixelLimit ...int64) *Processor {
	if quality <= 0 || quality > 100 {
		quality = 85
	}
	maxPixels := int64(40_000_000)
	if len(pixelLimit) > 0 {
		maxPixels = pixelLimit[0]
	}
	return &Processor{quality: quality, maxPixels: maxPixels}
}

// GetDimensions returns image width and height.
// It only decodes the config header, not the full image.
func (p *Processor) GetDimensions(data []byte) (int, int, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0, fmt.Errorf("decode config: %w", err)
	}
	return cfg.Width, cfg.Height, nil
}

// GenerateThumbnail creates a thumbnail that fits within maxDim x maxDim
// while preserving aspect ratio. Returns the thumbnail data, output MIME type,
// and output extension.
func (p *Processor) GenerateThumbnail(r io.Reader, srcMIME string, maxDim int) ([]byte, string, string, error) {
	// Revalidate stored/legacy bytes before allocation, not just upload metadata.
	// Bound header buffering even for files containing oversized metadata blocks.
	var header bytes.Buffer
	cfg, _, err := image.DecodeConfig(io.TeeReader(io.LimitReader(r, 1<<20), &header))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || p.maxPixels <= 0 || int64(cfg.Width) > p.maxPixels/int64(cfg.Height) {
		return nil, "", "", fmt.Errorf("invalid or oversized image dimensions")
	}
	img, err := imaging.Decode(io.MultiReader(bytes.NewReader(header.Bytes()), r), imaging.AutoOrientation(true))
	if err != nil {
		return nil, "", "", fmt.Errorf("decode: %w", err)
	}

	thumb := imaging.Fit(img, maxDim, maxDim, imaging.Lanczos)

	var buf bytes.Buffer
	var format imaging.Format
	var outMIME, outExt string

	switch srcMIME {
	case "image/png":
		format = imaging.PNG
		outMIME = "image/png"
		outExt = ".png"
	case "image/gif":
		// GIF thumbnails rendered as PNG (single frame)
		format = imaging.PNG
		outMIME = "image/png"
		outExt = ".png"
	default:
		format = imaging.JPEG
		outMIME = "image/jpeg"
		outExt = ".jpg"
	}

	var opts []imaging.EncodeOption
	if format == imaging.JPEG {
		opts = append(opts, imaging.JPEGQuality(p.quality))
	}

	if err := imaging.Encode(&buf, thumb, format, opts...); err != nil {
		return nil, "", "", fmt.Errorf("encode: %w", err)
	}

	return buf.Bytes(), outMIME, outExt, nil
}
