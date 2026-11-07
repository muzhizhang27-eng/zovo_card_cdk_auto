package handler

import (
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// Billing fixtures use only synthetic credentials and isolated local services.
func newBillingTestDB(t *testing.T) *sql.DB {
	t.Helper()
	conn := newCardPlatformSecurityDB(t)
	if _, err := conn.Exec(`CREATE TABLE cdk_session_bindings (
		cdk_code TEXT PRIMARY KEY, session_payload TEXT NOT NULL,
		redemption_token TEXT, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"CARD_API_KEY", "CARD_API_BASE", "CARD_API_PROXY_URL", "ACCOUNTHUB_BASE_URL", "CDK_LOOKUP_SOCKET"} {
		t.Setenv(key, "")
	}
	t.Setenv("SERVER_MODE", "debug")
	return conn
}

type billingTestResponse struct {
	Summary    map[string]any   `json:"summary"`
	Invoices   []map[string]any `json:"invoices"`
	InvoiceURL string           `json:"invoice_url"`
	AuthSource string           `json:"auth_source"`
	Error      string           `json:"error"`
}

func requestBilling(t *testing.T, body map[string]string) (*httptest.ResponseRecorder, billingTestResponse) {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.POST("/billing", SessionBillingCheck)
	request := httptest.NewRequest(http.MethodPost, "/billing", strings.NewReader(string(encoded)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	var payload billingTestResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid billing response: %v", err)
	}
	return response, payload
}

func TestSessionBillingCheckUsesCardPlatform(t *testing.T) {
	for _, tc := range []struct {
		name         string
		upstreamBody string
		wantStatus   int
		wantInvoices int
		wantError    string
	}{
		{
			name:         "subscription and invoices",
			upstreamBody: `{"code":0,"data":{"summary":{"email":"fixture@example.test","will_renew":false},"invoices":[{"hosted_invoice_url":"https://example.test/invoice"}]}}`,
			wantStatus:   http.StatusOK, wantInvoices: 1,
		},
		{
			name:         "null invoices become an empty array",
			upstreamBody: `{"code":0,"data":{"summary":{"email":"fixture@example.test"},"invoices":null}}`,
			wantStatus:   http.StatusOK,
		},
		{
			name:         "business error keeps the user message",
			upstreamBody: `{"code":400,"msg":"token 可能已失效"}`,
			wantStatus:   http.StatusBadGateway, wantError: "token 可能已失效",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newBillingTestDB(t)
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodPost || r.URL.Path != "/openapi/v1/gpt-direct/subscription-check" {
					t.Error("billing used an unexpected provider operation")
				}
				if r.Header.Get("X-API-Key") != "synthetic-billing-key" {
					t.Error("billing lost provider authentication")
				}
				var body map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["token_input"] != "synthetic-billing-token" {
					t.Error("billing changed the supplied credential")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.upstreamBody))
			}))
			t.Cleanup(upstream.Close)
			setSecurityFixtureSetting(t, "card_api_base", upstream.URL)
			setSecurityFixtureSetting(t, "card_api_key", "synthetic-billing-key")
			response, payload := requestBilling(t, map[string]string{"token_input": "synthetic-billing-token"})
			if response.Code != tc.wantStatus || calls.Load() != 1 || payload.AuthSource != "cardplatform" {
				t.Fatalf("billing status=%d calls=%d source=%q", response.Code, calls.Load(), payload.AuthSource)
			}
			if tc.wantError != "" {
				if payload.Error != tc.wantError {
					t.Fatalf("business message=%q", payload.Error)
				}
				return
			}
			if payload.Summary["email"] != "fixture@example.test" || payload.Invoices == nil || len(payload.Invoices) != tc.wantInvoices {
				t.Fatalf("unexpected subscription or invoice result: %+v", payload)
			}
		})
	}
}

func TestSessionBillingCheckPrefersConfiguredAccounthub(t *testing.T) {
	for _, accAvailable := range []bool{true, false} {
		name := "ACC succeeds"
		if !accAvailable {
			name = "ACC failure falls back to card platform"
		}
		t.Run(name, func(t *testing.T) {
			newBillingTestDB(t)
			var accCalls, cardCalls atomic.Int32
			acc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				accCalls.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != "/gpt/invoices-by-email" || r.URL.Query().Get("email") != "fixture+test@example.test" {
					t.Error("ACC query did not use the session's exact email")
				}
				w.Header().Set("Content-Type", "application/json")
				if !accAvailable {
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = w.Write([]byte(`{"error":"synthetic ACC failure"}`))
					return
				}
				_, _ = w.Write([]byte(`{"invoice_url":"https://example.test/acc-invoice"}`))
			}))
			t.Cleanup(acc.Close)
			t.Setenv("ACCOUNTHUB_BASE_URL", acc.URL)
			card := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				cardCalls.Add(1)
				if r.Method != http.MethodPost || r.URL.Path != "/openapi/v1/gpt-direct/subscription-check" {
					t.Error("ACC fallback used an unexpected provider operation")
				}
				_, _ = w.Write([]byte(`{"code":0,"data":{"summary":{"email":"fixture+test@example.test"},"invoices":[]}}`))
			}))
			t.Cleanup(card.Close)
			setSecurityFixtureSetting(t, "card_api_base", card.URL)
			setSecurityFixtureSetting(t, "card_api_key", "synthetic-billing-key")
			response, payload := requestBilling(t, map[string]string{"session": `{"user":{"email":"fixture+test@example.test"},"sessionToken":"synthetic-session"}`})
			if response.Code != http.StatusOK || accCalls.Load() != 1 {
				t.Fatalf("ACC billing status=%d calls=%d", response.Code, accCalls.Load())
			}
			if accAvailable {
				if cardCalls.Load() != 0 || payload.AuthSource != "accounthub" || payload.InvoiceURL != "https://example.test/acc-invoice" || len(payload.Invoices) != 1 {
					t.Fatal("successful ACC result was lost or unnecessarily queried the card platform")
				}
				return
			}
			if cardCalls.Load() != 1 || payload.AuthSource != "cardplatform" {
				t.Fatal("ACC failure did not use the card platform fallback")
			}
		})
	}
}

func TestSessionBillingCheckLoadsBoundSession(t *testing.T) {
	newBillingTestDB(t)
	const code = "ZC-SYNTHETIC-BILLING-CODE"
	const session = `{"sessionToken":"synthetic-bound-session","user":{"email":"bound@example.test"}}`
	if err := db.BindCDKSession(code, "synthetic-redemption", session); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["token_input"] != session {
			t.Error("CDK billing did not use the stored session")
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"summary":{"email":"bound@example.test"},"invoices":[]}}`))
	}))
	t.Cleanup(upstream.Close)
	setSecurityFixtureSetting(t, "card_api_base", upstream.URL)
	setSecurityFixtureSetting(t, "card_api_key", "synthetic-billing-key")
	for _, field := range []string{"cdk_code", "code"} {
		response, payload := requestBilling(t, map[string]string{field: strings.ToLower(code)})
		if response.Code != http.StatusOK || payload.Summary["email"] != "bound@example.test" {
			t.Fatalf("bound session query via %s failed", field)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("stored session calls=%d", calls.Load())
	}
}

func TestSessionBillingCheckUsesBrokerWithoutOwnerKey(t *testing.T) {
	newBillingTestDB(t)
	directory, err := os.MkdirTemp("", "billing-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	socket := filepath.Join(directory, "billing.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	broker := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/billing-check" || r.Header.Get("X-API-Key") != "" {
			t.Error("public billing used an unexpected broker operation or supplied an owner key")
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["token_input"] != "synthetic-public-session" {
			t.Error("public billing did not forward the holder's session")
		}
		_, _ = w.Write([]byte(`{"summary":{"email":"fixture@example.test","will_renew":false},"invoices":[]}`))
	})}
	go broker.Serve(listener)
	t.Cleanup(func() { _ = broker.Close() })
	t.Setenv("CDK_LOOKUP_SOCKET", socket)
	t.Setenv("CARD_API_PROXY_URL", "http://127.0.0.1:1")
	response, payload := requestBilling(t, map[string]string{"token_input": "synthetic-public-session"})
	if response.Code != http.StatusOK || payload.AuthSource != "cardplatform" || payload.Summary["email"] != "fixture@example.test" || calls.Load() != 1 {
		t.Fatalf("keyless public billing status=%d calls=%d", response.Code, calls.Load())
	}
	if strings.Contains(response.Body.String(), "synthetic-public-session") {
		t.Fatal("billing exposed the session credential")
	}
}

func TestSessionBillingCheckBrokerFailureDoesNotFallBack(t *testing.T) {
	newBillingTestDB(t)
	var providerCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		_, _ = w.Write([]byte(`{"code":0,"data":{"summary":{},"invoices":[]}}`))
	}))
	t.Cleanup(upstream.Close)
	setSecurityFixtureSetting(t, "card_api_base", upstream.URL)
	setSecurityFixtureSetting(t, "card_api_key", "synthetic-billing-key")
	t.Setenv("CDK_LOOKUP_SOCKET", filepath.Join(t.TempDir(), "missing.sock"))
	response, payload := requestBilling(t, map[string]string{"token_input": "synthetic-public-session"})
	if response.Code != http.StatusBadGateway || providerCalls.Load() != 0 || payload.Error == "" {
		t.Fatalf("broker failure status=%d provider calls=%d", response.Code, providerCalls.Load())
	}
	if strings.Contains(response.Body.String(), "missing.sock") || strings.Contains(response.Body.String(), "synthetic-billing-key") {
		t.Fatal("broker failure exposed private configuration")
	}
}

func TestSessionBillingCheckRejectsMissingInputAndUnboundCode(t *testing.T) {
	newBillingTestDB(t)
	for _, tc := range []struct {
		body   map[string]string
		status int
	}{
		{map[string]string{}, http.StatusBadRequest},
		{map[string]string{"token_input": "  ", "session": " "}, http.StatusBadRequest},
		{map[string]string{"cdk_code": "ZC-SYNTHETIC-UNBOUND-CODE"}, http.StatusNotFound},
	} {
		response, payload := requestBilling(t, tc.body)
		if response.Code != tc.status || payload.Error == "" {
			t.Fatalf("invalid input status=%d want=%d", response.Code, tc.status)
		}
	}
}
