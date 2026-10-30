package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// These tests use only an in-memory database and synthetic credentials/inventory.
// Do not run them in parallel: the handlers share db.DB and http.DefaultTransport.
func newCardPlatformSecurityDB(t *testing.T) *sql.DB {
	t.Helper()
	conn, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	previous := db.DB
	db.DB = conn
	t.Cleanup(func() { db.DB = previous; _ = conn.Close() })
	for _, query := range []string{
		`CREATE TABLE site_settings(key TEXT PRIMARY KEY, value TEXT, updated_at DATETIME)`,
		`CREATE TABLE cardplatform_cdk_codes(upstream_id INTEGER, code TEXT UNIQUE NOT NULL, code_prefix TEXT, plan TEXT, fee_amount_minor INTEGER, status TEXT, payment_country TEXT, created_at DATETIME)`,
		`CREATE TABLE admin_audit_logs(id INTEGER PRIMARY KEY, username TEXT, action TEXT, detail TEXT, ip TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
	} {
		if _, err := conn.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	gin.SetMode(gin.TestMode)
	return conn
}

func setSecurityFixtureSetting(t *testing.T, key, value string) {
	t.Helper()
	if err := db.SetSetting(key, value); err != nil {
		t.Fatalf("set fixture setting %s: %v", key, err)
	}
}

type securityRoundTripper func(*http.Request) (*http.Response, error)

func (fn securityRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestCardPlatformPingDoesNotForwardKeyThroughRedirect(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			newCardPlatformSecurityDB(t)
			t.Setenv("SERVER_MODE", "debug")
			var sourceRequests, targetRequests atomic.Int32
			var sourceReceivedKey atomic.Bool
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				targetRequests.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			defer target.Close()
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sourceRequests.Add(1)
				sourceReceivedKey.Store(r.Header.Get("X-API-Key") == "synthetic-ping-key")
				http.Redirect(w, r, target.URL+"/collect", status)
			}))
			defer source.Close()
			setSecurityFixtureSetting(t, "card_api_base", source.URL)
			setSecurityFixtureSetting(t, "card_api_key", "synthetic-ping-key")

			previousTransport := http.DefaultTransport
			http.DefaultTransport = securityRoundTripper(func(req *http.Request) (*http.Response, error) {
				// Both mock hosts are permitted, so following the redirect would
				// reach the target. Only unrelated egress-IP probes are cancelled.
				if "http://"+req.URL.Host == source.URL || "http://"+req.URL.Host == target.URL {
					return previousTransport.RoundTrip(req)
				}
				return nil, context.Canceled
			})
			t.Cleanup(func() { http.DefaultTransport = previousTransport })
			router := gin.New()
			router.GET("/ping", CardPlatformPing)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/ping", nil))
			if response.Code != http.StatusOK {
				t.Fatalf("ping status = %d, want 200", response.Code)
			}
			var body struct {
				Status int `json:"status"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Status != status || sourceRequests.Load() != 1 || !sourceReceivedKey.Load() {
				t.Fatal("ping did not inspect the authenticated source response")
			}
			if targetRequests.Load() != 0 {
				t.Fatal("ping followed an upstream redirect")
			}
			if strings.Contains(response.Body.String(), "synthetic-ping-key") {
				t.Fatal("ping exposed the API key in its response")
			}
		})
	}
}

func TestCardPlatformPingRejectsDisallowedProductionOriginBeforeRequest(t *testing.T) {
	newCardPlatformSecurityDB(t)
	t.Setenv("SERVER_MODE", "release")
	t.Setenv("CARD_API_ALLOWED_ORIGINS", "https://allowed.example")
	setSecurityFixtureSetting(t, "card_api_base", "https://untrusted.example")
	setSecurityFixtureSetting(t, "card_api_key", "synthetic-ping-key")
	var requests atomic.Int32
	previousTransport := http.DefaultTransport
	http.DefaultTransport = securityRoundTripper(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, context.Canceled
	})
	t.Cleanup(func() { http.DefaultTransport = previousTransport })
	router := gin.New()
	router.GET("/ping", CardPlatformPing)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/ping", nil))
	if response.Code != http.StatusBadRequest || requests.Load() != 0 {
		t.Fatalf("disallowed origin: status=%d requests=%d", response.Code, requests.Load())
	}
}

func TestAdminPutSettingsRejectsProductionOriginBeforeAnyWrite(t *testing.T) {
	for _, base := range []string{
		"http://allowed.example",
		"https://untrusted.example",
		"https://user:synthetic-password@allowed.example",
		"https://allowed.example/path",
		"https://allowed.example?redirect=elsewhere",
	} {
		t.Run(base, func(t *testing.T) {
			conn := newCardPlatformSecurityDB(t)
			t.Setenv("SERVER_MODE", "release")
			t.Setenv("CARD_API_ALLOWED_ORIGINS", "https://allowed.example")
			baseline := map[string]string{
				"card_api_base": "https://allowed.example", "card_api_key": "synthetic-old-key",
				"brand_name": "Old Brand", "skin": "zovo", "theme_mode": "dark",
			}
			for key, value := range baseline {
				setSecurityFixtureSetting(t, key, value)
			}
			body, err := json.Marshal(map[string]string{
				"card_api_base": base, "card_api_key": "synthetic-new-key", "brand_name": "New Brand",
				"skin": "ocean", "theme_mode": "light", "webhook_secret": "synthetic-new-webhook",
			})
			if err != nil {
				t.Fatal(err)
			}
			router := gin.New()
			router.PUT("/settings", AdminPutSettings)
			request := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(string(body)))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("settings status = %d, want 400", response.Code)
			}
			for key, want := range baseline {
				if value, err := db.GetSetting(key); err != nil || value != want {
					t.Fatalf("rejected settings request changed %s", key)
				}
			}
			var settingsCount, auditCount int
			if err := conn.QueryRow(`SELECT COUNT(*) FROM site_settings`).Scan(&settingsCount); err != nil {
				t.Fatal(err)
			}
			if err := conn.QueryRow(`SELECT COUNT(*) FROM admin_audit_logs`).Scan(&auditCount); err != nil {
				t.Fatal(err)
			}
			if settingsCount != len(baseline) || auditCount != 0 {
				t.Fatal("rejected request wrote settings or a success audit")
			}
		})
	}
}

func TestCardPlatformIssueFailureNeverReturnsHistoricalInventoryAsIssued(t *testing.T) {
	for _, test := range []struct {
		name         string
		upstreamCode int
		malformed    bool
		disconnect   bool
		cached       bool
		wantStatus   int
		wantSync     bool
	}{
		{name: "definite_bad_request", upstreamCode: 400, wantStatus: 400},
		{name: "definite_rate_limit", upstreamCode: 429, wantStatus: 429},
		{name: "upstream_auth_failure_is_not_admin_logout", upstreamCode: 401, wantStatus: 502},
		{name: "uncertain_server_error_imports_old_inventory", upstreamCode: 502, wantStatus: 502, wantSync: true},
		{name: "uncertain_server_error_with_already_cached_inventory", upstreamCode: 502, cached: true, wantStatus: 502, wantSync: true},
		{name: "uncertain_malformed_success_response", upstreamCode: 200, malformed: true, wantStatus: 502, wantSync: true},
		{name: "uncertain_lost_response", disconnect: true, wantStatus: 502, wantSync: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			conn := newCardPlatformSecurityDB(t)
			t.Setenv("SERVER_MODE", "debug")
			oldCodes := []string{"ZC-HISTORY-001-SYNTHETIC-ONLY", "ZC-HISTORY-002-SYNTHETIC-ONLY"}
			if test.cached {
				for i, code := range oldCodes {
					if err := db.SaveCardplatformCDKCode(int64(101+i), code, code[:14], "plus", 15, "PH"); err != nil {
						t.Fatal(err)
					}
				}
			}
			var purchases, syncs atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/openapi/v1/gpt-direct/plans":
					_, _ = w.Write([]byte(`{"code":0,"data":{"version":1,"plans":{"plus":{"enabled":true,"currency":"PHP"}},"registry":[{"key":"plus","label":"Plus"}]}}`))
				case r.Method == http.MethodPost && r.URL.Path == "/openapi/v1/gpt-direct/cdks":
					purchases.Add(1)
					if r.Header.Get("Idempotency-Key") != "synthetic-single-purchase" {
						t.Error("purchase idempotency key was not preserved")
					}
					if test.disconnect {
						connection, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						_ = connection.Close()
						return
					}
					w.WriteHeader(test.upstreamCode)
					if test.malformed {
						_, _ = w.Write([]byte(`{"incomplete":`))
					} else {
						_, _ = w.Write([]byte(`{"code":1,"msg":"synthetic issue failure","error_code":"synthetic_rejection"}`))
					}
				case r.Method == http.MethodGet && r.URL.Path == "/openapi/v1/gpt-direct/cdks":
					syncs.Add(1)
					if r.URL.Query().Get("plan") != "plus" || r.URL.Query().Get("status") != "unused" {
						t.Error("inventory sync did not preserve the requested plan/status")
					}
					items := make([]map[string]any, 0, len(oldCodes))
					for i, code := range oldCodes {
						items = append(items, map[string]any{"id": 101 + i, "code": code, "code_prefix": code[:14], "plan": "plus", "status": "unused", "payment_country": "PH", "fee_amount_minor": 15})
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"list": items, "total": len(items)}})
				default:
					t.Errorf("unexpected upstream endpoint: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer upstream.Close()
			setSecurityFixtureSetting(t, "card_api_base", upstream.URL)
			setSecurityFixtureSetting(t, "card_api_key", "synthetic-issue-key")
			router := gin.New()
			router.POST("/issue", CardPlatformIssueCDKs)
			request := httptest.NewRequest(http.MethodPost, "/issue", strings.NewReader(`{"plan":"plus","count":1,"funding_confirmed":true,"payment_country":"PH"}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", "synthetic-single-purchase")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("issue status = %d, want %d", response.Code, test.wantStatus)
			}
			var body map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"issued", "codes", "requested", "recovered"} {
				if _, exists := body[field]; exists {
					t.Fatalf("failed purchase returned success field %s", field)
				}
			}
			for _, code := range oldCodes {
				if strings.Contains(response.Body.String(), code) {
					t.Fatal("failed purchase exposed historical inventory as newly issued")
				}
			}
			wantSyncs, wantStored, wantImported := 0, 0, 0
			if test.wantSync {
				wantSyncs, wantStored = 1, len(oldCodes)
				if !test.cached {
					wantImported = len(oldCodes)
				}
				if body["error_code"] != "issue_outcome_unknown" || body["inventory_synced_count"] != float64(wantImported) {
					t.Fatal("uncertain purchase did not retain an explicit unknown outcome and inventory count")
				}
			} else if body["error_code"] == "issue_outcome_unknown" {
				t.Fatal("definite upstream rejection became an uncertain purchase")
			}
			if purchases.Load() != 1 || syncs.Load() != int32(wantSyncs) {
				t.Fatalf("purchase requests=%d, inventory sync requests=%d; want 1/%d", purchases.Load(), syncs.Load(), wantSyncs)
			}
			var stored, issueAudits int
			if err := conn.QueryRow(`SELECT COUNT(*) FROM cardplatform_cdk_codes`).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			if err := conn.QueryRow(`SELECT COUNT(*) FROM admin_audit_logs WHERE action = 'cardplatform_issue_cdk'`).Scan(&issueAudits); err != nil {
				t.Fatal(err)
			}
			if stored != wantStored || issueAudits != 0 {
				t.Fatalf("stored inventory=%d, success audits=%d; want %d/0", stored, issueAudits, wantStored)
			}
		})
	}
}
