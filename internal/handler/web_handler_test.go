package handler

import (
	"html/template"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/HeartBtz/pxl/internal/config"
)

func TestUploadPageExposesUploadLimits(t *testing.T) {
	h := &WebHandler{
		cfg: &config.Config{Upload: config.UploadConfig{
			MaxSizeBytes: 123456,
			MaxFiles:     7,
			AllowedTypes: []string{"image/png", "image/jpeg"},
		}},
		log: slog.Default(),
		tmpl: template.Must(template.New("upload.html").Option("missingkey=error").Parse(
			`{{.MaxSize}}|{{.MaxFiles}}|{{range .AllowedTypes}}{{.}};{{end}}`)),
	}
	rr := httptest.NewRecorder()
	h.UploadPage(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got, want := rr.Body.String(), "123456|7|image/png;image/jpeg;"; got != want {
		t.Fatalf("upload config = %q, want %q", got, want)
	}
}

func TestUploadPageRendersBulkUploadControls(t *testing.T) {
	cfg := &config.Config{Upload: config.UploadConfig{
		MaxSizeBytes: 123456, MaxFiles: 7, AllowedTypes: []string{"image/png", "image/jpeg"},
	}}
	h := NewWebHandler(nil, nil, nil, cfg, slog.Default())
	rr := httptest.NewRecorder()
	h.UploadPage(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rr.Body.String()
	for _, marker := range []string{"/static/pxl-upload.js", "webkitdirectory multiple", "maxSize:  123456", "maxFiles:  7", `allowedTypes: ["image/png","image/jpeg"]`, "</html>"} {
		if !strings.Contains(body, marker) {
			t.Errorf("rendered upload page missing %q", marker)
		}
	}
	if strings.Contains(body, "ZgotmplZ") {
		t.Fatal("template rejected a value in its JavaScript context")
	}
}
