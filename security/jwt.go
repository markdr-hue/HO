/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package security

import (
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type Claims struct {
	SiteID   int    `json:"site_id,omitempty"`
	UserID   int    `json:"user_id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	jwt.RegisteredClaims
}

type JWTManager struct {
	secret        []byte
	expiry        time.Duration
	SecureCookies bool
}

func NewJWTManager(secret string, expiry time.Duration) *JWTManager {
	return &JWTManager{
		secret: []byte(secret),
		expiry: expiry,
	}
}

func (j *JWTManager) Generate(userID int, username, role string) (string, error) {
	return j.GenerateForSite(0, userID, username, role)
}

// GenerateForSite creates a JWT token scoped to a specific site.
// Site ID 0 means admin/unscoped token.
func (j *JWTManager) GenerateForSite(siteID, userID int, username, role string) (string, error) {
	claims := Claims{
		SiteID:   siteID,
		UserID:   userID,
		Username: username,
		Role:     role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(j.expiry)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(j.secret)
}

// Expiry returns the token expiry duration, used for cookie MaxAge.
func (j *JWTManager) Expiry() time.Duration {
	return j.expiry
}

// AuthCookieName is the name of the HttpOnly cookie used for SSE auth.
const AuthCookieName = "ho_auth"

// SetAuthCookie writes an HttpOnly, SameSite=Strict cookie with the given token.
func (j *JWTManager) SetAuthCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     AuthCookieName,
		Value:    token,
		Path:     "/admin",
		MaxAge:   int(j.expiry.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   j.SecureCookies,
	})
}

// ClearAuthCookie removes the auth cookie.
func ClearAuthCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     AuthCookieName,
		Value:    "",
		Path:     "/admin",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

// HMAC computes HMAC-SHA256 of data using the JWT secret. Used for OAuth state signing.
func (j *JWTManager) HMAC(data []byte) []byte {
	mac := hmac.New(sha256.New, j.secret)
	mac.Write(data)
	return mac.Sum(nil)
}

func (j *JWTManager) Validate(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return j.secret, nil
	})
	if err != nil {
		return nil, err
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}

	return claims, nil
}
