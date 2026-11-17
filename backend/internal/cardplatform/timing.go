package cardplatform

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"math"
	"net"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type publicTimingKey struct{}

var fallbackTimingID atomic.Uint64

func timingOperation(path string) string {
	switch strings.SplitN(path, "?", 2)[0] {
	case "/preview":
		return "preview"
	case "/preflight":
		return "preflight"
	default:
		return ""
	}
}

func timingID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		stamp, sequence := uint64(time.Now().UnixNano()), fallbackTimingID.Add(1)
		for i := 0; i < 8; i++ {
			value[i], value[8+i] = byte(stamp>>(i*8)), byte(sequence>>(i*8))
		}
	}
	return hex.EncodeToString(value[:])
}

func milliseconds(d time.Duration) float64 {
	if d < 0 {
		return 0
	}
	return math.Round(float64(d)/float64(time.Millisecond)*1000) / 1000
}

// Records contain fixed operation names, random trace IDs and timings only.
// Never add URLs, headers, payloads, CDK values or account identities.
func writeTiming(record any) {
	encoded, _ := json.Marshal(record)
	log.Printf("[cdk-timing] %s", encoded)
}

type PublicHandlerTiming struct {
	ID                                          string
	operation                                   string
	started, decoded, callStarted, callFinished time.Time
}

// StartPublicHandlerTiming adds correlation without changing the request body.
func StartPublicHandlerTiming(ctx context.Context, path string) (context.Context, *PublicHandlerTiming) {
	t := &PublicHandlerTiming{ID: timingID(), operation: timingOperation(path), started: time.Now()}
	return context.WithValue(ctx, publicTimingKey{}, t.ID), t
}

func (t *PublicHandlerTiming) Decoded()      { t.decoded = time.Now() }
func (t *PublicHandlerTiming) CallStarted()  { t.callStarted = time.Now() }
func (t *PublicHandlerTiming) CallFinished() { t.callFinished = time.Now() }

func (t *PublicHandlerTiming) Finish(status int) {
	if t.operation == "" {
		return
	}
	end := time.Now()
	decoded := t.decoded
	if decoded.IsZero() {
		decoded = end
	}
	var config, upstream, local time.Duration
	if !t.callStarted.IsZero() {
		config = t.callStarted.Sub(decoded)
	}
	if !t.callFinished.IsZero() {
		upstream, local = t.callFinished.Sub(t.callStarted), end.Sub(t.callFinished)
	}
	writeTiming(struct {
		Kind       string  `json:"kind"`
		TraceID    string  `json:"trace_id"`
		Operation  string  `json:"operation"`
		Status     int     `json:"status"`
		DecodeMS   float64 `json:"decode_ms"`
		ConfigMS   float64 `json:"config_ms"`
		UpstreamMS float64 `json:"upstream_total_ms"`
		LocalMS    float64 `json:"postprocess_ms"`
		TotalMS    float64 `json:"total_ms"`
	}{"handler", t.ID, t.operation, status, milliseconds(decoded.Sub(t.started)), milliseconds(config), milliseconds(upstream), milliseconds(local), milliseconds(end.Sub(t.started))})
}

type publicCallTiming struct {
	id, operation string
	started       time.Time
	attempts      int
}

func startPublicCallTiming(ctx context.Context, path string) *publicCallTiming {
	operation := timingOperation(path)
	if operation == "" {
		return nil
	}
	id, _ := ctx.Value(publicTimingKey{}).(string)
	if id == "" {
		id = timingID()
	}
	return &publicCallTiming{id: id, operation: operation, started: time.Now()}
}

func (t *publicCallTiming) finish(status int, err error) {
	if t == nil {
		return
	}
	writeTiming(struct {
		Kind      string  `json:"kind"`
		TraceID   string  `json:"trace_id"`
		Operation string  `json:"operation"`
		Status    int     `json:"status"`
		Error     string  `json:"error_class"`
		Attempts  int     `json:"attempts"`
		TotalMS   float64 `json:"total_ms"`
	}{"call", t.id, t.operation, status, timingError(err), t.attempts, milliseconds(time.Since(t.started))})
}

type publicAttemptTiming struct {
	mu                                                                                                                    sync.Mutex
	id, operation                                                                                                         string
	attempt                                                                                                               int
	started, requestStarted, dnsStarted, connectStarted, connectFinished, tlsStarted, acquired, wrote, firstByte, headers time.Time
	queue, dns, connect, proxy, tls                                                                                       time.Duration
	reused, proxyConfigured                                                                                               bool
}

func (t *publicCallTiming) attempt() *publicAttemptTiming {
	if t == nil {
		return nil
	}
	t.attempts++
	return &publicAttemptTiming{id: t.id, operation: t.operation, attempt: t.attempts, started: time.Now()}
}

func (t *publicAttemptTiming) queued(start time.Time) {
	if t == nil {
		return
	}
	t.queue = time.Since(start)
}

func (t *publicAttemptTiming) requestContext(ctx context.Context, proxyConfigured bool) context.Context {
	if t == nil {
		return ctx
	}
	t.requestStarted, t.proxyConfigured = time.Now(), proxyConfigured
	return httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		DNSStart: func(httptrace.DNSStartInfo) { t.mu.Lock(); defer t.mu.Unlock(); t.dnsStarted = time.Now() },
		DNSDone: func(httptrace.DNSDoneInfo) {
			t.mu.Lock()
			defer t.mu.Unlock()
			if !t.dnsStarted.IsZero() {
				t.dns += time.Since(t.dnsStarted)
			}
		},
		ConnectStart: func(_, _ string) {
			t.mu.Lock()
			defer t.mu.Unlock()
			if t.connectStarted.IsZero() {
				t.connectStarted = time.Now()
			}
		},
		ConnectDone: func(_, _ string, err error) {
			t.mu.Lock()
			defer t.mu.Unlock()
			if err == nil && !t.connectStarted.IsZero() {
				t.connectFinished = time.Now()
				t.connect = t.connectFinished.Sub(t.connectStarted)
			}
		},
		TLSHandshakeStart: func() {
			t.mu.Lock()
			defer t.mu.Unlock()
			t.tlsStarted = time.Now()
			if t.proxyConfigured && !t.connectFinished.IsZero() {
				t.proxy = t.tlsStarted.Sub(t.connectFinished)
			}
		},
		TLSHandshakeDone: func(_ tls.ConnectionState, _ error) {
			t.mu.Lock()
			defer t.mu.Unlock()
			if !t.tlsStarted.IsZero() {
				t.tls = time.Since(t.tlsStarted)
			}
		},
		GotConn: func(info httptrace.GotConnInfo) {
			t.mu.Lock()
			defer t.mu.Unlock()
			t.acquired = time.Now()
			t.reused = info.Reused
		},
		WroteRequest: func(httptrace.WroteRequestInfo) { t.mu.Lock(); defer t.mu.Unlock(); t.wrote = time.Now() },
		GotFirstResponseByte: func() {
			t.mu.Lock()
			defer t.mu.Unlock()
			if t.firstByte.IsZero() {
				t.firstByte = time.Now()
			}
		},
	})
}

func (t *publicAttemptTiming) responseHeaders() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.headers = time.Now()
}

func (t *publicAttemptTiming) finish(status int, raw []byte, err error) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	end := time.Now()
	var acquire, write, wait, headers, body time.Duration
	if !t.requestStarted.IsZero() {
		acquired := t.acquired
		if acquired.IsZero() {
			acquired = end
		}
		acquire = acquired.Sub(t.requestStarted)
	}
	if !t.acquired.IsZero() {
		wrote := t.wrote
		if wrote.IsZero() {
			wrote = end
		}
		write = wrote.Sub(t.acquired)
	}
	if !t.wrote.IsZero() {
		first := t.firstByte
		if first.IsZero() {
			first = end
		}
		wait = first.Sub(t.wrote)
	}
	if !t.firstByte.IsZero() {
		received := t.headers
		if received.IsZero() {
			received = end
		}
		headers = received.Sub(t.firstByte)
	}
	if !t.headers.IsZero() {
		body = end.Sub(t.headers)
	}
	if t.tls == 0 && !t.tlsStarted.IsZero() {
		t.tls = end.Sub(t.tlsStarted)
	}
	stage := "complete"
	if err != nil {
		switch {
		case t.requestStarted.IsZero():
			stage = "queue"
		case t.acquired.IsZero():
			stage = "connection"
		case t.wrote.IsZero():
			stage = "request_write"
		case t.firstByte.IsZero():
			stage = "response_wait"
		case t.headers.IsZero():
			stage = "response_headers"
		default:
			stage = "response_body"
		}
	}
	var envelope struct {
		Code *int `json:"code"`
	}
	validJSON := json.Valid(raw)
	if validJSON {
		_ = json.Unmarshal(raw, &envelope)
	}
	writeTiming(struct {
		Kind         string  `json:"kind"`
		TraceID      string  `json:"trace_id"`
		Operation    string  `json:"operation"`
		Attempt      int     `json:"attempt"`
		Status       int     `json:"status"`
		Error        string  `json:"error_class"`
		Stage        string  `json:"end_stage"`
		QueueMS      float64 `json:"queue_ms"`
		ConnectionMS float64 `json:"connection_ms"`
		DNSMS        float64 `json:"dns_ms"`
		TCPMS        float64 `json:"tcp_ms"`
		ProxyMS      float64 `json:"proxy_connect_ms"`
		TLSMS        float64 `json:"tls_ms"`
		WriteMS      float64 `json:"request_write_ms"`
		WaitMS       float64 `json:"upstream_wait_ms"`
		HeadersMS    float64 `json:"response_headers_ms"`
		BodyMS       float64 `json:"response_body_ms"`
		TotalMS      float64 `json:"total_ms"`
		Reused       bool    `json:"connection_reused"`
		ResponseJSON bool    `json:"response_json"`
		BusinessCode *int    `json:"business_code"`
	}{"attempt", t.id, t.operation, t.attempt, status, timingError(err), stage, milliseconds(t.queue), milliseconds(acquire), milliseconds(t.dns), milliseconds(t.connect), milliseconds(t.proxy), milliseconds(t.tls), milliseconds(write), milliseconds(wait), milliseconds(headers), milliseconds(body), milliseconds(end.Sub(t.started)), t.reused, validJSON, envelope.Code})
}

func timingError(err error) string {
	if err == nil {
		return "none"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	var network net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &network) && network.Timeout() {
		return "timeout"
	}
	return "network_or_protocol"
}
