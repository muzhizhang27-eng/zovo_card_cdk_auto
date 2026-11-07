package cdklookup

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/cardplatform"
)

type billingFixtureUpstream struct {
	unavailableUpstream
	check func(context.Context, string) (*cardplatform.SubscriptionCheckResult, error)
}

func (upstream billingFixtureUpstream) CheckSubscription(ctx context.Context, input string) (*cardplatform.SubscriptionCheckResult, error) {
	return upstream.check(ctx, input)
}

func TestBillingBrokerResultsAndSafeErrors(t *testing.T) {
	for _, test := range []struct {
		name       string
		result     *cardplatform.SubscriptionCheckResult
		err        error
		wantStatus int
		wantError  string
	}{
		{
			name:       "success",
			result:     &cardplatform.SubscriptionCheckResult{Summary: map[string]any{"email": "fixture@example.test", "will_renew": false}},
			wantStatus: 200,
		},
		{
			name:       "business_error",
			err:        &cardplatform.APIError{HTTPStatus: 400, Code: 400, Msg: "synthetic token expired"},
			wantStatus: 502, wantError: "synthetic token expired",
		},
		{
			name:       "missing_owner_key",
			err:        &cardplatform.APIError{HTTPStatus: 401, Msg: "card_api_key not configured"},
			wantStatus: 503, wantError: ErrBillingUnavailable.Error(),
		},
		{
			name:       "owner_key_rejected",
			err:        &cardplatform.APIError{HTTPStatus: 401, Code: 401, Msg: "synthetic-owner-key rejected"},
			wantStatus: 503, wantError: ErrBillingUnavailable.Error(),
		},
		{
			name:       "owner_key_forbidden",
			err:        &cardplatform.APIError{HTTPStatus: 403, Code: 403, Msg: "synthetic-owner-key forbidden"},
			wantStatus: 503, wantError: ErrBillingUnavailable.Error(),
		},
		{
			name:       "owner_key_forbidden_business_code",
			err:        &cardplatform.APIError{HTTPStatus: 400, Code: 403, Msg: "synthetic-owner-key forbidden"},
			wantStatus: 503, wantError: ErrBillingUnavailable.Error(),
		},
		{
			name:       "transport_error",
			err:        errors.New("synthetic-private-session and synthetic-owner-key"),
			wantStatus: 503, wantError: ErrBillingUnavailable.Error(),
		},
		{name: "nil_result", wantStatus: 503},
		{
			name:       "oversized_response",
			result:     &cardplatform.SubscriptionCheckResult{Summary: map[string]any{"large": strings.Repeat("x", billingResponseLimit)}},
			wantStatus: 503,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			upstream := billingFixtureUpstream{check: func(ctx context.Context, input string) (*cardplatform.SubscriptionCheckResult, error) {
				calls++
				if input != "synthetic-private-session" {
					t.Error("broker altered the credential")
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Error("billing upstream has no deadline")
				}
				return test.result, test.err
			}}
			response := httptest.NewRecorder()
			Handler(upstream).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/billing-check", strings.NewReader(`{"token_input":"synthetic-private-session"}`)))
			if response.Code != test.wantStatus || calls != 1 {
				t.Fatalf("status=%d want=%d calls=%d", response.Code, test.wantStatus, calls)
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("billing response can be cached")
			}
			if strings.Contains(response.Body.String(), "synthetic-private-session") || strings.Contains(response.Body.String(), "synthetic-owner-key") ||
				strings.Contains(response.Body.String(), "card_api_key") {
				t.Fatal("private error detail escaped")
			}
			if test.wantStatus == http.StatusOK {
				var result map[string]any
				if json.Unmarshal(response.Body.Bytes(), &result) != nil {
					t.Fatal("invalid success response")
				}
				if _, ok := result["invoices"].([]any); !ok {
					t.Fatal("empty invoices must be an array")
				}
			}
			if test.wantError != "" {
				var result struct {
					Error string `json:"error"`
				}
				if json.Unmarshal(response.Body.Bytes(), &result) != nil || result.Error != test.wantError {
					t.Fatalf("error response=%s", response.Body.String())
				}
			}
		})
	}
}

func TestBillingBrokerRestrictedProtocol(t *testing.T) {
	upstream := billingFixtureUpstream{check: func(context.Context, string) (*cardplatform.SubscriptionCheckResult, error) {
		t.Fatal("invalid protocol reached upstream")
		return nil, nil
	}}
	for _, test := range []struct {
		method, path, body string
		want               int
	}{
		{http.MethodGet, "/billing-check", "", 404},
		{http.MethodPost, "/gpt-direct/orders", `{}`, 404},
		{http.MethodPost, "/billing-check", `{"token_input":"fixture","path":"/wallet"}`, 400},
		{http.MethodPost, "/billing-check", `{"token_input":"fixture","api_key":"synthetic-key"}`, 400},
		{http.MethodPost, "/billing-check", `{"token_input":"fixture"} {}`, 400},
		{http.MethodPost, "/billing-check", `{"token_input":""}`, 400},
		{http.MethodPost, "/billing-check", `null`, 400},
		{http.MethodPost, "/billing-check", `{"token_input":42}`, 400},
		{http.MethodPost, "/billing-check", `{"token_input":"` + strings.Repeat("x", billingRequestLimit) + `"}`, 413},
	} {
		response := httptest.NewRecorder()
		Handler(upstream).ServeHTTP(response, httptest.NewRequest(test.method, test.path, strings.NewReader(test.body)))
		if response.Code != test.want {
			t.Fatalf("method=%s path=%s status=%d want=%d", test.method, test.path, response.Code, test.want)
		}
	}
}

func billingSocket(t *testing.T, handler http.Handler) string {
	t.Helper()
	directory, err := os.MkdirTemp("", "cdk-billing-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	path := filepath.Join(directory, "broker.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	go server.Serve(listener)
	t.Cleanup(func() { _ = server.Close() })
	return path
}

func TestBillingSocketClientAndBusinessErrors(t *testing.T) {
	t.Setenv("CARD_API_PROXY_URL", "http://127.0.0.1:1")
	t.Setenv("GPTCHECK_PROXY_URL", "http://127.0.0.1:1")
	for _, businessError := range []bool{false, true} {
		upstream := billingFixtureUpstream{check: func(context.Context, string) (*cardplatform.SubscriptionCheckResult, error) {
			if businessError {
				return nil, &cardplatform.APIError{Code: 400, Msg: "synthetic token expired"}
			}
			return &cardplatform.SubscriptionCheckResult{Summary: map[string]any{"email": "fixture@example.test"}}, nil
		}}
		path := billingSocket(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-API-Key") != "" {
				t.Error("public client sent an owner key")
			}
			Handler(upstream).ServeHTTP(w, r)
		}))
		result, err := CheckSubscription(context.Background(), path, "synthetic-session")
		if businessError {
			var apiErr *cardplatform.APIError
			if !errors.As(err, &apiErr) || apiErr.Msg != "synthetic token expired" || apiErr.HTTPStatus != 502 {
				t.Fatalf("business error=%v", err)
			}
		} else if err != nil || result.Summary["email"] != "fixture@example.test" {
			t.Fatalf("billing result=%v error=%v", result, err)
		}
		if _, err := CheckSubscription(context.Background(), path+".missing", "synthetic-session"); !errors.Is(err, ErrBillingUnavailable) {
			t.Fatalf("missing socket error=%v", err)
		}
	}
}

func TestBillingSocketClientRejectsRedirectsAndOversizedResponses(t *testing.T) {
	var externalCalls atomic.Int32
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { externalCalls.Add(1) }))
	defer external.Close()
	for _, handler := range []http.Handler{
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, external.URL, http.StatusTemporaryRedirect)
		}),
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(strings.Repeat("x", billingResponseLimit+1)))
		}),
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`null`)) }),
	} {
		path := billingSocket(t, handler)
		if _, err := CheckSubscription(context.Background(), path, "synthetic-session"); !errors.Is(err, ErrBillingUnavailable) {
			t.Fatalf("unsafe response error=%v", err)
		}
	}
	if _, err := CheckSubscription(context.Background(), external.URL, "synthetic-session"); !errors.Is(err, ErrBillingUnavailable) {
		t.Fatalf("HTTP URL accepted as socket: %v", err)
	}
	if externalCalls.Load() != 0 {
		t.Fatal("Unix billing query used an HTTP fallback or followed a redirect")
	}
}

func TestBillingBrokerDoesNotFollowAuthenticatedProviderRedirects(t *testing.T) {
	t.Setenv("SERVER_MODE", "debug")
	t.Setenv("CARD_API_PROXY_URL", "")
	var requests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	client := cardplatform.NewReadOnly(cardplatform.Config{SiteBase: source.URL, APIKey: "synthetic-owner-key"})
	response := httptest.NewRecorder()
	Handler(client).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/billing-check", strings.NewReader(`{"token_input":"synthetic-session"}`)))
	if response.Code != http.StatusServiceUnavailable || requests.Load() != 0 {
		t.Fatal("billing followed an authenticated provider redirect")
	}
}

type billingConcurrencyUpstream struct {
	billingFixtureUpstream
	lookupCalls atomic.Int32
}

func (upstream *billingConcurrencyUpstream) ListCDKsQuery(context.Context, cardplatform.CDKListQuery) (*cardplatform.CDKListResult, error) {
	upstream.lookupCalls.Add(1)
	return nil, ErrUnavailable
}

func TestBillingAndLookupShareWorkerLimit(t *testing.T) {
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	finished := make(chan struct{}, 4)
	defer func() {
		close(release)
		for i := 0; i < 4; i++ {
			<-finished
		}
	}()
	upstream := &billingConcurrencyUpstream{billingFixtureUpstream: billingFixtureUpstream{
		check: func(context.Context, string) (*cardplatform.SubscriptionCheckResult, error) {
			started <- struct{}{}
			<-release
			return &cardplatform.SubscriptionCheckResult{}, nil
		},
	}}
	handler := Handler(upstream)
	for i := 0; i < 4; i++ {
		go func() {
			defer func() { finished <- struct{}{} }()
			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/billing-check", strings.NewReader(`{"token_input":"synthetic-session"}`)))
		}()
	}
	for i := 0; i < 4; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("billing did not occupy the four shared worker slots")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	request := httptest.NewRequest(http.MethodPost, "/lookup", strings.NewReader(`{"code":"`+fixtureCode+`"}`)).WithContext(ctx)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || upstream.lookupCalls.Load() != 0 {
		t.Fatal("lookup bypassed the shared billing worker limit")
	}
}
