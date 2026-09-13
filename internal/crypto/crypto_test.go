package crypto

import (
	"testing"
)

func TestGenerateShortID(t *testing.T) {
	ids := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := GenerateShortID(8)
		if len(id) != 8 {
			t.Errorf("ShortID length = %d, want 8", len(id))
		}
		if ids[id] {
			t.Errorf("duplicate ShortID: %s", id)
		}
		ids[id] = true
	}
}

func TestGenerateDeleteToken(t *testing.T) {
	tokens := make(map[string]bool)
	for i := 0; i < 50; i++ {
		tok := GenerateDeleteToken()
		if len(tok) == 0 {
			t.Error("empty delete token")
		}
		if tokens[tok] {
			t.Errorf("duplicate delete token")
		}
		tokens[tok] = true
	}
}

func TestHashAPIKey(t *testing.T) {
	hash1 := HashAPIKey("test_key")
	hash2 := HashAPIKey("test_key")
	if hash1 != hash2 {
		t.Error("HashAPIKey should be deterministic")
	}
	if len(hash1) == 0 {
		t.Error("hash should not be empty")
	}
	hash3 := HashAPIKey("different_key")
	if hash1 == hash3 {
		t.Error("different keys should produce different hashes")
	}
}

func TestExtensionFromMIME(t *testing.T) {
	tests := []struct {
		mime string
		want string
	}{
		{"image/jpeg", ".jpg"},
		{"image/png", ".png"},
		{"image/gif", ".gif"},
		{"image/webp", ".webp"},
		{"image/bmp", ".bmp"},
		{"application/pdf", ".bin"},
		{"", ".bin"},
	}
	for _, tt := range tests {
		got := ExtensionFromMIME(tt.mime)
		if got != tt.want {
			t.Errorf("ExtensionFromMIME(%q) = %q, want %q", tt.mime, got, tt.want)
		}
	}
}
