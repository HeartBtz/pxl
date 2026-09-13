package middleware

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/HeartBtz/pxl/internal/metrics"
	"github.com/HeartBtz/pxl/internal/service"
)

type contextKey string

const (
	UserIDKey   contextKey = "user_id"
	UsernameKey contextKey = "username"
	RoleKey     contextKey = "role"
	AuthTypeKey contextKey = "auth_type"
)

// Auth requires a valid JWT or API key.
func Auth(authSvc *service.AuthService) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// An outer OptionalAuth middleware may already have authenticated or
			// refreshed this request. Do not consume the freshly rotated refresh
			// token a second time in nested route groups.
			if _, ok := GetUserID(r.Context()); ok {
				next.ServeHTTP(w, r)
				return
			}
			ctx, ok, authType := authenticate(r, authSvc)
			if !ok && (authType == "cookie" || authType == "none") {
				ctx, ok = refreshBrowserSession(w, r, authSvc)
				if ok {
					authType = "cookie"
				}
			}
			if !ok {
				metrics.AuthFailures.WithLabelValues(authType).Inc()
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// OptionalAuth sets user context if credentials are present, but does not reject.
func OptionalAuth(authSvc *service.AuthService) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, ok, authType := authenticate(r, authSvc)
			if !ok && (authType == "cookie" || authType == "none") {
				ctx, ok = refreshBrowserSession(w, r, authSvc)
			}
			if ok {
				r = r.WithContext(ctx)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireAdmin requires role=admin in context.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role, _ := r.Context().Value(RoleKey).(string)
		if role != "admin" {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// SessionOnly blocks long-lived API keys from credential management and admin
// operations. JWT bearer tokens and protected browser cookies remain valid.
func SessionOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authType, _ := r.Context().Value(AuthTypeKey).(string)
		if authType == "apikey" {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			http.Error(w, `{"error":"session authentication required"}`, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// authenticate tries JWT (Authorization: Bearer) then API key (X-API-Key).
// Returns (context, success, authType) where authType is "bearer", "apikey", or "none".
func authenticate(r *http.Request, authSvc *service.AuthService) (context.Context, bool, string) {
	ctx := r.Context()

	// Try Bearer token
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		tokenStr := strings.TrimPrefix(auth, "Bearer ")
		_, user, err := authSvc.ValidateJWTUser(r.Context(), tokenStr)
		if err == nil {
			ctx = userContext(ctx, user.ID.String(), user.Username, user.Role, "bearer")
			return ctx, true, "bearer"
		}
		return ctx, false, "bearer"
	}

	// Try API key
	if key := r.Header.Get("X-API-Key"); key != "" {
		user, err := authSvc.ValidateAPIKey(r.Context(), key)
		if err == nil {
			ctx = userContext(ctx, user.ID.String(), user.Username, user.Role, "apikey")
			return ctx, true, "apikey"
		}
		return ctx, false, "apikey"
	}

	// Browser sessions use a host-only HttpOnly cookie. API clients continue to
	// use Authorization or X-API-Key and are unaffected by CSRF checks.
	if cookie, err := r.Cookie("pxl_session"); err == nil && cookie.Value != "" {
		_, user, err := authSvc.ValidateJWTUser(r.Context(), cookie.Value)
		if err == nil {
			ctx = userContext(ctx, user.ID.String(), user.Username, user.Role, "cookie")
			return ctx, true, "cookie"
		}
		return ctx, false, "cookie"
	}

	return ctx, false, "none"
}

func userContext(ctx context.Context, userID, username, role, authType string) context.Context {
	ctx = context.WithValue(ctx, UserIDKey, userID)
	ctx = context.WithValue(ctx, UsernameKey, username)
	ctx = context.WithValue(ctx, RoleKey, role)
	return context.WithValue(ctx, AuthTypeKey, authType)
}

func refreshBrowserSession(w http.ResponseWriter, r *http.Request, authSvc *service.AuthService) (context.Context, bool) {
	cookie, err := r.Cookie("pxl_refresh")
	if err != nil || cookie.Value == "" {
		return r.Context(), false
	}
	resp, err := authSvc.RefreshJWT(r.Context(), cookie.Value)
	if err != nil || resp.User == nil {
		return r.Context(), false
	}
	secure := authSvc.BrowserCookieSecure()
	http.SetCookie(w, &http.Cookie{ // #nosec G124 -- Secure follows the validated public URL; production is HTTPS
		Name: "pxl_session", Value: resp.Token, Path: "/", HttpOnly: true,
		Secure: secure, SameSite: http.SameSiteStrictMode, MaxAge: int(resp.ExpiresIn),
	})
	http.SetCookie(w, &http.Cookie{ // #nosec G124 -- Secure follows the validated public URL; production is HTTPS
		Name: "pxl_refresh", Value: resp.RefreshToken, Path: "/", HttpOnly: true,
		Secure: secure, SameSite: http.SameSiteStrictMode, MaxAge: int(authSvc.RefreshExpiry().Seconds()),
	})
	return userContext(r.Context(), resp.User.ID, resp.User.Username, resp.User.Role, "cookie"), true
}

// CookieOriginProtection rejects state-changing cookie-authenticated requests
// unless they originate from the configured application origin. This blocks
// classic and same-site sibling CSRF without exposing a token to JavaScript.
func CookieOriginProtection(baseURL string) func(http.Handler) http.Handler {
	expected, _ := url.Parse(baseURL)
	expectedOrigin := ""
	if expected != nil && expected.Scheme != "" && expected.Host != "" {
		expectedOrigin = expected.Scheme + "://" + expected.Host
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}
			_, sessionErr := r.Cookie("pxl_session")
			_, refreshErr := r.Cookie("pxl_refresh")
			browserEndpoint := strings.HasPrefix(r.URL.Path, "/api/v1/auth/browser-")
			hasBearer := strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !browserEndpoint && ((sessionErr != nil && refreshErr != nil) || hasBearer || r.Header.Get("X-API-Key") != "") {
				next.ServeHTTP(w, r)
				return
			}
			origin := r.Header.Get("Origin")
			if expectedOrigin == "" || origin != expectedOrigin {
				http.Error(w, `{"error":"invalid request origin"}`, http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// GetUserID extracts user ID string from context.
func GetUserID(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(UserIDKey).(string)
	return v, ok
}

// GetRole extracts role from context.
func GetRole(ctx context.Context) string {
	v, _ := ctx.Value(RoleKey).(string)
	return v
}
