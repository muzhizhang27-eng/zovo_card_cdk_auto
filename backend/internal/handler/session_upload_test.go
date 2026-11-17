package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/lanting"
)

const (
	uploadSecret  = "synthetic-hmac-secret-0123456789abcd"
	uploadSession = `{"sessionToken":"synthetic-session-token-value-0001","note":"keep a & b"}`
	uploadCode    = "SXC-SYNTHETIC-0001"
)

func resetRetrySessionLimits(t *testing.T) {
	t.Helper()
	retrySessionMu.Lock()
	oldHits, oldPrune := retrySessionHits, retrySessionPrune
	retrySessionHits = map[string]*retrySessionCounter{}
	retrySessionPrune = time.Time{}
	retrySessionMu.Unlock()
	t.Cleanup(func() {
		retrySessionMu.Lock()
		retrySessionHits, retrySessionPrune = oldHits, oldPrune
		retrySessionMu.Unlock()
	})
}

func TestRetrySessionLimitBoundsIPAndStorage(t *testing.T) {
	resetRetrySessionLimits(t)
	var accepted atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, _ := retrySessionAllowed("192.0.2.55"); ok {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != retrySessionIPLimit {
		t.Fatalf("accepted=%d want=%d", accepted.Load(), retrySessionIPLimit)
	}
	if ok, _ := retrySessionAllowed("192.0.2.56"); !ok {
		t.Fatal("a different IP was limited by the first address")
	}
	retrySessionMu.Lock()
	retrySessionHits = map[string]*retrySessionCounter{}
	for i := 0; i < retrySessionMaxEntries; i++ {
		retrySessionHits[itoa(i)] = &retrySessionCounter{hits: 1, windowStart: time.Now()}
	}
	retrySessionPrune = time.Now()
	retrySessionMu.Unlock()
	if ok, _ := retrySessionAllowed("203.0.113.10"); ok {
		t.Fatal("entry cap was bypassed")
	}
	if len(retrySessionHits) > retrySessionMaxEntries {
		t.Fatalf("unbounded rate-limit storage: %d", len(retrySessionHits))
	}
	retrySessionMu.Lock()
	for _, entry := range retrySessionHits {
		entry.windowStart = time.Now().Add(-retrySessionWindow - time.Second)
	}
	retrySessionMu.Unlock()
	if ok, _ := retrySessionAllowed("203.0.113.11"); !ok {
		t.Fatal("expired entries were not removed")
	}
	if len(retrySessionHits) != 2 {
		t.Fatalf("expired entries remain: %d", len(retrySessionHits))
	}
}

func TestPublicRetrySessionHidesTokenAndCDKExistence(t *testing.T) {
	resetRetrySessionLimits(t)
	gin.SetMode(gin.TestMode)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if r.URL.RawQuery != "" || strings.Contains(r.URL.RequestURI(), "synthetic-session") {
			t.Errorf("token leaked into the upstream url: %s", r.URL.RequestURI())
		}
		if strings.Contains(string(body), "synthetic-session-token-value-0001") && r.URL.Path != "/api/v1/internal/cdk-activation-token" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"accepted":true,"order_id":"order-synthetic","session":"synthetic-session-token-value-0001"}`)
	}))
	t.Cleanup(upstream.Close)
	t.Setenv("LANTING_TOKEN_UPLOAD_URL", upstream.URL)
	t.Setenv("LANTING_CDK_TOKEN_HMAC_SECRET", uploadSecret)

	router := gin.New()
	_ = router.SetTrustedProxies(nil)
	router.POST("/retry-session", PublicCDKRetrySession)

	ok := postRetrySession(t, router, "192.0.2.70:1000", uploadCode, uploadSession)
	if ok.Code != http.StatusOK || ok.Body.String() != `{"ok":true}` {
		t.Fatalf("success status=%d body=%s", ok.Code, ok.Body.String())
	}
	if strings.Contains(ok.Body.String(), "synthetic-session") || strings.Contains(ok.Body.String(), "order-synthetic") {
		t.Fatalf("response echoed upstream data: %s", ok.Body.String())
	}

	missing := postRetrySession(t, router, "192.0.2.71:1000", uploadCode, "eyJ.fixture.payload")
	if missing.Code != http.StatusBadRequest || missing.Body.String() != `{"error":"invalid_session"}` || strings.Contains(missing.Body.String(), "eyJ.fixture.payload") {
		t.Fatalf("invalid status=%d body=%s", missing.Code, missing.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("invalid session reached lanting, calls=%d", calls.Load())
	}

	upstream.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"code":"CDK_NOT_FOUND","session":"synthetic-session-token-value-0001"}}`)
	})
	failed := postRetrySession(t, router, "192.0.2.72:1000", "SXC-SYNTHETIC-0002", uploadSession)
	if failed.Code != http.StatusBadGateway || failed.Body.String() != `{"error":"upload_failed"}` {
		t.Fatalf("failure status=%d body=%s", failed.Code, failed.Body.String())
	}
	if strings.Contains(failed.Body.String(), "CDK_NOT_FOUND") || strings.Contains(failed.Body.String(), "synthetic-session") {
		t.Fatalf("failure revealed lookup or token: %s", failed.Body.String())
	}

	t.Setenv("LANTING_TOKEN_UPLOAD_URL", "")
	t.Setenv("LANTING_CDK_TOKEN_HMAC_SECRET", "")
	disabled := postRetrySession(t, router, "192.0.2.73:1000", uploadCode, uploadSession)
	if disabled.Code != http.StatusServiceUnavailable || strings.Contains(disabled.Body.String(), "synthetic-session") {
		t.Fatalf("disabled status=%d body=%s", disabled.Code, disabled.Body.String())
	}

	for i := 0; i < retrySessionIPLimit; i++ {
		retrySessionAllowed("192.0.2.80")
	}
	limited := postRetrySession(t, router, "192.0.2.80:1000", uploadCode, uploadSession)
	if limited.Code != http.StatusTooManyRequests || strings.Contains(limited.Body.String(), "synthetic-session") || limited.Body.String() != `{"error":"rate_limited"}` {
		t.Fatalf("limited status=%d body=%s", limited.Code, limited.Body.String())
	}
}

func TestPreflightAndRedeemIsolateLantingFailures(t *testing.T) {
	newBillingTestDB(t)
	gin.SetMode(gin.TestMode)
	const upstreamBody = `{"code":0,"data":{"preflight_token":"synthetic-preflight","email":"member@example.invalid"}}`
	card := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/preflight"):
			_, _ = io.WriteString(w, upstreamBody)
		case strings.HasSuffix(r.URL.Path, "/redeem"):
			_, _ = io.WriteString(w, `{"code":0,"data":{"status":"pending"}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(card.Close)
	release := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(release) }) }
	t.Cleanup(stop)
	var calls atomic.Int32
	var sawTokenInURL atomic.Bool
	var uploaded atomic.Value
	lantingServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.RawQuery != "" || strings.Contains(r.URL.RequestURI(), "synthetic-session") {
			sawTokenInURL.Store(true)
		}
		body, _ := io.ReadAll(r.Body)
		uploaded.Store(string(body))
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"accepted":true,"session":"synthetic-session-token-value-0001"}`)
	}))
	t.Cleanup(lantingServer.Close)
	t.Cleanup(func() {
		stop()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = lanting.Wait(ctx)
	})
	t.Setenv("CARD_API_BASE", card.URL)
	t.Setenv("LANTING_TOKEN_UPLOAD_URL", lantingServer.URL)
	t.Setenv("LANTING_CDK_TOKEN_HMAC_SECRET", uploadSecret)

	router := gin.New()
	router.POST("/preflight", PublicCDKPreflight)
	router.POST("/redeem", PublicCDKRedeem)
	body := map[string]any{
		"code":             uploadCode,
		"redemption_token": "synthetic-redemption-token",
		"credential":       map[string]string{"mode": "session", "session": uploadSession},
	}
	started := time.Now()
	response := postJSON(router, "/preflight", "192.0.2.90:1000", body)
	if time.Since(started) > 1500*time.Millisecond {
		t.Fatalf("preflight waited for lanting: %s", time.Since(started))
	}
	if response.Code != http.StatusOK || response.Body.String() != upstreamBody {
		t.Fatalf("preflight status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "synthetic-session-token-value-0001") {
		t.Fatal("preflight response contained the session")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := lanting.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatalf("preflight uploaded to lanting, calls=%d", calls.Load())
	}

	// The web redeem body has no Session and no CDK. Both come from the preflight binding.
	// A different credential on this request must not replace the session the preflight used.
	other := strings.Replace(uploadSession, "0001", "0002", 1)
	redeemStarted := time.Now()
	redeem := postJSON(router, "/redeem", "192.0.2.90:1000", map[string]any{
		"redemption_token":  "synthetic-redemption-token",
		"preflight_token":   "synthetic-preflight",
		"client_request_id": "synthetic-request",
		"credential":        map[string]string{"mode": "session", "session": other},
	})
	if time.Since(redeemStarted) > 1500*time.Millisecond {
		t.Fatalf("redeem waited for lanting: %s", time.Since(redeemStarted))
	}
	if redeem.Code != http.StatusOK || !strings.Contains(redeem.Body.String(), `"status":"pending"`) {
		t.Fatalf("redeem status=%d body=%s", redeem.Code, redeem.Body.String())
	}
	if strings.Contains(redeem.Body.String(), "synthetic-session") {
		t.Fatal("redeem response contained the session")
	}
	stop()
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	if err := lanting.Wait(ctx2); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || sawTokenInURL.Load() {
		t.Fatalf("calls=%d token_in_url=%t", calls.Load(), sawTokenInURL.Load())
	}
	sent, _ := uploaded.Load().(string)
	if !strings.Contains(sent, uploadCode) || !strings.Contains(sent, "synthetic-session-token-value-0001") || strings.Contains(sent, "synthetic-session-token-value-0002") {
		t.Fatalf("forwarded the wrong session: %s", sent)
	}
}

func TestRedeemWithoutPreflightUsesSubmittedSession(t *testing.T) {
	newBillingTestDB(t)
	gin.SetMode(gin.TestMode)
	card := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"code":0,"data":{"status":"pending"}}`)
	}))
	t.Cleanup(card.Close)
	var calls atomic.Int32
	var uploaded atomic.Value
	lantingServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		uploaded.Store(string(body))
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(lantingServer.Close)
	t.Setenv("CARD_API_BASE", card.URL)
	t.Setenv("LANTING_TOKEN_UPLOAD_URL", lantingServer.URL)
	t.Setenv("LANTING_CDK_TOKEN_HMAC_SECRET", uploadSecret)
	router := gin.New()
	router.POST("/redeem", PublicCDKRedeem)

	grok := postJSON(router, "/redeem", "192.0.2.93:1000", map[string]any{
		"code":       "SXC-SYNTHETIC-0004",
		"credential": map[string]string{"mode": "session", "session": "sso=" + strings.Repeat("a", 40)},
	})
	if grok.Code != http.StatusOK {
		t.Fatalf("grok redeem status=%d", grok.Code)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := lanting.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("grok credential was uploaded")
	}

	card2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"code":0,"data":{"status":"pending"}}`)
	}))
	t.Cleanup(card2.Close)
	t.Setenv("CARD_API_BASE", card2.URL)
	redeem := postJSON(router, "/redeem", "192.0.2.94:1000", map[string]any{
		"code":             "SXC-SYNTHETIC-0005",
		"redemption_token": "synthetic-redemption-token-2",
		"credential":       map[string]string{"mode": "session", "session": uploadSession},
	})
	if redeem.Code != http.StatusOK || strings.Contains(redeem.Body.String(), "synthetic-session") {
		t.Fatalf("redeem status=%d body=%s", redeem.Code, redeem.Body.String())
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	if err := lanting.Wait(ctx2); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d", calls.Load())
	}
	sent, _ := uploaded.Load().(string)
	if !strings.Contains(sent, "SXC-SYNTHETIC-0005") || !strings.Contains(sent, "synthetic-session-token-value-0001") {
		t.Fatalf("forwarded body = %s", sent)
	}
}

func TestFailedPreflightDoesNotUpload(t *testing.T) {
	newBillingTestDB(t)
	gin.SetMode(gin.TestMode)
	card := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"code":1,"msg":"fixture rejection"}`)
	}))
	t.Cleanup(card.Close)
	var calls atomic.Int32
	lantingServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(lantingServer.Close)
	t.Setenv("CARD_API_BASE", card.URL)
	t.Setenv("LANTING_TOKEN_UPLOAD_URL", lantingServer.URL)
	t.Setenv("LANTING_CDK_TOKEN_HMAC_SECRET", uploadSecret)
	router := gin.New()
	router.POST("/preflight", PublicCDKPreflight)
	response := postJSON(router, "/preflight", "192.0.2.92:1000", map[string]any{
		"code":       "SXC-SYNTHETIC-0003",
		"credential": map[string]string{"mode": "session", "session": uploadSession},
	})
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "fixture rejection") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := lanting.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatalf("failed preflight uploaded, calls=%d", calls.Load())
	}
}

func postRetrySession(t *testing.T, router http.Handler, remote, code, session string) *httptest.ResponseRecorder {
	t.Helper()
	return postJSON(router, "/retry-session", remote, map[string]string{"code": code, "session": session})
}

func postJSON(router http.Handler, path, remote string, body any) *httptest.ResponseRecorder {
	encoded, _ := json.Marshal(body)
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(encoded))
	request.RemoteAddr = remote
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func itoa(n int) string {
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	var b [16]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = digits[n%10]
		n /= 10
	}
	return string(b[i:])
}
