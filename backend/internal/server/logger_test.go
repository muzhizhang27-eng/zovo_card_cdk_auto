package server

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRequestLogsExcludeCredentialsWithoutChangingRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const code = "synthetic-unused-cdk"
	const token = "synthetic-redemption-token"
	const session = `{"accessToken":"synthetic-session-secret"}`
	const authorization = "Bearer synthetic-auth-secret"
	const cookie = "auth_token=synthetic-cookie-secret"
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			var logs bytes.Buffer
			r := gin.New()
			r.Use(requestLogger(&logs), requestRecovery(&logs))
			path := "/api/v1/public/cdk/result"
			r.Handle(method, path, func(c *gin.Context) {
				if c.Query("code") != code || c.Query("token") != token {
					t.Error("logging changed the request query")
				}
				body, err := io.ReadAll(c.Request.Body)
				if err != nil || string(body) != session {
					t.Error("logging changed the request body")
				}
				if c.GetHeader("Authorization") != authorization || c.GetHeader("Cookie") != cookie {
					t.Error("logging changed request headers")
				}
				_ = c.Error(errors.New("upstream failed for token=" + token))
				c.Status(http.StatusBadRequest)
			})
			req := httptest.NewRequest(method, path+"?code="+code+"&token="+token, strings.NewReader(session))
			req.Header.Set("Authorization", authorization)
			req.Header.Set("Cookie", cookie)
			response := httptest.NewRecorder()
			r.ServeHTTP(response, req)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("response status=%d", response.Code)
			}
			output := logs.String()
			for _, secret := range []string{code, token, session, authorization, cookie, "?", "upstream failed"} {
				if strings.Contains(output, secret) {
					t.Errorf("request log exposed sensitive request data: %q", secret)
				}
			}
			for _, field := range []string{`method="` + method + `"`, `path="` + path + `"`, "status=400", "duration=", "ip=", "bytes="} {
				if !strings.Contains(output, field) {
					t.Errorf("request log missing diagnostic field %q", field)
				}
			}
		})
	}
}

func TestRecoveryLogsExcludePanicAndRequestCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var logs bytes.Buffer
	r := gin.New()
	r.Use(requestLogger(&logs), requestRecovery(&logs))
	r.GET("/api/v1/public/cdk/result", func(c *gin.Context) {
		panic("synthetic-panic-secret")
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/public/cdk/result?token=synthetic-query-secret", nil)
	req.Header.Set("Cookie", "auth_token=synthetic-cookie-secret")
	response := httptest.NewRecorder()
	r.ServeHTTP(response, req)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("panic response status=%d", response.Code)
	}
	output := logs.String()
	for _, secret := range []string{"synthetic-panic-secret", "synthetic-query-secret", "synthetic-cookie-secret"} {
		if strings.Contains(output, secret) {
			t.Errorf("recovery log exposed sensitive data: %q", secret)
		}
	}
	for _, field := range []string{"[HTTP recovery]", "panic_type=string", "logger_test.go:", "status=500", "duration="} {
		if !strings.Contains(output, field) {
			t.Errorf("recovery log missing diagnostic field %q", field)
		}
	}
}
