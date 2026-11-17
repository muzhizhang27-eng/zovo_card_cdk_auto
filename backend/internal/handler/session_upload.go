package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/auth"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/lanting"
)

const (
	retrySessionIPLimit     = 20
	retrySessionGlobalLimit = 200
	retrySessionMaxEntries  = 4096
	retrySessionWindow      = 10 * time.Minute
	retrySessionMaxBody     = 48 * 1024
)

type retrySessionCounter struct {
	hits        int
	windowStart time.Time
}

var (
	retrySessionMu    sync.Mutex
	retrySessionHits  = map[string]*retrySessionCounter{}
	retrySessionPrune time.Time
)

// retrySessionAllowed counts one request against the source IP and the process.
// The window, digest key, and entry cap follow the login limiter.
func retrySessionAllowed(ip string) (bool, time.Duration) {
	retrySessionMu.Lock()
	defer retrySessionMu.Unlock()
	now := time.Now()
	if now.Sub(retrySessionPrune) >= time.Minute || len(retrySessionHits) >= retrySessionMaxEntries-2 {
		for key, counter := range retrySessionHits {
			if now.Sub(counter.windowStart) >= retrySessionWindow {
				delete(retrySessionHits, key)
			}
		}
		retrySessionPrune = now
	}
	if strings.TrimSpace(ip) == "" {
		ip = "unknown"
	}
	keys := []string{"ip:" + auth.SecretDigest(ip), "global"}
	limits := []int{retrySessionIPLimit, retrySessionGlobalLimit}
	missing := 0
	for i, key := range keys {
		counter := retrySessionHits[key]
		if counter != nil && now.Sub(counter.windowStart) >= retrySessionWindow {
			delete(retrySessionHits, key)
			counter = nil
		}
		if counter == nil {
			missing++
			continue
		}
		if counter.hits >= limits[i] {
			return false, counter.windowStart.Add(retrySessionWindow).Sub(now)
		}
	}
	if len(retrySessionHits)+missing > retrySessionMaxEntries {
		return false, time.Minute
	}
	for _, key := range keys {
		if retrySessionHits[key] == nil {
			retrySessionHits[key] = &retrySessionCounter{windowStart: now}
		}
		retrySessionHits[key].hits++
	}
	return true, 0
}

// PublicCDKRetrySession POST /api/v1/public/cdk/retry-session
// The customer resubmits a CDK and ChatGPT Session after activation failed.
// The response never contains the session, and failures do not say whether the CDK exists.
func PublicCDKRetrySession(c *gin.Context) {
	if ok, _ := retrySessionAllowed(c.ClientIP()); !ok {
		c.Header("Retry-After", "600")
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "rate_limited"})
		return
	}
	client := lanting.FromEnv()
	if !client.Enabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "unavailable"})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, retrySessionMaxBody)
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	var req struct {
		Code    string `json:"code"`
		CDKCode string `json:"cdk_code"`
		Session string `json:"session"`
	}
	if json.Unmarshal(raw, &req) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	code := strings.TrimSpace(req.Code)
	if code == "" {
		code = strings.TrimSpace(req.CDKCode)
	}
	if code == "" || len(code) > 512 || strings.TrimSpace(req.Session) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	session := lanting.AcceptActivationSession(req.Session)
	if session == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_session"})
		return
	}
	client.Attempts = 2
	ctx, cancel := context.WithTimeout(c.Request.Context(), 9*time.Second)
	defer cancel()
	status, uploadErr := client.Upload(ctx, code, session)
	if errors.Is(uploadErr, lanting.ErrDisabled) {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "unavailable"})
		return
	}
	if uploadErr == nil && status >= 200 && status < 300 {
		lanting.Remember(code, "", session)
		c.JSON(http.StatusOK, gin.H{"ok": true})
		return
	}
	if status == http.StatusTooManyRequests {
		c.Header("Retry-After", "60")
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "rate_limited"})
		return
	}
	c.JSON(http.StatusBadGateway, gin.H{"error": "upload_failed"})
}

func maybeForwardActivationSession(body map[string]any, status int, raw json.RawMessage, preflight bool) {
	session := activationSession(body["credential"])
	if session == "" {
		return
	}
	if preflight && !preflightAccepted(status, raw) {
		return
	}
	code := str(body["code"])
	if code == "" {
		code = str(body["cdk_code"])
	}
	token := str(body["redemption_token"])
	if code == "" && token != "" && db.DB != nil {
		if found, err := db.FindCodeByRedemptionToken(token); err == nil {
			code = strings.TrimSpace(found)
		}
	}
	lanting.Schedule(code, token, session, preflight)
}

func activationSession(cred any) string {
	m, ok := cred.(map[string]any)
	if !ok {
		return ""
	}
	return lanting.AcceptActivationSession(str(m["session"]))
}

func preflightAccepted(status int, raw json.RawMessage) bool {
	if status < 200 || status >= 300 || len(raw) == 0 {
		return false
	}
	var payload map[string]any
	if json.Unmarshal(raw, &payload) != nil {
		return false
	}
	if code, ok := payload["code"].(float64); ok && code != 0 {
		return false
	}
	if token, _ := payload["preflight_token"].(string); strings.TrimSpace(token) != "" {
		return true
	}
	data, _ := payload["data"].(map[string]any)
	if data == nil {
		return false
	}
	token, _ := data["preflight_token"].(string)
	return strings.TrimSpace(token) != ""
}
