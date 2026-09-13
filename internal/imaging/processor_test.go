package imaging

import (
	"bytes"
	"image"
	"image/png"
	"testing"
)

func TestThumbnailRechecksPixelLimit(t *testing.T) {
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewNRGBA(image.Rect(0, 0, 20, 20))); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := NewProcessor(85, 100).GenerateThumbnail(bytes.NewReader(data.Bytes()), "image/png", 10); err == nil {
		t.Fatal("oversized stored image decoded")
	}
	if _, _, _, err := NewProcessor(85, 400).GenerateThumbnail(bytes.NewReader(data.Bytes()), "image/png", 10); err != nil {
		t.Fatal(err)
	}
}
