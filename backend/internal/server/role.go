package server

import (
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
)

// adminSurface isolates the credential-bearing management process from public
// redemption handlers, even if a proxy configuration is accidentally widened.
func adminSurface() gin.HandlerFunc {
	return func(c *gin.Context) {
		if os.Getenv("APP_ROLE") != "admin" {
			c.Next()
			return
		}
		p := c.Request.URL.Path
		allowed := p == "/health" ||
			(p == "/api/v1/public/site" || p == "/api/v1/setup/status") && c.Request.Method == http.MethodGet ||
			strings.HasPrefix(p, "/api/v1/auth/admin/") ||
			strings.HasPrefix(p, "/api/v1/admin/") || p == "/api/v1/stats/system"
		if !allowed {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		c.Header("Cache-Control", "no-store")
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
		c.Next()
	}
}
