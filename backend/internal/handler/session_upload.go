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

// forwardRedeemActivationSession runs when the customer submits redeem.
// The web client does not send the Session again. With a preflight token, the
// session that was bound for that redemption is the one the card platform uses.
// A redeem that carries its own ChatGPT session and no preflight token uses that
// session. The value is never written back to the HTTP response.
func forwardRedeemActivationSession(body map[string]any) {
	token := str(body["redemption_token"])
	code := str(body["code"])
	if code == "" {
		code = str(body["cdk_code"])
	}
	bodySession := activationSession(body["credential"])
	boundSession, boundCode := boundActivationSession(token, code)
	if code == "" {
		code = boundCode
	}
	session := ""
	if str(body["preflight_token"]) != "" {
		session = boundSession
		if session == "" {
			session = bodySession
		}
	} else if bodySession != "" {
		session = bodySession
	} else {
		session = boundSession
	}
	if session == "" || code == "" {
		return
	}
	lanting.Schedule(code, token, session, true)
}

func boundActivationSession(redemptionToken, code string) (session, boundCode string) {
	if db.DB == nil {
		return "", ""
	}
	if redemptionToken != "" {
		cdk, raw, err := db.ActivationBindingByToken(redemptionToken)
		if err == nil {
			boundCode = strings.TrimSpace(cdk)
			session = lanting.AcceptActivationSession(raw)
		}
	}
	if session == "" && code != "" {
		if raw, err := db.GetSessionByCDK(code); err == nil {
			session = lanting.AcceptActivationSession(raw)
		}
	}
	return session, boundCode
}

func activationSession(cred any) string {
	m, ok := cred.(map[string]any)
	if !ok {
		return ""
	}
	return lanting.AcceptActivationSession(str(m["session"]))
}
