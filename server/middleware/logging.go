/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package middleware

import (
	"log/slog"
	"net/http"
	"time"
)

// statusWriter wraps http.ResponseWriter to capture the status code.
type statusWriter struct {
	http.ResponseWriter
	status  int
	written int64
}

func (sw *statusWriter) WriteHeader(code int) {
	sw.status = code
	sw.ResponseWriter.WriteHeader(code)
}

func (sw *statusWriter) Write(b []byte) (int, error) {
	n, err := sw.ResponseWriter.Write(b)
	sw.written += int64(n)
	return n, err
}

// Flush implements http.Flusher so SSE streaming works through the logging middleware.
func (sw *statusWriter) Flush() {
	if f, ok := sw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap returns the underlying ResponseWriter (for middleware introspection).
func (sw *statusWriter) Unwrap() http.ResponseWriter {
	return sw.ResponseWriter
}

// Logging returns middleware that logs each request using slog.
func Logging(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)

			duration := time.Since(start)

			attrs := []any{
				"method", r.Method,
				"path", r.URL.Path,
				"status", sw.status,
				"duration_ms", duration.Milliseconds(),
				"bytes", sw.written,
				"ip", ClientIP(r),
				"request_id", GetRequestID(r.Context()),
			}

			switch {
			case sw.status >= 500:
				logger.Error("http request", attrs...)
			case sw.status >= 400:
				logger.Warn("http request", attrs...)
			default:
				logger.Debug("http request", attrs...)
			}
		})
	}
}
