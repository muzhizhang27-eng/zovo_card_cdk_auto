package lanting

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	uploadPath       = "/api/v1/internal/cdk-activation-token"
	minSecretLength  = 32
	maxUploadBytes   = 48 * 1024
	defaultAttempts  = 3
	uploadTimeout    = 4 * time.Second
	backgroundBudget = 15 * time.Second
	seenCap          = 4096
)

var (
	// ErrDisabled means the upload URL or HMAC secret is unset or rejected.
	ErrDisabled = errors.New("disabled")
	errFailed   = errors.New("failed")

	defaultHTTP = &http.Client{
		Timeout: uploadTimeout,
		Transport: &http.Transport{
			Proxy: nil, // lanting is local; do not inherit the Zovo/ChatGPT proxy
			DialContext: (&net.Dialer{
				Timeout:   2 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2:   false,
			MaxIdleConns:        10,
			IdleConnTimeout:     30 * time.Second,
			TLSHandshakeTimeout: 2 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	seenMu sync.Mutex
	seen   = map[string]string{}

	uploads sync.WaitGroup

	disabledLog sync.Once
)

// Client posts a CDK activation token to lanting. A zero Client is not used;
// FromEnv reads the process environment on each call.
type Client struct {
	BaseURL  string
	Secret   string
	HTTP     *http.Client
	Now      func() time.Time
	Attempts int
	Backoff  func(attempt int) time.Duration
	Log      func(string)
}

// FromEnv disables the feature when either value is unset.
// LANTING_TOKEN_UPLOAD_URL is the lanting origin, for example http://127.0.0.1:4313.
// LANTING_CDK_TOKEN_HMAC_SECRET is the shared HMAC key (at least 32 characters).
func FromEnv() Client {
	return Client{
		BaseURL: strings.TrimSpace(os.Getenv("LANTING_TOKEN_UPLOAD_URL")),
		Secret:  strings.TrimSpace(os.Getenv("LANTING_CDK_TOKEN_HMAC_SECRET")),
	}
}

func (c Client) Enabled() bool {
	urlSet := strings.TrimSpace(c.BaseURL) != ""
	secret := strings.TrimSpace(c.Secret)
	if !urlSet && secret == "" {
		return false
	}
	if !urlSet || len(secret) < minSecretLength {
		logDisabled("incomplete")
		return false
	}
	if _, err := resolveURL(c.BaseURL); err != nil {
		logDisabled("url_rejected")
		return false
	}
	return true
}

func logDisabled(reason string) {
	disabledLog.Do(func() {
		log.Printf("[lanting-token] disabled reason=%s", reason)
	})
}

// Schedule forwards the session without waiting. always is set for a successful
// preflight. A redeem schedules only when this process has not already forwarded
// the same session for the code or redemption token.
func Schedule(code, redemptionToken, session string, always bool) {
	client := FromEnv()
	if !client.Enabled() {
		return
	}
	code = strings.TrimSpace(code)
	session = strings.TrimSpace(session)
	redemptionToken = strings.TrimSpace(redemptionToken)
	if code == "" || session == "" {
		return
	}
	if !claim(code, redemptionToken, tokenDigest(session), always) {
		return
	}
	client.Attempts = defaultAttempts
	uploads.Add(1)
	go func() {
		defer uploads.Done()
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("[lanting-token] outcome=panic panic_type=%T", rec)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), backgroundBudget)
		defer cancel()
		_, _ = client.Upload(ctx, code, session)
	}()
}

// Wait blocks until background uploads scheduled so far have finished.
func Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		uploads.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

// Remember records a session that was already forwarded, so a later redeem of
// the same value does not upload it again.
func Remember(code, redemptionToken, session string) {
	code = strings.TrimSpace(code)
	session = strings.TrimSpace(session)
	redemptionToken = strings.TrimSpace(redemptionToken)
	if code == "" || session == "" {
		return
	}
	seenMu.Lock()
	defer seenMu.Unlock()
	rememberLocked(code, redemptionToken, tokenDigest(session))
}

func claim(code, token, digest string, always bool) bool {
	seenMu.Lock()
	defer seenMu.Unlock()
	if len(seen) > seenCap {
		seen = map[string]string{}
	}
	if !always && !changedLocked(code, token, digest) {
		return false
	}
	rememberLocked(code, token, digest)
	return true
}

func changedLocked(code, token, digest string) bool {
	known := false
	for _, key := range []string{memKey("c", code), memKey("t", token)} {
		if key == "" {
			continue
		}
		prev, ok := seen[key]
		if !ok {
			continue
		}
		known = true
		if prev != digest {
			return true
		}
	}
	return !known
}

func rememberLocked(code, token, digest string) {
	if key := memKey("c", code); key != "" {
		seen[key] = digest
	}
	if key := memKey("t", token); key != "" {
		seen[key] = digest
	}
}

func memKey(kind, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(kind + "|" + value))
	return kind + ":" + hex.EncodeToString(sum[:])
}

func tokenDigest(session string) string {
	sum := sha256.Sum256([]byte(session))
	return hex.EncodeToString(sum[:])
}

// Upload signs one request and retries only network failures and 408/429/5xx.
// The returned error is a class name and never includes the session or secret.
func (c Client) Upload(ctx context.Context, code, session string) (int, error) {
	if !c.Enabled() {
		return 0, ErrDisabled
	}
	endpoint, err := resolveURL(c.BaseURL)
	if err != nil {
		return 0, ErrDisabled
	}
	code = strings.TrimSpace(code)
	body, err := marshalUpload(code, session)
	if err != nil || len(body) > maxUploadBytes {
		c.logOutcome("rejected", 0, "BODY_TOO_LARGE", 0, code, session)
		return 0, errFailed
	}
	attempts := c.Attempts
	if attempts <= 0 {
		attempts = defaultAttempts
	}
	var lastStatus int
	var lastCode, lastClass string
	for attempt := 1; attempt <= attempts; attempt++ {
		if ctx.Err() != nil {
			lastClass = "timeout"
			break
		}
		status, codeName, class, callErr := c.post(ctx, endpoint, body)
		lastStatus, lastCode, lastClass = status, codeName, class
		if callErr == nil && status >= 200 && status < 300 {
			c.logOutcome("accepted", status, "", attempt, code, session)
			return status, nil
		}
		if !retryable(status, callErr) || attempt == attempts {
			break
		}
		if !sleep(ctx, c.backoff(attempt)) {
			lastClass = "timeout"
			break
		}
	}
	outcome := "failed"
	if lastStatus >= 400 && lastStatus < 500 {
		outcome = "rejected"
	}
	if lastClass == "" {
		lastClass = "upstream"
	}
	c.logOutcome(outcome, lastStatus, lastCode, attempts, code, session)
	return lastStatus, errors.New(lastClass)
}

func (c Client) backoff(attempt int) time.Duration {
	if c.Backoff != nil {
		return c.Backoff(attempt)
	}
	return time.Duration(attempt) * 200 * time.Millisecond
}

func (c Client) post(ctx context.Context, endpoint string, body []byte) (int, string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, "", "network", err
	}
	timestamp := c.timestamp()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Lanting-Timestamp", timestamp)
	req.Header.Set("X-Lanting-Signature", sign(c.Secret, timestamp, body))
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = defaultHTTP
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		class := "network"
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			class = "timeout"
		}
		return 0, "", class, err
	}
	defer resp.Body.Close()
	code := readErrorCode(resp.Body)
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, code, "upstream", nil
}

func (c Client) timestamp() string {
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	return strconv.FormatInt(now.UnixMilli(), 10)
}

func sign(secret, timestamp string, raw []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp))
	_, _ = mac.Write([]byte{'.'})
	_, _ = mac.Write(raw)
	return hex.EncodeToString(mac.Sum(nil))
}

func marshalUpload(code, session string) ([]byte, error) {
	payload := struct {
		CDKCode    string `json:"cdk_code"`
		Credential struct {
			Mode    string `json:"mode"`
			Session string `json:"session"`
		} `json:"credential"`
	}{CDKCode: code}
	payload.Credential.Mode = "session"
	payload.Credential.Session = session
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(&payload); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func retryable(status int, err error) bool {
	if err != nil {
		return true
	}
	switch status {
	case http.StatusRequestTimeout, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func readErrorCode(r io.Reader) string {
	raw, _ := io.ReadAll(io.LimitReader(r, 2048))
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return ""
	}
	code := payload.Error.Code
	if len(code) == 0 || len(code) > 64 {
		return ""
	}
	for _, ch := range code {
		if (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') && ch != '_' {
			return ""
		}
	}
	return code
}

func (c Client) logOutcome(outcome string, status int, errorCode string, attempts int, code, session string) {
	line := fmt.Sprintf("[lanting-token] outcome=%s http_status=%d error_code=%s attempts=%d cdk_fp=%s",
		outcome, status, errorCode, attempts, fingerprint(code))
	line = redact(line, c.Secret, session, code)
	if c.Log != nil {
		c.Log(line)
		return
	}
	log.Print(line)
}

func fingerprint(code string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(code)))
	return hex.EncodeToString(sum[:4])
}

func redact(message string, secrets ...string) string {
	for _, secret := range secrets {
		if len(secret) < 8 {
			continue
		}
		message = strings.ReplaceAll(message, secret, "[redacted]")
	}
	return message
}

func resolveURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return "", ErrDisabled
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !loopbackHost(u.Hostname()) {
			return "", ErrDisabled
		}
	default:
		return "", ErrDisabled
	}
	if port := u.Port(); port != "" {
		n, convErr := strconv.Atoi(port)
		if convErr != nil || n < 1 || n > 65535 {
			return "", ErrDisabled
		}
	}
	path := strings.TrimRight(u.Path, "/")
	switch {
	case path == "":
		u.Path = uploadPath
	case path == uploadPath || strings.HasSuffix(path, uploadPath):
		u.Path = path
	default:
		u.Path = path + uploadPath
	}
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
