package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

func newTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestID())
	r.Use(AccessLog())
	r.GET("/api/v1/health", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	r.GET("/api/v1/ready", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.GET("/api/v1/ok", func(c *gin.Context) { c.Status(http.StatusOK) })
	return r
}

func TestRequestIDGeneratesAndEchoes(t *testing.T) {
	r := newTestRouter()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if got := w.Header().Get(RequestIDHeader); got == "" {
		t.Fatalf("expected generated request id header, got empty")
	}
}

func TestRequestIDReusesIncomingValue(t *testing.T) {
	r := newTestRouter()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	req.Header.Set(RequestIDHeader, "client-supplied-id")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if got := w.Header().Get(RequestIDHeader); got != "client-supplied-id" {
		t.Fatalf("expected client-supplied-id, got %q", got)
	}
}

func TestAccessLogEmitsStructuredLineWithRequestID(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(NewContextHandler(slog.NewJSONHandler(&buf, nil))))
	defer slog.SetDefault(prev)

	r := newTestRouter()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ok", nil)
	req.Header.Set(RequestIDHeader, "req-abc-123")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("access log is not JSON: %v (%q)", err, buf.String())
	}
	if got := rec["msg"]; got != "http_request" {
		t.Fatalf("log msg = %v, want http_request", got)
	}
	if got := rec["request_id"]; got != "req-abc-123" {
		t.Fatalf("log request_id = %v, want req-abc-123", got)
	}
	if got := rec["status"]; got != float64(200) {
		t.Fatalf("log status = %v, want 200", got)
	}
	if !bytes.Contains(buf.Bytes(), []byte("duration_ms")) {
		t.Fatalf("access log missing duration_ms: %q", buf.String())
	}
}

func TestHealthReturnsNoContentWithoutDatabase(t *testing.T) {
	s := &Server{}
	r := gin.New()
	r.GET("/api/v1/health", s.handleHealth)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("health status = %d, want 204", w.Code)
	}
}

func TestReadyReturns503WhenDatabaseUnreachable(t *testing.T) {
	cfg, err := pgxpool.ParseConfig("postgres://gd:gd@127.0.0.1:1/gamedepot?sslmode=disable")
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	defer pool.Close()

	s := &Server{pool: pool}
	r := gin.New()
	r.GET("/api/v1/ready", s.handleReady)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ready", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready status = %d, want 503 when database unreachable", w.Code)
	}
}
