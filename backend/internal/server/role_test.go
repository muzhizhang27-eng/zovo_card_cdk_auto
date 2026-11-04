package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestManagementSurfaceBlocksPublicAndBootstrap(t *testing.T) {
	t.Setenv("APP_ROLE", "admin")
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(adminSurface())
	r.NoRoute(func(c *gin.Context) { c.Status(http.StatusOK) })
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"POST", "/api/v1/setup/bootstrap", 404},
		{"POST", "/api/v1/public/cdk/redeem", 404},
		{"GET", "/api/v1/lookup/cdk", 404},
		{"POST", "/api/v1/webhooks/cardplatform", 404},
		{"GET", "/api/v1/public/cdk/plans", 404},
		{"GET", "/api/v1/setup/status", 200},
		{"GET", "/api/v1/public/site", 200},
		{"POST", "/api/v1/auth/admin/login", 200},
		{"GET", "/api/v1/admin/settings", 200},
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.status {
			t.Errorf("%s %s: %d", tc.method, tc.path, w.Code)
		}
	}
}
