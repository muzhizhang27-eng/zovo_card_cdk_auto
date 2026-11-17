package cardplatform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func captureTimingLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var captured bytes.Buffer
	writer, flags := log.Writer(), log.Flags()
	log.SetOutput(&captured)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(writer); log.SetFlags(flags) })
	return &captured
}

func timingRecords(t *testing.T, captured *bytes.Buffer, kind string) []map[string]any {
	t.Helper()
	var records []map[string]any
	for _, line := range strings.Split(captured.String(), "\n") {
		start := strings.Index(line, "[cdk-timing] ")
		if start < 0 {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line[start+len("[cdk-timing] "):]), &record); err != nil {
			t.Fatal("invalid timing JSON")
		}
		if record["kind"] == kind {
			records = append(records, record)
		}
	}
	return records
}

func TestPublicTimingSeparatesServerWaitBodyAndConnectionReuse(t *testing.T) {
	logs := captureTimingLogs(t)
	const response = `{"code":0,"data":{"preflight_token":"synthetic-secret-response"}}`
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if r.Header.Get("X-Redemption-Device") != "synthetic-device" || r.Header.Get("X-API-Key") != "" {
			t.Error("request identity or authentication changed")
		}
		time.Sleep(25 * time.Millisecond)
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		time.Sleep(15 * time.Millisecond)
		_, _ = w.Write([]byte(response))
	}))
	defer srv.Close()
	client := New(Config{SiteBase: srv.URL, APIKey: "synthetic-owner-secret"})
	client.client = srv.Client()
	client.client.Timeout = defaultHTTPTimeout
	client.limiter = &publicLimiter{interval: time.Millisecond}
	ctx, handler := StartPublicHandlerTiming(context.Background(), "/preflight")
	handler.Decoded()
	handler.CallStarted()
	for i := 0; i < 2; i++ {
		status, raw, err := client.Preflight(ctx, map[string]any{"code": "SXC-SYNTHETIC-PRIVATE", "credential": map[string]string{"session": "SYNTHETIC-SESSION-MUST-NOT-LOG"}}, "synthetic-device")
		if err != nil || status != 200 || string(raw) != response {
			t.Fatal("instrumentation changed the public response")
		}
	}
	handler.CallFinished()
	handler.Finish(200)
	attempts := timingRecords(t, logs, "attempt")
	if len(attempts) != 2 {
		t.Fatal("expected one record per outgoing request")
	}
	for _, record := range attempts {
		if record["trace_id"] != handler.ID || record["operation"] != "preflight" || record["error_class"] != "none" {
			t.Fatal("trace correlation changed")
		}
		if record["upstream_wait_ms"].(float64) < 20 || record["response_body_ms"].(float64) < 10 {
			t.Fatal("slow response stages were not separated")
		}
		sum := 0.0
		for _, field := range []string{"queue_ms", "connection_ms", "request_write_ms", "upstream_wait_ms", "response_headers_ms", "response_body_ms"} {
			sum += record[field].(float64)
		}
		if sum > record["total_ms"].(float64)+2 {
			t.Fatal("top-level timing phases overlap")
		}
	}
	if attempts[0]["connection_reused"] != false || attempts[1]["connection_reused"] != true {
		t.Fatal("cold and reused connections were not distinguished")
	}
	if attempts[0]["tls_ms"].(float64) <= 0 || attempts[1]["tls_ms"].(float64) != 0 {
		t.Fatal("TLS handshake timing is incorrect")
	}
	for _, secret := range []string{"SXC-SYNTHETIC-PRIVATE", "SYNTHETIC-SESSION-MUST-NOT-LOG", "synthetic-owner-secret", "synthetic-device", "synthetic-secret-response", srv.URL} {
		if strings.Contains(logs.String(), secret) {
			t.Fatal("sensitive input or output was logged")
		}
	}
	if client.client.Timeout != defaultHTTPTimeout {
		t.Fatal("instrumentation changed timeout policy")
	}
	if len(timingRecords(t, logs, "handler")) != 1 || len(timingRecords(t, logs, "call")) != 2 {
		t.Fatal("missing handler or call summary")
	}
}

func TestPublicTimingMeasuresRateLimitQueueWithoutAddingRetries(t *testing.T) {
	logs := captureTimingLogs(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); _, _ = w.Write([]byte(`{"code":0}`)) }))
	defer srv.Close()
	client := New(Config{SiteBase: srv.URL})
	client.limiter = &publicLimiter{interval: 40 * time.Millisecond}
	for i := 0; i < 2; i++ {
		if _, _, err := client.Preview(context.Background(), "synthetic", "device"); err != nil {
			t.Fatal(err)
		}
	}
	records := timingRecords(t, logs, "attempt")
	if calls.Load() != 2 || len(records) != 2 || records[1]["queue_ms"].(float64) < 25 {
		t.Fatal("limiter queue timing is missing or calls were retried")
	}
}

func TestPublicTimingTimeoutRecordsWaitingStageAndLeavesPolicyUnchanged(t *testing.T) {
	logs := captureTimingLogs(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer srv.Close()
	client := New(Config{SiteBase: srv.URL})
	client.client.Timeout = 25 * time.Millisecond
	client.limiter = &publicLimiter{interval: time.Millisecond}
	_, _, err := client.Preflight(context.Background(), nil, "device")
	if err == nil || calls.Load() != 1 {
		t.Fatal("instrumentation changed timeout or retry behavior")
	}
	records := timingRecords(t, logs, "attempt")
	if len(records) != 1 || records[0]["error_class"] != "timeout" || records[0]["end_stage"] != "response_wait" || records[0]["upstream_wait_ms"].(float64) < 15 {
		t.Fatal("timeout phase was not recorded")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = client.Preview(ctx, "synthetic", "device")
	if !errors.Is(err, context.Canceled) || calls.Load() != 1 {
		t.Fatal("cancelled request semantics changed")
	}
	records = timingRecords(t, logs, "attempt")
	if records[len(records)-1]["end_stage"] != "queue" || records[len(records)-1]["error_class"] != "cancelled" {
		t.Fatal("cancelled queue was not classified")
	}
}

func TestPublicTimingKeepsExisting429RetriesAndExcludesPayments(t *testing.T) {
	logs := captureTimingLogs(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
			return
		}
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	defer srv.Close()
	client := New(Config{SiteBase: srv.URL})
	client.limiter = &publicLimiter{interval: time.Millisecond}
	if status, _, err := client.Preflight(context.Background(), nil, "device"); err != nil || status != 200 {
		t.Fatal("existing rate-limit retry changed")
	}
	attempts := timingRecords(t, logs, "attempt")
	callsSummary := timingRecords(t, logs, "call")
	if len(attempts) != 2 || len(callsSummary) != 1 || callsSummary[0]["attempts"] != float64(2) || attempts[0]["trace_id"] != attempts[1]["trace_id"] {
		t.Fatal("retry attempts lost correlation")
	}
	logs.Reset()
	_, _, _ = client.Redeem(context.Background(), map[string]string{"client_request_id": "synthetic"}, "device")
	if logs.Len() != 0 {
		t.Fatal("timing instrumentation affected a payment endpoint")
	}
}

func TestPublicTimingDoesNotRetryNetworkOrServerFailures(t *testing.T) {
	logs := captureTimingLogs(t)
	for _, preflight := range []bool{false, true} {
		for _, networkFailure := range []bool{false, true} {
			calls := 0
			original := errors.New("NEVER_LOG_PRIVATE_UPSTREAM_URL_OR_SESSION")
			client := New(Config{SiteBase: "https://synthetic.invalid"})
			client.limiter = &publicLimiter{interval: time.Millisecond}
			client.client.Transport = publicRoundTripper(func(*http.Request) (*http.Response, error) {
				calls++
				if networkFailure {
					return nil, original
				}
				return &http.Response{StatusCode: 503, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"temporary"}`))}, nil
			})
			var status int
			var err error
			if preflight {
				status, _, err = client.Preflight(context.Background(), nil, "device")
			} else {
				status, _, err = client.Preview(context.Background(), "synthetic", "device")
			}
			if calls != 1 || client.client.Timeout != defaultHTTPTimeout {
				t.Fatal("existing retry or timeout policy changed")
			}
			if networkFailure && !errors.Is(err, original) || !networkFailure && (err != nil || status != 503) {
				t.Fatal("error response semantics changed")
			}
		}
	}
	if strings.Contains(logs.String(), "NEVER_LOG_PRIVATE") {
		t.Fatal("raw error leaked into timing logs")
	}
}
