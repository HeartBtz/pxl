//go:build integration

package handler

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/HeartBtz/pxl/internal/domain"
	"github.com/HeartBtz/pxl/internal/middleware"
)

func TestUploadAutoAlbum(t *testing.T) {
	for _, tt := range []struct {
		name      string
		query     string
		wantAlbum bool
	}{
		{name: "default", wantAlbum: true},
		{name: "opt out", query: "?auto_album=false"},
		{name: "explicit true", query: "?auto_album=true", wantAlbum: true},
		{name: "empty", query: "?auto_album=", wantAlbum: true},
		{name: "invalid", query: "?auto_album=invalid", wantAlbum: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newAuditFixture(t)
			u := f.user(t, "owner")
			login := f.login(t, u)
			ih := NewImageHandler(f.imageSvc, f.albumSvc, f.cfg, f.log)
			t.Cleanup(ih.Stop)
			h := middleware.OptionalAuth(f.auth)(http.HandlerFunc(ih.Upload))
			var body bytes.Buffer
			mp := multipart.NewWriter(&body)
			for i := range 2 {
				part, err := mp.CreateFormFile("file", "fixture.png")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := part.Write(fixturePNG(t, uint8(i))); err != nil {
					t.Fatal(err)
				}
			}
			if err := mp.Close(); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/upload"+tt.query, &body)
			req.Header.Set("Content-Type", mp.FormDataContentType())
			req.Header.Set("Authorization", "Bearer "+login.Token)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code != http.StatusCreated {
				t.Fatalf("upload status=%d body=%s", rr.Code, rr.Body.String())
			}
			var result struct {
				Images []domain.UploadResponse `json:"images"`
				Album  map[string]string       `json:"album"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if len(result.Images) != 2 || (result.Album != nil) != tt.wantAlbum {
				t.Fatalf("unexpected upload response: %s", rr.Body.String())
			}
			albums, err := f.albumSvc.ListByUser(t.Context(), u.ID)
			if err != nil {
				t.Fatal(err)
			}
			wantCount := 0
			if tt.wantAlbum {
				wantCount = 1
			}
			if len(albums) != wantCount {
				t.Fatalf("persisted albums=%d, want %d", len(albums), wantCount)
			}
			for _, upload := range result.Images {
				img, err := f.images.GetByShortID(t.Context(), upload.ShortID)
				if err != nil {
					t.Fatal(err)
				}
				if img.UserID == nil || *img.UserID != u.ID || upload.DeleteToken == "" {
					t.Fatal("upload lost ownership or deletion capability")
				}
			}
		})
	}
}
