package cardplatform

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPublic429RetriesSameBodyAndDevice(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		if r.Header.Get("X-API-Key") != "" || r.Header.Get("X-Redemption-Device") != "device-1" {
			t.Error("incorrect public authentication or device")
		}
		if len(bodies) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"error":"rate limited"}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"status":"awaiting_card"}}`))
	}))
	defer srv.Close()
	cli := New(Config{SiteBase: srv.URL, APIKey: "never-send-this"})
	cli.limiter = &publicLimiter{interval: time.Millisecond}
	status, _, err := cli.Redeem(context.Background(), map[string]string{
		"client_request_id": "original-request", "redemption_token": "synthetic", "preflight_token": "synthetic",
	}, "device-1")
	if err != nil || status != 200 || len(bodies) != 2 || bodies[0] != bodies[1] {
		t.Fatalf("request was not retried unchanged: status=%d bodies=%v err=%v", status, bodies, err)
	}
}

func TestPublicQuotaSharedAcrossClientsAndResultTokens(t *testing.T) {
	old := sharedPublicLimiter
	sharedPublicLimiter = &publicLimiter{interval: 20 * time.Millisecond}
	defer func() { sharedPublicLimiter = old }()
	var mu sync.Mutex
	var times []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		times = append(times, time.Now())
		mu.Unlock()
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	defer srv.Close()
	var wg sync.WaitGroup
	for _, token := range []string{"one", "two", "three", "four"} {
		wg.Add(1)
		go func(token string) {
			defer wg.Done()
			cli := New(Config{SiteBase: srv.URL})
			if _, _, err := cli.Result(context.Background(), token, "device"); err != nil {
				t.Error(err)
			}
		}(token)
	}
	wg.Wait()
	if len(times) != 4 {
		t.Fatalf("got %d calls", len(times))
	}
	if elapsed := times[3].Sub(times[0]); elapsed < 55*time.Millisecond {
		t.Fatalf("different clients/tokens bypassed the shared quota: %v", elapsed)
	}
}

func TestPublicRetryAfterIsCancellable(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(429)
	}))
	defer srv.Close()
	cli := New(Config{SiteBase: srv.URL})
	cli.limiter = &publicLimiter{interval: time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_, _, err := cli.Preview(ctx, "synthetic", "device")
	if !errors.Is(err, context.DeadlineExceeded) || calls.Load() != 1 {
		t.Fatalf("backoff did not stop after cancellation: calls=%d err=%v", calls.Load(), err)
	}
}

type publicRoundTripper func(*http.Request) (*http.Response, error)

func (fn publicRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func TestPublicDoesNotRetryUnknownPaymentResult(t *testing.T) {
	for _, networkError := range []bool{true, false} {
		calls := 0
		cli := New(Config{SiteBase: "https://synthetic.invalid"})
		cli.limiter = &publicLimiter{interval: time.Millisecond}
		cli.client.Transport = publicRoundTripper(func(*http.Request) (*http.Response, error) {
			calls++
			if networkError {
				return nil, errors.New("response lost after acceptance")
			}
			return &http.Response{StatusCode: 502, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"upstream unavailable"}`))}, nil
		})
		_, _, _ = cli.Redeem(context.Background(), map[string]string{"client_request_id": "same-id"}, "device")
		if calls != 1 {
			t.Fatalf("unknown outcome retried %d times", calls)
		}
	}
}

func TestPublic429RetryBoundAndRecoveryExclusion(t *testing.T) {
	for _, recovery := range []bool{false, true} {
		calls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
		}))
		cli := New(Config{SiteBase: srv.URL})
		cli.limiter = &publicLimiter{interval: time.Millisecond}
		var status int
		if recovery {
			status, _, _ = cli.RecoverSubscription(context.Background(), map[string]bool{"confirmed": true}, "device")
		} else {
			status, _, _ = cli.Preview(context.Background(), "synthetic", "device")
		}
		want := 3
		if recovery {
			want = 1
		}
		if status != 429 || calls != want {
			t.Errorf("recovery=%v status=%d calls=%d want=%d", recovery, status, calls, want)
		}
		srv.Close()
	}
}

func TestRetryAfterSecondsAndHTTPDate(t *testing.T) {
	if got := retryAfter("3", 0); got != 3*time.Second {
		t.Fatal(got)
	}
	future := time.Now().Add(5 * time.Second).UTC().Format(http.TimeFormat)
	if got := retryAfter(future, 0); got < 3*time.Second || got > 5*time.Second {
		t.Fatal(got)
	}
	if got := retryAfter("invalid", 1); got != 4*time.Second {
		t.Fatal(got)
	}
}
