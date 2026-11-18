package server

import (
	"context"
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// requestIDKey is the context key type used to carry the request id through the
// request context so the slog handler can read it.
type requestIDKey struct{}

const RequestIDHeader = "X-Request-Id"

// NewContextHandler wraps a slog.Handler so records emitted with a context that
// carries a request id (see RequestID) get the attribute added automatically.
func NewContextHandler(base slog.Handler) slog.Handler {
	return contextHandler{next: base}
}

type contextHandler struct {
	next slog.Handler
}

func (h contextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if v := ctx.Value(requestIDKey{}); v != nil {
		if id, ok := v.(string); ok {
			r.AddAttrs(slog.String("request_id", id))
		}
	}
	return h.next.Handle(ctx, r)
}

func (h contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return contextHandler{next: h.next.WithAttrs(attrs)}
}

func (h contextHandler) WithGroup(name string) slog.Handler {
	return contextHandler{next: h.next.WithGroup(name)}
}

// RequestID returns a gin middleware that assigns an X-Request-Id to every
// request (or reuses the incoming one), puts it in the request context and echoes
// it back on the response. Logs emitted through a context-aware slog handler
// carry it as `request_id`.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(RequestIDHeader)
		if id == "" {
			id = uuid.New().String()
		}
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), requestIDKey{}, id))
		c.Header(RequestIDHeader, id)
		c.Next()
	}
}

// AccessLog returns a gin middleware that emits one structured log line per
// request after the chain completes, with method, path, status and duration.
func AccessLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		slog.InfoContext(
			c.Request.Context(),
			"http_request",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"duration_ms", float64(time.Since(start).Microseconds())/1000.0,
		)
	}
}
