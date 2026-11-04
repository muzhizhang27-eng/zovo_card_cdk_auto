package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/auth"
)

const (
	authCookieName = "auth_token"
	csrfCookieName = "csrf_token"
)

// isSecureRequest 判断当前请求是否走 HTTPS（直连 TLS 或反代标记）。
func isSecureRequest(c *gin.Context) bool {
	return auth.SecureCookies(c.Request)
}

// setAuthCookies 下发 HttpOnly 的认证 cookie 与可被前端读取的 CSRF cookie。
func setAuthCookies(c *gin.Context, token, csrf string, maxAgeSeconds int) {
	secure := isSecureRequest(c)
	authName, csrfName := auth.CookieNames(c.Request)
	c.SetSameSite(http.SameSiteLaxMode)
	if secure {
		// Retire legacy unprefixed cookies; HTTPS authentication never reads them.
		c.SetCookie(authCookieName, "", -1, "/", "", true, true)
		c.SetCookie(csrfCookieName, "", -1, "/", "", true, false)
	}
	c.SetCookie(authName, token, maxAgeSeconds, "/", "", secure, true)
	c.SetCookie(csrfName, csrf, maxAgeSeconds, "/", "", secure, false)
	c.Header("Cache-Control", "no-store")
}

// clearAuthCookies 清除认证相关 cookie（登出）。
func clearAuthCookies(c *gin.Context) {
	c.SetSameSite(http.SameSiteLaxMode)
	secure := isSecureRequest(c)
	authName, csrfName := auth.CookieNames(c.Request)
	c.SetCookie(authName, "", -1, "/", "", secure, true)
	c.SetCookie(csrfName, "", -1, "/", "", secure, false)
	if secure {
		c.SetCookie(authCookieName, "", -1, "/", "", true, true)
		c.SetCookie(csrfCookieName, "", -1, "/", "", true, false)
	}
	c.Header("Cache-Control", "no-store")
}
