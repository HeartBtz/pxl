// Package metrics définit l'instrumentation Prometheus de PXL.
//
// Métriques exposées :
//   - pxl_http_requests_total      : compteur de requêtes HTTP
//   - pxl_http_request_duration_seconds : histogramme de latence
//   - pxl_uploads_total / upload_bytes_total
//   - pxl_image_views_total / active_downloads
//   - pxl_thumbnail_generated_total / thumbnail_errors_total
//   - pxl_auth_failures_total      : par type (jwt, apikey, password)
package metrics

import (
	"database/sql"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	HTTPRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "pxl_http_requests_total",
		Help: "Total HTTP requests",
	}, []string{"method", "path", "status"})

	HTTPRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "pxl_http_request_duration_seconds",
		Help:    "HTTP request latency",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "path"})

	UploadsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "pxl_uploads_total",
		Help: "Total image uploads",
	})

	UploadBytesTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "pxl_upload_bytes_total",
		Help: "Total bytes uploaded",
	})

	UploadErrors = promauto.NewCounter(prometheus.CounterOpts{
		Name: "pxl_upload_errors_total",
		Help: "Total upload errors",
	})

	ImageViews = promauto.NewCounter(prometheus.CounterOpts{
		Name: "pxl_image_views_total",
		Help: "Total image views",
	})

	ImageServed = promauto.NewCounter(prometheus.CounterOpts{
		Name: "pxl_image_served_bytes_total",
		Help: "Total bytes served for images",
	})

	ThumbsGenerated = promauto.NewCounter(prometheus.CounterOpts{
		Name: "pxl_thumbs_generated_total",
		Help: "Total thumbnails generated",
	})

	ThumbErrors = promauto.NewCounter(prometheus.CounterOpts{
		Name: "pxl_thumb_errors_total",
		Help: "Total thumbnail generation errors",
	})

	AuthFailures = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "pxl_auth_failures_total",
		Help: "Total authentication failures",
	}, []string{"type"})
)

// InstrumentHandler is middleware that records Prometheus request metrics.
func InstrumentHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		wrapped := &statusWriter{ResponseWriter: w, code: 200}
		next.ServeHTTP(wrapped, r)

		d := time.Since(start).Seconds()
		path := "unmatched"
		if route := chi.RouteContext(r.Context()); route != nil && route.RoutePattern() != "" {
			path = route.RoutePattern()
		}
		method := r.Method
		switch method {
		case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
		default:
			method = "OTHER"
		}
		HTTPRequestsTotal.WithLabelValues(method, path, strconv.Itoa(wrapped.code)).Inc()
		HTTPRequestDuration.WithLabelValues(method, path).Observe(d)
	})
}

// Handler returns the Prometheus scrape handler.
func Handler() http.Handler { return promhttp.Handler() }

// RegisterDBMetrics exports database connection pool stats to Prometheus.
func RegisterDBMetrics(db *sql.DB) {
	promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "pxl_db_open_connections",
		Help: "Number of open DB connections",
	}, func() float64 { return float64(db.Stats().OpenConnections) })

	promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "pxl_db_in_use_connections",
		Help: "Number of DB connections currently in use",
	}, func() float64 { return float64(db.Stats().InUse) })

	promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "pxl_db_idle_connections",
		Help: "Number of idle DB connections",
	}, func() float64 { return float64(db.Stats().Idle) })

	promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "pxl_db_wait_count_total",
		Help: "Total number of connections waited for",
	}, func() float64 { return float64(db.Stats().WaitCount) })

	promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "pxl_db_wait_duration_seconds",
		Help: "Total time blocked waiting for a new connection",
	}, func() float64 { return db.Stats().WaitDuration.Seconds() })
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(code int) {
	w.code = code
	w.ResponseWriter.WriteHeader(code)
}

// Unwrap returns the underlying ResponseWriter, supporting http.Hijacker/Flusher.
func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *statusWriter) ReadFrom(src io.Reader) (int64, error) {
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(src)
	}
	return io.Copy(w.ResponseWriter, src)
}
