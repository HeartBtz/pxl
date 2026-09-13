package metrics

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
)

func TestRequestMetricCardinalityIsBounded(t *testing.T) {
	HTTPRequestsTotal.Reset()
	HTTPRequestDuration.Reset()
	r := chi.NewRouter()
	r.Use(InstrumentHandler)
	r.Get("/i/{shortID}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	for i := range 100 {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", fmt.Sprintf("/i/arbitrary-long-value-%d", i), nil))
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(fmt.Sprintf("UNKNOWN%d", i), fmt.Sprintf("/random/%d", i), nil))
	}
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() == "pxl_http_requests_total" && len(f.Metric) != 2 {
			t.Fatalf("request series=%d want=2", len(f.Metric))
		}
	}
}
