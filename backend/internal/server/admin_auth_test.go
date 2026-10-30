package server_test

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/tuzi/cdk-recharge-system/internal/auth"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/handler"
	"github.com/tuzi/cdk-recharge-system/internal/server"
)

const existingAdminPassword = "Existing-Admin-Password-27"

var fixtureSequence atomic.Uint32

type adminFixture struct {
	t    *testing.T
	r    *gin.Engine
	db   *sql.DB
	hash string
	ip   string
}

func newAdminFixture(t *testing.T) *adminFixture {
	t.Helper()
	t.Setenv("SERVER_MODE", "release")
	t.Setenv("JWT_SECRET", "synthetic-test-jwt-signing-secret-32-bytes")
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://admin.test")
	gin.SetMode(gin.TestMode)
	conn, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	oldDB := db.DB
	db.DB = conn
	t.Cleanup(func() { db.DB = oldDB; conn.Close() })
	for _, q := range []string{
		`CREATE TABLE admin_users(id INTEGER PRIMARY KEY, username TEXT UNIQUE, password_hash TEXT, display_name TEXT, is_active INTEGER, updated_at DATETIME)`,
		`CREATE TABLE admin_audit_logs(id INTEGER PRIMARY KEY, username TEXT, action TEXT, detail TEXT, ip TEXT, created_at DATETIME)`,
	} {
		if _, err := conn.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	hash, err := db.HashAdminPassword(existingAdminPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO admin_users VALUES(1,'admin',?,'Admin',1,CURRENT_TIMESTAMP)`, hash); err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateAdminSessions(); err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateAdminSessions(); err != nil {
		t.Fatal(err)
	}
	var after string
	if err := conn.QueryRow(`SELECT password_hash FROM admin_users WHERE id=1`).Scan(&after); err != nil || after != hash {
		t.Fatal("session migration changed the existing password hash")
	}
	r := gin.New()
	_ = r.SetTrustedProxies(nil)
	r.POST("/login", handler.AdminLogin)
	r.POST("/logout", server.JWTAuthMiddleware(), handler.AdminLogout)
	r.POST("/change", server.JWTAuthMiddleware(), handler.AdminChangePassword)
	r.GET("/me", server.JWTAuthMiddleware(), handler.AdminMe)
	r.POST("/write", server.JWTAuthMiddleware(), server.AdminAuthMiddleware(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	n := fixtureSequence.Add(1)
	return &adminFixture{t: t, r: r, db: conn, hash: hash, ip: fmt.Sprintf("198.51.%d.%d", n/250, n%250+1)}
}

func (f *adminFixture) request(method, path, body string, cookies []*http.Cookie, headers map[string]string) *httptest.ResponseRecorder {
	f.t.Helper()
	req := httptest.NewRequest(method, "https://admin.test"+path, strings.NewReader(body))
	// Each test's distinct address prevents unrelated fixture limits from interacting.
	req.RemoteAddr = f.ip + ":1234"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://admin.test")
	for _, cookie := range cookies {
		req.AddCookie(cookie)
		if cookie.Name == "__Host-cdk_admin_csrf" {
			req.Header.Set("X-CSRF-Token", cookie.Value)
		}
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	w := httptest.NewRecorder()
	f.r.ServeHTTP(w, req)
	return w
}

func (f *adminFixture) login() []*http.Cookie {
	f.t.Helper()
	resp := f.request("POST", "/login", `{"username":"admin","password":"`+existingAdminPassword+`"}`, nil, nil)
	if resp.Code != 200 {
		f.t.Fatalf("login status=%d", resp.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		f.t.Fatal(err)
	}
	if _, ok := payload["token"]; ok {
		f.t.Fatal("login exposed a Bearer token to JavaScript")
	}
	var cookies []*http.Cookie
	for _, cookie := range resp.Result().Cookies() {
		if cookie.MaxAge < 0 {
			continue
		}
		if !cookie.Secure || cookie.Domain != "" || cookie.Path != "/" || !strings.HasPrefix(cookie.Name, "__Host-") {
			f.t.Fatal("production cookie lacks host-only HTTPS restrictions")
		}
		if cookie.HttpOnly != (cookie.Name == "__Host-cdk_admin_auth") {
			f.t.Fatal("unexpected HttpOnly attribute")
		}
		cookies = append(cookies, cookie)
	}
	if len(cookies) != 2 {
		f.t.Fatalf("expected two session cookies, got %d", len(cookies))
	}
	return cookies
}

func sessionToken(cookies []*http.Cookie) string {
	for _, cookie := range cookies {
		if cookie.Name == "__Host-cdk_admin_auth" {
			return cookie.Value
		}
	}
	return ""
}

func TestAdminLoginCSRFAndLogout(t *testing.T) {
	f := newAdminFixture(t)
	cookies := f.login()
	if w := f.request("GET", "/me", "", cookies, nil); w.Code != 200 || !strings.Contains(w.Body.String(), "expires_at") {
		t.Fatal("valid session rejected or missing expiry")
	}
	if w := f.request("POST", "/write", "{}", cookies, nil); w.Code != 204 {
		t.Fatalf("valid CSRF status=%d", w.Code)
	}
	for _, headers := range []map[string]string{
		{"X-CSRF-Token": "wrong"},
		{"Origin": "https://sibling.admin.test"},
		{"Origin": "null"},
		{"Origin": "", "Sec-Fetch-Site": "cross-site"},
	} {
		if w := f.request("POST", "/write", "{}", cookies, headers); w.Code != 403 {
			t.Fatalf("unsafe write status=%d", w.Code)
		}
	}
	forged := []*http.Cookie{{Name: "__Host-cdk_admin_auth", Value: sessionToken(cookies)}, {Name: "__Host-cdk_admin_csrf", Value: "attacker-chosen"}}
	if w := f.request("POST", "/write", "{}", forged, nil); w.Code != 403 {
		t.Fatal("unbound CSRF cookie/header pair accepted")
	}
	legacy := []*http.Cookie{{Name: "auth_token", Value: sessionToken(cookies)}}
	if w := f.request("GET", "/me", "", legacy, nil); w.Code != 401 {
		t.Fatal("production accepted an unprefixed auth cookie")
	}
	if w := f.request("POST", "/logout", "{}", cookies, nil); w.Code != 200 {
		t.Fatalf("logout status=%d", w.Code)
	}
	if w := f.request("GET", "/me", "", cookies, nil); w.Code != 401 {
		t.Fatal("logout failed to revoke the server session")
	}
	if w := f.request("GET", "/me", "", nil, map[string]string{"Authorization": "Bearer " + sessionToken(cookies)}); w.Code != 401 {
		t.Fatal("logout failed to revoke Bearer replay")
	}
}

func TestAdminPasswordChangeRevokesAllSessions(t *testing.T) {
	f := newAdminFixture(t)
	first, second := f.login(), f.login()
	if w := f.request("POST", "/change", `{"old_password":"`+existingAdminPassword+`","new_password":"New-Admin-Password-29"}`, first, nil); w.Code != 200 {
		t.Fatalf("change status=%d", w.Code)
	}
	for _, cookies := range [][]*http.Cookie{first, second} {
		if w := f.request("GET", "/me", "", cookies, nil); w.Code != 401 {
			t.Fatal("password change retained an old session")
		}
	}
	if err := db.CreateAdminSession(1, "raced-session", "csrf", time.Now().Add(time.Hour), f.hash); err == nil {
		t.Fatal("session issued after the verified password changed")
	}
}

func TestAdminDisableDeleteAndExpiryRejectSessions(t *testing.T) {
	for _, action := range []string{"disable", "delete", "expiry"} {
		t.Run(action, func(t *testing.T) {
			f := newAdminFixture(t)
			cookies := f.login()
			switch action {
			case "disable":
				if _, err := f.db.Exec(`UPDATE admin_users SET is_active=0 WHERE id=1`); err != nil {
					t.Fatal(err)
				}
				if _, err := f.db.Exec(`UPDATE admin_users SET is_active=1 WHERE id=1`); err != nil {
					t.Fatal(err)
				}
			case "delete":
				if _, err := f.db.Exec(`DELETE FROM admin_users WHERE id=1`); err != nil {
					t.Fatal(err)
				}
			case "expiry":
				if _, err := f.db.Exec(`UPDATE admin_sessions SET expires_at=0`); err != nil {
					t.Fatal(err)
				}
			}
			if w := f.request("GET", "/me", "", cookies, nil); w.Code != 401 {
				t.Fatalf("%s retained session", action)
			}
		})
	}
}

func TestAdminJWTRequiresHS256ExpiryAndStoredSession(t *testing.T) {
	f := newAdminFixture(t)
	cookies := f.login()
	valid, err := auth.ParseAdminToken(sessionToken(cookies))
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"algorithm", "expiry", "issuer", "jti", "unregistered"} {
		claims := *valid
		method := jwt.SigningMethodHS256
		switch change {
		case "algorithm":
			method = jwt.SigningMethodHS512
		case "expiry":
			claims.ExpiresAt = nil
		case "issuer":
			claims.Issuer = ""
		case "jti":
			claims.ID = ""
		case "unregistered":
			claims.ID = strings.Repeat("a", 64)
		}
		token, err := jwt.NewWithClaims(method, claims).SignedString([]byte(auth.JWTSecret()))
		if err != nil {
			t.Fatal(err)
		}
		if w := f.request("GET", "/me", "", nil, map[string]string{"Authorization": "Bearer " + token}); w.Code != 401 {
			t.Fatalf("accepted %s token", change)
		}
	}
}

func TestAdminLoginRejectsCrossOriginAndHidesAccountStatus(t *testing.T) {
	f := newAdminFixture(t)
	if w := f.request("POST", "/login", `{"username":"admin","password":"`+existingAdminPassword+`"}`, nil, map[string]string{"Origin": "https://evil.test"}); w.Code != 403 {
		t.Fatal("cross-origin login accepted")
	}
	var failure string
	for _, action := range []string{"missing", "incorrect", "disabled"} {
		username := "admin"
		if action == "missing" {
			username = "nonexistent"
		}
		if action == "disabled" {
			if _, err := f.db.Exec(`UPDATE admin_users SET is_active=0 WHERE id=1`); err != nil {
				t.Fatal(err)
			}
		}
		w := f.request("POST", "/login", `{"username":"`+username+`","password":"incorrect-password"}`, nil, nil)
		if w.Code != 401 {
			t.Fatalf("%s status=%d", action, w.Code)
		}
		if failure != "" && failure != w.Body.String() {
			t.Fatal("login exposed account existence or activation")
		}
		failure = w.Body.String()
	}
}

func TestAdminLoginIgnoresUntrustedForwardedIP(t *testing.T) {
	f := newAdminFixture(t)
	for i := 0; i < 6; i++ {
		w := f.request("POST", "/login", `{"username":"missing","password":"incorrect-password"}`, nil,
			map[string]string{"X-Forwarded-For": fmt.Sprintf("203.0.113.%d", i+1), "X-Real-IP": fmt.Sprintf("192.0.2.%d", i+1)})
		want := http.StatusUnauthorized
		if i == 5 {
			want = http.StatusTooManyRequests
		}
		if w.Code != want {
			t.Fatalf("attempt %d status=%d want=%d", i+1, w.Code, want)
		}
	}
}

func TestAdminProductionRejectsPlainHTTP(t *testing.T) {
	f := newAdminFixture(t)
	for _, path := range []string{"/login", "/logout", "/me"} {
		method := "POST"
		if path == "/me" {
			method = "GET"
		}
		req := httptest.NewRequest(method, "http://admin.test"+path, strings.NewReader(`{"username":"admin","password":"`+existingAdminPassword+`"}`))
		req.RemoteAddr = f.ip + ":1234"
		w := httptest.NewRecorder()
		f.r.ServeHTTP(w, req)
		if w.Code != http.StatusUpgradeRequired || len(w.Result().Cookies()) != 0 {
			t.Fatalf("plaintext %s status=%d", path, w.Code)
		}
	}
}

func TestAdminDevelopmentLoginUsesLocalCookies(t *testing.T) {
	f := newAdminFixture(t)
	t.Setenv("SERVER_MODE", "debug")
	t.Setenv("CORS_ALLOWED_ORIGINS", "")
	// Vite changeOrigin forwards the backend host while retaining browser Origin.
	req := httptest.NewRequest("POST", "http://127.0.0.1:8080/login", strings.NewReader(`{"username":"admin","password":"`+existingAdminPassword+`"}`))
	req.RemoteAddr = f.ip + ":1234"
	req.Header.Set("Origin", "http://127.0.0.1:5173")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("dev login status=%d", w.Code)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatal("missing dev session cookies")
	}
	for _, cookie := range cookies {
		if cookie.Secure || cookie.Domain != "" || (cookie.Name != "auth_token" && cookie.Name != "csrf_token") {
			t.Fatal("incorrect local cookie attributes")
		}
	}
}

func TestAdminLegacyPasswordAndConcurrentChange(t *testing.T) {
	f := newAdminFixture(t)
	legacyHash := fmt.Sprintf("%x", sha256.Sum256([]byte(existingAdminPassword)))
	if _, err := f.db.Exec(`UPDATE admin_users SET password_hash=? WHERE id=1`, legacyHash); err != nil {
		t.Fatal(err)
	}
	cookies := f.login()
	if w := f.request("GET", "/me", "", cookies, nil); w.Code != 200 {
		t.Fatal("legacy password session rejected")
	}
	var upgraded string
	if err := f.db.QueryRow(`SELECT password_hash FROM admin_users WHERE id=1`).Scan(&upgraded); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(upgraded, "$2") {
		t.Fatal("legacy password hash not upgraded")
	}
	if ok, _ := db.VerifyAdminPassword(upgraded, existingAdminPassword); !ok {
		t.Fatal("legacy password changed during upgrade")
	}
	if err := db.SetAdminPassword("admin", "Concurrent-New-Password-42", upgraded); err != nil {
		t.Fatal(err)
	}
	if err := db.UpgradeAdminHash("admin", legacyHash, upgraded); err == nil {
		t.Fatal("old login overwrote the new password")
	}
	if err := db.SetAdminPassword("admin", "Stale-Password-Change-44", upgraded); err == nil {
		t.Fatal("stale password verification overwrote the new password")
	}
	var finalHash string
	if err := f.db.QueryRow(`SELECT password_hash FROM admin_users WHERE id=1`).Scan(&finalHash); err != nil {
		t.Fatal(err)
	}
	if ok, _ := db.VerifyAdminPassword(finalHash, "Concurrent-New-Password-42"); !ok {
		t.Fatal("new password lost after stale updates")
	}
}
