/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package middleware

import (
	"log/slog"
	"net/http"
)

// CORS returns middleware that sets Cross-Origin Resource Sharing headers.
// In development (when allowedOrigins is empty or contains "*"), all origins
// are permitted. Otherwise, only the listed origins are allowed.
func CORS(allowedOrigins []string) func(http.Handler) http.Handler {
	allowAll := len(allowedOrigins) == 0
	if !allowAll {
		for _, o := range allowedOrigins {
			if o == "*" {
				allowAll = true
				break
			}
		}
	}

	if allowAll {
		slog.Warn("CORS: allowing all origins (Access-Control-Allow-Origin: *)")
	}

	originSet := make(map[string]struct{}, len(allowedOrigins))
	for _, o := range allowedOrigins {
		originSet[o] = struct{}{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")

			if allowAll {
				w.Header().Set("Access-Control-Allow-Origin", "*")
				// Credentials are incompatible with wildcard origin per spec.
			} else if origin != "" {
				if _, ok := originSet[origin]; ok {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Set("Access-Control-Allow-Credentials", "true")
					w.Header().Set("Vary", "Origin")
				}
			}

			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS, PATCH")
			w.Header().Set("Access-Control-Allow-Headers", "Accept, Authorization, Content-Type, X-Request-ID")
			w.Header().Set("Access-Control-Max-Age", "86400")

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
