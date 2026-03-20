/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package security

import (
	"context"
	"net/http"
	"strings"
)

type contextKey string

const ClaimsKey contextKey = "claims"

// Authenticator is chi middleware that validates JWT from Authorization header
// or from the "token" query parameter (needed for SSE/EventSource which cannot set headers).
func (j *JWTManager) Authenticator(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var tokenString string

		authHeader := r.Header.Get("Authorization")
		if authHeader != "" {
			tokenString = strings.TrimPrefix(authHeader, "Bearer ")
			if tokenString == authHeader {
				writeJSONError(w, http.StatusUnauthorized, "invalid authorization format")
				return
			}
		} else if cookie, err := r.Cookie(AuthCookieName); err == nil && cookie.Value != "" {
			tokenString = cookie.Value
		} else if qToken := r.URL.Query().Get("token"); qToken != "" {
			// Query-param tokens are only intended for SSE/EventSource which
			// cannot set custom headers. Browsers log query strings in history
			// and referrer headers, so avoid using this for non-SSE requests.
			tokenString = qToken
		} else {
			writeJSONError(w, http.StatusUnauthorized, "missing authorization")
			return
		}

		claims, err := j.Validate(tokenString)
		if err != nil {
			writeJSONError(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}

		ctx := context.WithValue(r.Context(), ClaimsKey, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireRole middleware checks that the authenticated user has the required role
func RequireRole(role string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := r.Context().Value(ClaimsKey).(*Claims)
			if !ok || claims.Role != role {
				writeJSONError(w, http.StatusForbidden, "insufficient permissions")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// GetClaims extracts claims from request context
func GetClaims(r *http.Request) *Claims {
	claims, _ := r.Context().Value(ClaimsKey).(*Claims)
	return claims
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write([]byte(`{"error":"` + msg + `"}`))
}
