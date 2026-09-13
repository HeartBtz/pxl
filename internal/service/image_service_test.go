package service

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/png"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/HeartBtz/pxl/internal/config"
	"github.com/HeartBtz/pxl/internal/domain"
)

func TestValidDimensions(t *testing.T) {
	tests := []struct {
		name          string
		width, height int
		maxPixels     int64
		want          bool
	}{
		{name: "valid", width: 10_000, height: 10_000, maxPixels: 100_000_000, want: true},
		{name: "pixel bomb", width: 50_000, height: 50_000, maxPixels: 100_000_000, want: false},
		{name: "zero width", width: 0, height: 100, maxPixels: 100_000_000, want: false},
		{name: "zero limit", width: 100, height: 100, maxPixels: 0, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validDimensions(tt.width, tt.height, tt.maxPixels); got != tt.want {
				t.Fatalf("validDimensions() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAuthTagChangesWithPasswordHash(t *testing.T) {
	svc := &AuthService{cfg: &config.Config{Auth: config.AuthConfig{JWTSecret: strings.Repeat("s", 32)}}}
	first := svc.authTag("bcrypt-hash-one")
	if first == "" || first != svc.authTag("bcrypt-hash-one") {
		t.Fatal("auth tag is empty or unstable")
	}
	if first == svc.authTag("bcrypt-hash-two") {
		t.Fatal("password hash change did not change auth tag")
	}
}

func TestSanitizeFilename(t *testing.T) {
	if got := sanitizeFilename("../bad\x00\nname.png"); got != "badname.png" {
		t.Fatalf("sanitizeFilename() = %q", got)
	}
	long := strings.Repeat("é", 200)
	got := sanitizeFilename(long)
	if len(got) > 255 {
		t.Fatalf("sanitized filename is %d bytes", len(got))
	}
	if !utf8.ValidString(got) {
		t.Fatal("sanitized filename is not valid UTF-8")
	}
}

func TestHashDeleteTokenIsOneWayAndDomainSeparated(t *testing.T) {
	raw := "delete-secret"
	hashed := hashDeleteToken(raw)
	if hashed == raw || !strings.HasPrefix(hashed, "sha256:") {
		t.Fatalf("unexpected stored delete token %q", hashed)
	}
	if hashed != hashDeleteToken(raw) {
		t.Fatal("delete token hashing is not deterministic")
	}
	if hashed == hashDeleteToken(raw+"x") {
		t.Fatal("different tokens produced the same hash")
	}
}

func TestUploadRejectsActiveContentAndImageBombBeforeStorage(t *testing.T) {
	cfg := &config.Config{Upload: config.UploadConfig{MaxSizeBytes: 4096, MaxPixels: 100, TempDir: t.TempDir(), AllowedTypes: []string{"image/png"}}}
	svc := &ImageService{cfg: cfg}
	for _, payload := range []string{"<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>", "<!doctype html><html>fixture</html>"} {
		if _, err := svc.Upload(t.Context(), nil, "fake.png", []byte(payload)); !errors.Is(err, domain.ErrBlockedType) {
			t.Fatal("active content was not rejected before DB/storage")
		}
	}
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewNRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	bomb := data.Bytes()
	binary.BigEndian.PutUint32(bomb[16:20], 100000)
	binary.BigEndian.PutUint32(bomb[20:24], 100000)
	binary.BigEndian.PutUint32(bomb[29:33], crc32.ChecksumIEEE(bomb[12:29]))
	if _, err := svc.Upload(t.Context(), nil, "bomb.png", bomb); !errors.Is(err, domain.ErrInvalidImage) {
		t.Fatal("image bomb was not rejected before DB/storage")
	}
}
