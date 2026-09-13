// Package middleware fournit les middlewares HTTP de PXL :
//   - CORS           : gestion des requêtes cross-origin (deny-by-default)
//   - SecurityHeaders: en-têtes de sécurité (CSP avec nonces, X-Frame-Options)
//   - RealIP         : extraction de l'IP réelle derrière un proxy
//   - RateLimiter    : limitation par IP avec nettoyage automatique
//   - Recovery       : récupération des panics avec stack trace
//   - Logger         : logging structuré des requêtes
//
// Le middleware d'auth est dans auth.go (séparé pour clarté).
package middleware

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// CORS configures Cross-Origin Resource Sharing headers.
// If allowedOrigin is empty, CORS headers are not set (deny cross-origin by default).
func CORS(allowedOrigin string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" {
				w.Header().Add("Vary", "Origin")
			}
			if allowedOrigin != "" && origin == allowedOrigin {
				w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key, X-Delete-Token")
				w.Header().Set("Access-Control-Max-Age", "86400")
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// CSPNonceKey is the context key for the CSP nonce value.
const CSPNonceKey contextKey = "csp_nonce"

// SecurityHeaders adds CSP (with per-request nonce), HSTS, and other security headers.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nonce := generateCSPNonce()
		ctx := context.WithValue(r.Context(), CSPNonceKey, nonce)

		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		// HTML, API data and refreshed cookies must not enter shared caches.
		// Image handlers explicitly opt public immutable bytes back into caching.
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		w.Header().Set("Content-Security-Policy",
			fmt.Sprintf("default-src 'self'; connect-src 'self'; img-src 'self' data: blob:; style-src 'self' 'unsafe-inline'; script-src 'self' 'nonce-%s'; font-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'self'; form-action 'self'", nonce))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// generateCSPNonce produces a cryptographically random base64 nonce.
func generateCSPNonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawStdEncoding.EncodeToString(b)
}

// GetCSPNonce extracts the CSP nonce from context.
func GetCSPNonce(ctx context.Context) string {
	v, _ := ctx.Value(CSPNonceKey).(string)
	return v
}

// extractIP safely extracts the IP from an address, handling cases with or without ports.
func extractIP(addr string) string {
	ip, _, err := net.SplitHostPort(addr)
	if err != nil {
		return strings.TrimSpace(addr)
	}
	return strings.TrimSpace(ip)
}

// RealIP extracts the real client IP from trusted proxies.
func RealIP(trustedProxies []string) func(http.Handler) http.Handler {
	trusted := make(map[string]bool, len(trustedProxies))
	trustedCIDRs := make([]*net.IPNet, 0, len(trustedProxies))
	for _, p := range trustedProxies {
		p = strings.TrimSpace(p)
		if _, network, err := net.ParseCIDR(p); err == nil {
			trustedCIDRs = append(trustedCIDRs, network)
		} else if ip := net.ParseIP(p); ip != nil {
			trusted[ip.String()] = true
		}
	}
	isTrusted := func(value string) bool {
		ip := net.ParseIP(strings.TrimSpace(value))
		if ip == nil {
			return false
		}
		if trusted[ip.String()] {
			return true
		}
		for _, network := range trustedCIDRs {
			if network.Contains(ip) {
				return true
			}
		}
		return false
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if len(trusted) > 0 || len(trustedCIDRs) > 0 {
				host := extractIP(r.RemoteAddr)
				if isTrusted(host) {
					if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
						parts := strings.Split(xff, ",")
						var clientIP string
						for i := len(parts) - 1; i >= 0; i-- {
							ip := net.ParseIP(strings.TrimSpace(parts[i]))
							if ip == nil {
								continue
							}
							clientIP = ip.String()
							if !isTrusted(clientIP) {
								break
							}
						}
						if clientIP != "" {
							r.RemoteAddr = net.JoinHostPort(clientIP, "0")
						}
					} else if xri := r.Header.Get("X-Real-IP"); xri != "" {
						if ip := net.ParseIP(strings.TrimSpace(xri)); ip != nil {
							r.RemoteAddr = net.JoinHostPort(ip.String(), "0")
						}
					}
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RateLimiter provides per-IP rate limiting using a token bucket.
type RateLimiter struct {
	shards   [64]rateLimitShard
	rate     int
	window   time.Duration
	stopCh   chan struct{}
	stopOnce sync.Once
}

type rateLimitShard struct {
	mu       sync.Mutex
	visitors map[string]*visitor
}

type visitor struct {
	tokens    int
	lastReset time.Time
}

func NewRateLimiter(rate int, window time.Duration) *RateLimiter {
	rl := &RateLimiter{
		rate:   rate,
		window: window,
		stopCh: make(chan struct{}),
	}
	for i := range rl.shards {
		rl.shards[i].visitors = make(map[string]*visitor)
	}
	go rl.cleanup()
	return rl
}

func (rl *RateLimiter) Stop() { rl.stopOnce.Do(func() { close(rl.stopCh) }) }

func (rl *RateLimiter) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := extractIP(r.RemoteAddr)
		shard := rl.shard(ip)

		shard.mu.Lock()
		v, ok := shard.visitors[ip]
		if !ok {
			v = &visitor{tokens: rl.rate, lastReset: time.Now()}
			shard.visitors[ip] = v
		}
		if time.Since(v.lastReset) > rl.window {
			v.tokens = rl.rate
			v.lastReset = time.Now()
		}
		if v.tokens <= 0 {
			remaining := rl.window - time.Since(v.lastReset)
			retryAfter := remaining / time.Second
			if remaining%time.Second > 0 {
				retryAfter++
			}
			if retryAfter < 1 {
				retryAfter = 1
			}
			shard.mu.Unlock()
			w.Header().Set("Retry-After", strconv.FormatInt(int64(retryAfter), 10))
			http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
			return
		}
		v.tokens--
		shard.mu.Unlock()

		next.ServeHTTP(w, r)
	})
}

func (rl *RateLimiter) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-rl.stopCh:
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-2 * rl.window)
			for i := range rl.shards {
				shard := &rl.shards[i]
				shard.mu.Lock()
				for ip, v := range shard.visitors {
					if v.lastReset.Before(cutoff) {
						delete(shard.visitors, ip)
					}
				}
				shard.mu.Unlock()
			}
		}
	}
}

func (rl *RateLimiter) shard(ip string) *rateLimitShard {
	h := fnv.New32a()
	_, _ = h.Write([]byte(ip))
	return &rl.shards[h.Sum32()%uint32(len(rl.shards))]
}

// Logger logs HTTP requests using slog.
func Logger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := &responseWriter{ResponseWriter: w, status: 200}
			next.ServeHTTP(ww, r)
			// Health probes run every few seconds and add no diagnostic value when
			// successful. Failures remain visible.
			if r.URL.Path == "/health" && ww.status < 400 {
				return
			}
			logger.Info("http",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", ww.status),
				slog.Duration("duration", time.Since(start)),
				slog.String("ip", r.RemoteAddr),
				slog.String("request_id", GetRequestID(r.Context())),
			)
		})
	}
}

// Recovery catches panics and returns 500.
func Recovery(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					logger.Error("panic recovered", slog.Any("panic", rec), slog.String("path", r.URL.Path))
					http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// MaxBody limits request body size.
func MaxBody(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			next.ServeHTTP(w, r)
		})
	}
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) ReadFrom(src io.Reader) (int64, error) {
	if rf, ok := rw.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(src)
	}
	return io.Copy(rw.ResponseWriter, src)
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

// Unwrap returns the underlying ResponseWriter, supporting http.Hijacker/Flusher.
func (rw *responseWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}

// RequestIDKey is the context key for the request ID.
const RequestIDKey contextKey = "request_id"

// RequestID generates (or propagates) a unique request ID for trace correlation.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if !validRequestID(id) {
			id = uuid.NewString()
		}
		ctx := context.WithValue(r.Context(), RequestIDKey, id)
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func validRequestID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

// GetRequestID extracts the request ID from context.
func GetRequestID(ctx context.Context) string {
	v, _ := ctx.Value(RequestIDKey).(string)
	return v
}
