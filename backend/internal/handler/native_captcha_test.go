package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestNativeCaptchaProxiesScopedTokensAndRawResponses(t *testing.T) {
	conn := newBillingTestDB(t)
	const token = "synthetic+token/with?reserved&characters"
	if _, err := conn.Exec(`INSERT INTO cdk_session_bindings(cdk_code, session_payload, redemption_token)
		VALUES ('SXC-SYNTHETIC-CAPTCHA', '', ?)`, token); err != nil {
		t.Fatal(err)
	}
	const params = `{"code":0,"data":{"paymentIntentId":"pi_synthetic","clientSecret":"synthetic-secret","expiresAt":180000,"extra":"preserved"}}`
	const complete = `{"code":0,"data":{"granted":false,"retry_allowed":true,"attempts_left":1,"extra":"preserved"}}`
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("X-API-Key") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("public captcha request forwarded privileged credentials")
		}
		if r.Header.Get("X-Redemption-Device") != "synthetic-device" {
			t.Error("captcha request lost device binding")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/cdk/native-captcha/params":
			if r.Method != http.MethodGet || r.URL.Query().Get("token") != token {
				t.Error("incorrect params method or token encoding")
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(params))
		case "/api/v1/cdk/native-captcha/complete":
			var body map[string]any
			if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&body) != nil || body["token"] != token || len(body) != 1 {
				t.Error("complete must forward only the resolved token")
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(complete))
		default:
			t.Errorf("unexpected upstream path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()
	t.Setenv("CARD_API_BASE", upstream.URL)
	t.Setenv("CARD_API_KEY", "synthetic-owner-key-must-not-leak")
	router := gin.New()
	router.GET("/params", PublicCDKNativeCaptchaParams)
	router.POST("/complete", PublicCDKNativeCaptchaComplete)
	for _, tc := range []struct {
		name, method, path, body, response string
		status                             int
	}{
		{"params-token", "GET", "/params?token=" + url.QueryEscape(token), "", params, 200},
		{"params-code", "GET", "/params?code=SXC-SYNTHETIC-CAPTCHA", "", params, 200},
		{"params-code-alias", "GET", "/params?cdk_code=SXC-SYNTHETIC-CAPTCHA", "", params, 200},
		{"complete-token", "POST", "/complete", `{"token":"` + token + `"}`, complete, 202},
		{"complete-code", "POST", "/complete", `{"code":"SXC-SYNTHETIC-CAPTCHA"}`, complete, 202},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-Redemption-Device", "synthetic-device")
			request.Header.Set("Authorization", "Bearer synthetic-admin-token")
			request.Header.Set("Cookie", "admin=synthetic-admin-cookie")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != tc.status || response.Body.String() != tc.response {
				t.Fatalf("response status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
	if calls != 5 {
		t.Fatalf("upstream calls = %d, want 5", calls)
	}
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/params", ""},
		{"GET", "/params?code=SXC-NOT-BOUND", ""},
		{"POST", "/complete", `{}`},
		{"POST", "/complete", `{"code":"SXC-NOT-BOUND"}`},
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("missing or unresolved token status = %d, want 400", response.Code)
		}
	}
	if calls != 5 {
		t.Fatal("missing or unresolved token reached upstream")
	}
}
