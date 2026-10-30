package server

import (
	"fmt"
	"io"
	"net/http"
	"runtime/debug"

	"github.com/gin-gonic/gin"
)

// Gin's default formatter appends RawQuery to Path. Public lookup URLs contain
// CDKs and redemption tokens, so log only the path and operational metadata.
// Private handler errors may also contain upstream URLs or credentials.
func requestLogger(out io.Writer) gin.HandlerFunc {
	return gin.LoggerWithConfig(gin.LoggerConfig{
		Output: out,
		Formatter: func(p gin.LogFormatterParams) string {
			return fmt.Sprintf("[HTTP] time=%s method=%q path=%q status=%d duration=%s ip=%q bytes=%d\n",
				p.TimeStamp.Format("2006-01-02T15:04:05.000Z07:00"),
				p.Method, p.Request.URL.EscapedPath(), p.StatusCode, p.Latency, p.ClientIP, p.BodySize)
		},
	})
}

// Default recovery can dump the full request and panic value. Keep a stack and
// the panic type for diagnosis without logging headers, queries or payloads.
func requestRecovery(out io.Writer) gin.HandlerFunc {
	return gin.CustomRecoveryWithWriter(nil, func(c *gin.Context, err any) {
		fmt.Fprintf(out, "[HTTP recovery] method=%q path=%q panic_type=%T\n%s",
			c.Request.Method, c.Request.URL.EscapedPath(), err, debug.Stack())
		c.AbortWithStatus(http.StatusInternalServerError)
	})
}
