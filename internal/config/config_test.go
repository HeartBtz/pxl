package config

import (
	"strings"
	"testing"
	"time"
)

func TestPublicOriginAndProxyValidation(t *testing.T) {
	valid := Config{
		Server:   ServerConfig{BaseURL: "https://pxl.example", ReadHeaderTimeout: time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 4096},
		Auth:     AuthConfig{JWTSecret: strings.Repeat("x", 32), JWTExpiry: time.Minute, RefreshExpiry: time.Hour},
		Database: DatabaseConfig{Password: "fixture-only", MaxOpenConns: 1},
		Storage:  StorageConfig{Backend: "local"},
		Upload:   UploadConfig{MaxSizeBytes: 1024, MaxFiles: 1, MaxPixels: 100, TempDir: "unused"},
		Security: SecurityConfig{RateLimitRequests: 1, PublicRateLimitRequests: 1, RateLimitWindow: time.Minute},
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, origin := range []string{"https://user@pxl.example", "https://*.example", "https://pxl.example/subpath", "https://pxl.example?x=y", "https://pxl.example#fragment", "javascript:fixture"} {
		cfg := valid
		cfg.Server.BaseURL = origin
		if err := cfg.Validate(); err == nil {
			t.Fatal("unsafe public origin accepted")
		}
	}
	for _, proxy := range []string{"*", "0.0.0.0/0", "::/0", "invalid"} {
		cfg := valid
		cfg.Security.TrustedProxies = []string{proxy}
		if err := cfg.Validate(); err == nil {
			t.Fatal("unrestricted or invalid proxy accepted")
		}
	}
	for _, origin := range []string{"*", "https://*.example", "https://pxl.example/path", "https://user@pxl.example"} {
		cfg := valid
		cfg.Security.CORSAllowedOrigin = origin
		if err := cfg.Validate(); err == nil {
			t.Fatal("unsafe CORS origin accepted")
		}
	}
	valid.Server.BaseURL += "/"
	if err := valid.Validate(); err != nil || strings.HasSuffix(valid.Server.BaseURL, "/") {
		t.Fatal("trailing slash normalization failed")
	}
}
