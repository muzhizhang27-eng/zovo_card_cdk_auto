package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

const AdminIssuer = "cdk-admin"

type CustomClaims struct {
	UserID   int64  `json:"user_id"`
	IsAdmin  bool   `json:"is_admin"`
	Username string `json:"username"`
	jwt.RegisteredClaims
}

func JWTSecret() string {
	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		secret = "your-secret-key-change-in-production"
	}
	return secret
}

func NewSessionSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func SecretDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func ParseAdminToken(raw string) (*CustomClaims, error) {
	claims := &CustomClaims{}
	token, err := jwt.ParseWithClaims(raw, claims, func(token *jwt.Token) (any, error) {
		return []byte(JWTSecret()), nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithExpirationRequired(),
		jwt.WithIssuer(AdminIssuer), jwt.WithIssuedAt())
	if err != nil || token == nil || !token.Valid || !claims.IsAdmin || claims.UserID <= 0 ||
		claims.Username == "" || len(claims.ID) != 64 || claims.IssuedAt == nil {
		return nil, fmt.Errorf("invalid admin session")
	}
	return claims, nil
}

// In production the application must be bound to loopback behind a proxy that
// overwrites X-Forwarded-Proto, or serve TLS directly.
func SecureTransport(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func RequireSecureTransport(r *http.Request) bool {
	return !strings.EqualFold(os.Getenv("SERVER_MODE"), "release") || SecureTransport(r)
}

func SecureCookies(r *http.Request) bool {
	return strings.EqualFold(os.Getenv("SERVER_MODE"), "release") || SecureTransport(r)
}

func CookieNames(r *http.Request) (authName, csrfName string) {
	if SecureCookies(r) {
		return "__Host-cdk_admin_auth", "__Host-cdk_admin_csrf"
	}
	return "auth_token", "csrf_token"
}

// Check browser origins even on login and Bearer writes. Non-browser clients
// may omit Origin; cookie-authenticated writes still require a session-bound CSRF token.
func WriteOriginAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" || site == "same-site" {
			return false
		}
		if referer := r.Header.Get("Referer"); referer != "" {
			u, err := url.Parse(referer)
			if err != nil || u.Host == "" {
				return false
			}
			origin = u.Scheme + "://" + u.Host
		} else {
			return true
		}
	}
	u, err := url.Parse(origin)
	if err != nil || u.User != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") ||
		u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return false
	}
	origin = strings.ToLower(u.Scheme + "://" + u.Host)
	scheme := "http"
	if SecureCookies(r) {
		scheme = "https"
	}
	if origin == strings.ToLower(scheme+"://"+r.Host) {
		return true
	}
	allowed := os.Getenv("CORS_ALLOWED_ORIGINS")
	if allowed == "" && !strings.EqualFold(os.Getenv("SERVER_MODE"), "release") {
		allowed = "http://localhost:5173,http://127.0.0.1:5173,http://localhost:5174,http://127.0.0.1:5174"
	}
	for _, candidate := range strings.Split(allowed, ",") {
		if origin == strings.ToLower(strings.TrimRight(strings.TrimSpace(candidate), "/")) {
			return true
		}
	}
	return false
}
