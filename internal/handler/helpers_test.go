package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeJSONRejectsTrailingAndOversizedBodies(t *testing.T) {
	for _, body := range []string{`{"name":"ok"} {"name":"extra"}`, `{"name":"ok"} garbage`, `{"name":"ok"}` + strings.Repeat(" ", maxJSONBodySize)} {
		var dst struct {
			Name string `json:"name"`
		}
		if err := decodeJSON(httptest.NewRequest("POST", "/", strings.NewReader(body)), &dst); err == nil {
			t.Fatal("invalid JSON body accepted")
		}
	}
	var dst struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(httptest.NewRequest("POST", "/", strings.NewReader("{\"name\":\"ok\"}\n")), &dst); err != nil {
		t.Fatal(err)
	}
}

func TestUploadDoesNotDowngradeInvalidCredentials(t *testing.T) {
	for _, kind := range []string{"Authorization", "X-API-Key", "pxl_session", "pxl_refresh"} {
		req := httptest.NewRequest("POST", "/api/v1/upload", nil)
		if strings.HasPrefix(kind, "pxl_") {
			req.AddCookie(&http.Cookie{Name: kind, Value: "fixture"})
		} else {
			req.Header.Set(kind, "Bearer fixture")
		}
		rr := httptest.NewRecorder()
		(&ImageHandler{}).Upload(rr, req)
		if rr.Code != 401 {
			t.Fatalf("unvalidated %s downgraded to anonymous upload", kind)
		}
	}
}
