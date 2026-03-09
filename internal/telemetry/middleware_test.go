package telemetry

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPMiddleware_SetsStatusCode(t *testing.T) {
	InstallNoopProviders()
	m := buildMetrics(NoopMeterProvider())

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte("ok"))
	})

	mw := HTTPMiddleware(m)(handler)
	req := httptest.NewRequest("GET", "/health", nil)
	rec := httptest.NewRecorder()

	mw.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rec.Code)
	}
}

func TestHTTPMiddleware_Default200(t *testing.T) {
	InstallNoopProviders()
	m := buildMetrics(NoopMeterProvider())

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	mw := HTTPMiddleware(m)(handler)
	req := httptest.NewRequest("GET", "/test", nil)
	rec := httptest.NewRecorder()

	mw.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestHTTPMiddleware_NilMetrics(t *testing.T) {
	InstallNoopProviders()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mw := HTTPMiddleware(nil)(handler)
	req := httptest.NewRequest("POST", "/webhook/whatsapp", nil)
	rec := httptest.NewRecorder()

	mw.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestHTTPMiddleware_500Error(t *testing.T) {
	InstallNoopProviders()
	m := buildMetrics(NoopMeterProvider())

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	mw := HTTPMiddleware(m)(handler)
	req := httptest.NewRequest("GET", "/health", nil)
	rec := httptest.NewRecorder()

	mw.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}

func TestTracer_ReturnsNonNil(t *testing.T) {
	InstallNoopProviders()
	tracer := Tracer()
	if tracer == nil {
		t.Fatal("expected non-nil tracer")
	}
}
