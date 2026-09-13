package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestRateLimiterRetryAfter(t *testing.T) {
	for _, tt := range []struct {
		name    string
		elapsed time.Duration
		want    string
	}{
		{name: "full window", want: "60"},
		{name: "round up", elapsed: 250 * time.Millisecond, want: "60"},
		{name: "remaining window", elapsed: 58500 * time.Millisecond, want: "2"},
		{name: "last fraction", elapsed: 59750 * time.Millisecond, want: "1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				rl := NewRateLimiter(2, time.Minute)
				defer rl.Stop()
				h := rl.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusNoContent)
				}))
				req := httptest.NewRequest(http.MethodPost, "/api/v1/upload", nil)
				for range 2 {
					rr := httptest.NewRecorder()
					h.ServeHTTP(rr, req)
					if rr.Code != http.StatusNoContent || rr.Header().Get("Retry-After") != "" {
						t.Fatalf("allowed request: status=%d headers=%v", rr.Code, rr.Header())
					}
				}
				time.Sleep(tt.elapsed)
				for range 2 {
					rr := httptest.NewRecorder()
					h.ServeHTTP(rr, req)
					if rr.Code != http.StatusTooManyRequests {
						t.Fatalf("exhausted limit: status=%d, want 429", rr.Code)
					}
					if got := rr.Header().Get("Retry-After"); got != tt.want {
						t.Fatalf("Retry-After = %q, want %q", got, tt.want)
					}
				}
				// Rejections must not extend the window or change the refill limit.
				time.Sleep(time.Minute - tt.elapsed + time.Nanosecond)
				for i := range 3 {
					rr := httptest.NewRecorder()
					h.ServeHTTP(rr, req)
					wantStatus, wantRetry := http.StatusNoContent, ""
					if i == 2 {
						wantStatus, wantRetry = http.StatusTooManyRequests, "60"
					}
					if rr.Code != wantStatus || rr.Header().Get("Retry-After") != wantRetry {
						t.Fatalf("refilled request %d: status=%d headers=%v", i, rr.Code, rr.Header())
					}
				}
			})
		})
	}
}

func TestSecurityHeaders_CSPNonce(t *testing.T) {
	handler := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nonce := GetCSPNonce(r.Context())
		if nonce == "" {
			t.Error("CSP nonce should be set in context")
		}
		w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	csp := rr.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("CSP header should be set")
	}
	if strings.Contains(csp, "'unsafe-inline'") && strings.Contains(csp, "script-src") {
		// Check script-src specifically
		for _, part := range strings.Split(csp, ";") {
			part = strings.TrimSpace(part)
			if strings.HasPrefix(part, "script-src") && strings.Contains(part, "'unsafe-inline'") {
				t.Error("script-src should not contain 'unsafe-inline'")
			}
		}
	}
	if !strings.Contains(csp, "nonce-") {
		t.Error("CSP should contain nonce")
	}
	for _, directive := range []string{"object-src 'none'", "base-uri 'self'", "frame-ancestors 'self'", "form-action 'self'"} {
		if !strings.Contains(csp, directive) {
			t.Errorf("CSP missing %q", directive)
		}
	}
}

func TestSecurityHeaders_UniqueNonce(t *testing.T) {
	var nonces []string
	handler := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nonces = append(nonces, GetCSPNonce(r.Context()))
	}))

	for i := 0; i < 10; i++ {
		req := httptest.NewRequest("GET", "/", nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
	}

	seen := make(map[string]bool)
	for _, n := range nonces {
		if seen[n] {
			t.Errorf("duplicate nonce: %s", n)
		}
		seen[n] = true
	}
}

func TestCORS_EmptyOrigin_NoCORSHeaders(t *testing.T) {
	handler := CORS("")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Origin", "http://evil.com")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("CORS should not be set when origin is not configured")
	}
}

func TestCORS_ConfiguredOrigin(t *testing.T) {
	handler := CORS("http://example.com")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Origin", "http://example.com")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Header().Get("Access-Control-Allow-Origin") != "http://example.com" {
		t.Errorf("CORS origin should be http://example.com, got %q", rr.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestCORS_DoesNotReflectUntrustedOrigin(t *testing.T) {
	handler := CORS("https://pxl.example")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	req.Header.Set("Origin", "https://evil.example")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("untrusted origin received CORS permission %q", got)
	}
}

func TestRealIP_ValidatesForwardedChain(t *testing.T) {
	handler := RealIP([]string{"10.0.0.0/8"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.RemoteAddr))
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.2:443"
	req.Header.Set("X-Forwarded-For", "garbage, 203.0.113.8, 10.0.0.3")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if got := rr.Body.String(); got != "203.0.113.8:0" {
		t.Fatalf("RemoteAddr = %q", got)
	}
}

func TestCookieOriginProtection(t *testing.T) {
	handler := CookieOriginProtection("https://pxl.example")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, test := range []struct {
		name, origin string
		want         int
	}{
		{name: "same origin", origin: "https://pxl.example", want: http.StatusNoContent},
		{name: "cross origin", origin: "https://evil.example", want: http.StatusForbidden},
		{name: "missing origin", want: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
			req.AddCookie(&http.Cookie{Name: "pxl_session", Value: "secret"})
			if test.origin != "" {
				req.Header.Set("Origin", test.origin)
			}
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			if rr.Code != test.want {
				t.Fatalf("status = %d, want %d", rr.Code, test.want)
			}
		})
	}
}

func TestBrowserEndpointsAlwaysRequireTrustedOrigin(t *testing.T) {
	h := CookieOriginProtection("https://pxl.example")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	for _, path := range []string{"/api/v1/auth/browser-login", "/api/v1/auth/browser-register", "/api/v1/auth/browser-refresh"} {
		for _, origin := range []string{"", "null", "https://sibling.example", "https://pxl.example"} {
			for _, header := range []string{"", "Authorization", "X-API-Key"} {
				req := httptest.NewRequest("POST", path, nil)
				req.Header.Set("Origin", origin)
				if header != "" {
					req.Header.Set(header, "Bearer fixture")
				}
				rr := httptest.NewRecorder()
				h.ServeHTTP(rr, req)
				want := 403
				if origin == "https://pxl.example" {
					want = 204
				}
				if rr.Code != want {
					t.Fatalf("browser origin guard status=%d want=%d", rr.Code, want)
				}
			}
		}
	}
	// Header-authenticated API clients retain their non-browser contract.
	req := httptest.NewRequest("POST", "/api/v1/auth/password", nil)
	req.Header.Set("Authorization", "Bearer fixture")
	req.AddCookie(&http.Cookie{Name: "pxl_session", Value: "fixture"})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 204 {
		t.Fatal("API bearer contract broken")
	}
}

func TestAuthAcceptsContextFromOuterOptionalAuth(t *testing.T) {
	called := false
	handler := Auth(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	req := httptest.NewRequest(http.MethodGet, "/account", nil)
	req = req.WithContext(userContext(req.Context(), "id", "user", "user", "cookie"))
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if !called {
		t.Fatal("nested Auth did not reuse authenticated request context")
	}
}

func TestSessionOnlyRejectsAPIKey(t *testing.T) {
	handler := SessionOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, test := range []struct {
		authType string
		want     int
	}{
		{authType: "apikey", want: http.StatusForbidden},
		{authType: "bearer", want: http.StatusNoContent},
		{authType: "cookie", want: http.StatusNoContent},
	} {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req = req.WithContext(userContext(req.Context(), "id", "user", "user", test.authType))
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != test.want {
			t.Errorf("%s status = %d, want %d", test.authType, rr.Code, test.want)
		}
	}
}

func TestResponseWriter_Unwrap(t *testing.T) {
	rr := httptest.NewRecorder()
	rw := &responseWriter{ResponseWriter: rr, status: 200}

	unwrapped := rw.Unwrap()
	if unwrapped != rr {
		t.Error("Unwrap should return the underlying ResponseWriter")
	}
}

func TestRequestIDRejectsUnsafeValues(t *testing.T) {
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	for _, unsafe := range []string{"contains spaces", "line\nbreak", strings.Repeat("a", 129)} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Request-ID", unsafe)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if got := rr.Header().Get("X-Request-ID"); got == unsafe || got == "" {
			t.Fatalf("unsafe request ID %q was not replaced: %q", unsafe, got)
		}
	}
}

func TestRequestIDKeepsSafeValue(t *testing.T) {
	const id = "edge-01.request_42"
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-ID", id)
	rr := httptest.NewRecorder()
	RequestID(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {})).ServeHTTP(rr, req)
	if got := rr.Header().Get("X-Request-ID"); got != id {
		t.Fatalf("request ID = %q, want %q", got, id)
	}
}
