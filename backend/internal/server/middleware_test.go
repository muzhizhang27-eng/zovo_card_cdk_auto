package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestCDKCORSAllowsDeviceHeaderForConfiguredOrigin(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", "http://localhost:5173")
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(CORSMiddleware())
	r.POST("/preview", func(c *gin.Context) { c.Status(http.StatusOK) })
	for _, origin := range []string{"http://localhost:5173", "https://untrusted.invalid"} {
		req := httptest.NewRequest("OPTIONS", "/preview", nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", "POST")
		req.Header.Set("Access-Control-Request-Headers", "content-type,x-redemption-device")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != 204 || !strings.Contains(strings.ToLower(w.Header().Get("Access-Control-Allow-Headers")), "x-redemption-device") {
			t.Fatalf("device header blocked: %v", w.Header())
		}
		want := ""
		if origin == "http://localhost:5173" {
			want = origin
		}
		if got := w.Header().Get("Access-Control-Allow-Origin"); got != want {
			t.Fatalf("origin=%s got=%s want=%s", origin, got, want)
		}
	}
}
