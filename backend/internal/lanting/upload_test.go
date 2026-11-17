package lanting

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testSecret  = "synthetic-hmac-secret-0123456789abcd"
	testCode    = "SXC-SYNTHETIC-0001"
	testSession = `{"sessionToken":"synthetic-session-token-value-0001","note":"a & b <tag>"}`
)

func TestSignMatchesTimestampDotRawBody(t *testing.T) {
	raw := []byte(`{"cdk_code":"SXC-SYNTHETIC-0001","credential":{"mode":"session","session":"synthetic-session-token-value-0001"}}`)
	const timestamp = "1700000000000"
	mac := hmac.New(sha256.New, []byte(testSecret))
	mac.Write([]byte(timestamp + "."))
	mac.Write(raw)
	if got := sign(testSecret, timestamp, raw); got != hex.EncodeToString(mac.Sum(nil)) {
		t.Fatalf("signature = %s", got)
	}
	if sign(testSecret, timestamp, raw) == sign("other-synthetic-hmac-secret-0123456789", timestamp, raw) {
		t.Fatal("signature ignored the secret")
	}
}

func TestResolveURLAppendsPathAndRejectsRemoteHTTP(t *testing.T) {
	got, err := resolveURL("http://127.0.0.1:4313")
	if err != nil || got != "http://127.0.0.1:4313"+uploadPath {
		t.Fatalf("loopback origin = %s %v", got, err)
	}
	got, err = resolveURL("http://127.0.0.1:4313" + uploadPath)
	if err != nil || got != "http://127.0.0.1:4313"+uploadPath {
		t.Fatalf("full endpoint = %s %v", got, err)
	}
	if _, err := resolveURL("http://lanting.example" + uploadPath); err == nil {
		t.Fatal("plaintext remote url was accepted")
	}
	if _, err := resolveURL("https://user:secret@lanting.example"); err == nil {
		t.Fatal("url with userinfo was accepted")
	}
	if _, err := resolveURL("http://127.0.0.1:4313/upload?session=synthetic-session-token-value-0001"); err == nil {
		t.Fatal("url with query was accepted")
	}
	if _, err := resolveURL("https://lanting.example"); err != nil {
		t.Fatalf("https origin rejected: %v", err)
	}
}

func TestUploadSignsBodyWithoutPuttingTokenInTheURL(t *testing.T) {
	var calls atomic.Int32
	server := newSignedServer(t, testSecret, func(w http.ResponseWriter, r *http.Request, body []byte) {
		calls.Add(1)
		if r.URL.RawQuery != "" || strings.Contains(r.URL.RequestURI(), "synthetic-session") || strings.Contains(r.URL.RequestURI(), testSecret) {
			t.Errorf("token or secret leaked into the url: %s", r.URL.RequestURI())
		}
		if !strings.Contains(string(body), "a & b <tag>") || strings.Contains(string(body), `\u0026`) || strings.Contains(string(body), `\u003c`) {
			t.Errorf("session was escaped or dropped: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"accepted":true,"session":"`+testSession+`"}`)
	})
	var logs []string
	client := Client{
		BaseURL:  server.URL,
		Secret:   testSecret,
		Attempts: 1,
		Now:      func() time.Time { return time.UnixMilli(1700000000000) },
		Log:      func(line string) { logs = append(logs, line) },
	}
	status, err := client.Upload(context.Background(), testCode, testSession)
	if err != nil || status != http.StatusOK || calls.Load() != 1 {
		t.Fatalf("status=%d err=%v calls=%d", status, err, calls.Load())
	}
	for _, line := range logs {
		if strings.Contains(line, testSession) || strings.Contains(line, "synthetic-session-token-value-0001") || strings.Contains(line, testSecret) {
			t.Fatalf("log leaked a secret: %s", line)
		}
		if !strings.Contains(line, "outcome=accepted") {
			t.Fatalf("log = %s", line)
		}
	}
}

func TestUploadRedactsTransportErrorsAndDoesNotRetryClientErrors(t *testing.T) {
	secretSession := "synthetic-session-token-value-0001"
	var logs []string
	client := Client{
		BaseURL:  "http://127.0.0.1:4313",
		Secret:   testSecret,
		Attempts: 3,
		Backoff:  func(int) time.Duration { return 0 },
		HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return nil, errors.New("dial failed " + secretSession + " " + testSecret)
		}), Timeout: time.Second},
		Log: func(line string) { logs = append(logs, line) },
	}
	_, err := client.Upload(context.Background(), testCode, secretSession)
	if err == nil || strings.Contains(err.Error(), secretSession) || strings.Contains(err.Error(), testSecret) {
		t.Fatalf("returned error leaked or was empty: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("logs = %#v", logs)
	}
	for _, line := range logs {
		if strings.Contains(line, secretSession) || strings.Contains(line, testSecret) {
			t.Fatalf("log leaked a secret: %s", line)
		}
	}

	var calls atomic.Int32
	server := newSignedServer(t, testSecret, func(w http.ResponseWriter, r *http.Request, body []byte) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		http.Error(w, `{"error":{"code":"CDK_NOT_FOUND","request_id":"req","leak":"`+secretSession+`"}}`, http.StatusNotFound)
	})
	logs = nil
	client = Client{BaseURL: server.URL, Secret: testSecret, Attempts: 3, Backoff: func(int) time.Duration { return 0 }, Log: func(line string) { logs = append(logs, line) }}
	status, err := client.Upload(context.Background(), testCode, secretSession)
	if status != http.StatusNotFound || err == nil || calls.Load() != 1 {
		t.Fatalf("status=%d err=%v calls=%d", status, err, calls.Load())
	}
	joined := strings.Join(logs, "\n")
	if strings.Contains(joined, secretSession) || !strings.Contains(joined, "error_code=CDK_NOT_FOUND") {
		t.Fatalf("logs = %s", joined)
	}
}

func TestUploadRetriesBoundedUpstreamFailures(t *testing.T) {
	var calls atomic.Int32
	server := newSignedServer(t, testSecret, func(w http.ResponseWriter, r *http.Request, body []byte) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"accepted":true}`)
	})
	client := Client{BaseURL: server.URL, Secret: testSecret, Attempts: 3, Backoff: func(int) time.Duration { return time.Millisecond }}
	status, err := client.Upload(context.Background(), testCode, "synthetic-session-token-value-0001")
	if err != nil || status != http.StatusOK || calls.Load() != 3 {
		t.Fatalf("status=%d err=%v calls=%d", status, err, calls.Load())
	}
}

func TestScheduleSkipsUnchangedSessionAndIgnoresMissingConfig(t *testing.T) {
	t.Setenv("LANTING_TOKEN_UPLOAD_URL", "")
	t.Setenv("LANTING_CDK_TOKEN_HMAC_SECRET", "")
	if FromEnv().Enabled() {
		t.Fatal("empty config was enabled")
	}
	var calls atomic.Int32
	server := newSignedServer(t, testSecret, func(w http.ResponseWriter, r *http.Request, body []byte) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	t.Setenv("LANTING_TOKEN_UPLOAD_URL", server.URL)
	t.Setenv("LANTING_CDK_TOKEN_HMAC_SECRET", testSecret)
	session := "synthetic-session-token-value-0001"
	Schedule(testCode, "synthetic-redemption-token", session, true)
	Schedule(testCode, "synthetic-redemption-token", session, false)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d", calls.Load())
	}
	Schedule(testCode, "synthetic-redemption-token", session+"-changed", false)
	if err := Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func newSignedServer(t *testing.T, secret string, next func(http.ResponseWriter, *http.Request, []byte)) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		timestamp := r.Header.Get("X-Lanting-Timestamp")
		signature := r.Header.Get("X-Lanting-Signature")
		if len(timestamp) != 13 || signature != sign(secret, timestamp, body) {
			t.Errorf("timestamp=%s signature mismatch", timestamp)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != uploadPath {
			t.Errorf("path = %s", r.URL.Path)
		}
		next(w, r, body)
	}))
	t.Cleanup(server.Close)
	return server
}
