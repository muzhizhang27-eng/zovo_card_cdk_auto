package handler

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/db"
)

func TestClaudeIssueIsScopedPrefixedAndUSOnly(t *testing.T) {
	t.Setenv("SERVER_MODE", "debug")
	conn, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	previous := db.DB
	db.DB = conn
	t.Cleanup(func() { db.DB = previous; _ = conn.Close() })
	for _, query := range []string{
		`CREATE TABLE site_settings(key TEXT PRIMARY KEY,value TEXT,updated_at DATETIME)`,
		`CREATE TABLE cardplatform_cdk_codes(upstream_id INTEGER,code TEXT UNIQUE NOT NULL,code_prefix TEXT,plan TEXT,fee_amount_minor INTEGER,status TEXT,payment_country TEXT,created_at DATETIME)`,
	} {
		if _, err := conn.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	issued := []string{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/openapi/v1/gpt-direct/plans":
			if r.URL.Query().Get("product") != "claude" {
				t.Error("Claude issue used another catalogue")
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"version":1,"plans":{"claude_pro":{"enabled":true,"currency":"USD"},"claude_max_5x":{"enabled":true,"currency":"USD"},"claude_max_20x":{"enabled":true,"currency":"USD"},"plus":{"enabled":true}},"registry":[{"key":"pro","label":"Pro"},{"key":"max_5x","label":"Max5x"},{"key":"max_20x","label":"Max20x"}]}}`))
		case "/openapi/v1/gpt-direct/cdks":
			var body struct {
				Plan    string `json:"plan"`
				Country string `json:"payment_country"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				t.Fatal("invalid issue body")
			}
			if !strings.HasPrefix(body.Plan, "claude_") || body.Country != "US" {
				t.Errorf("unsafe Claude issue: %+v", body)
			}
			issued = append(issued, body.Plan)
			_, _ = w.Write([]byte(`{"code":0,"data":{"requested":1,"issued":[{"id":1,"code":"ZC-FAKE-CLAUDE-TEST-CODE","plan":"pro","payment_country":"US"}]}}`))
		default:
			t.Errorf("unexpected upstream endpoint %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	if err := db.SetSetting("card_api_base", upstream.URL); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting("card_api_key", "fixture-key"); err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/issue", CardPlatformIssueCDKs)
	for _, body := range []string{
		`{"plan":"pro","product":"claude","count":1,"funding_confirmed":true,"payment_country":"PH"}`,
		`{"plan":"claude_max_5x","count":1,"funding_confirmed":true}`,
		`{"plan":"max_20x","product":"claude","count":1,"funding_confirmed":true}`,
	} {
		req := httptest.NewRequest("POST", "/issue", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != 200 {
			t.Fatalf("Claude issue failed: %d %s", response.Code, response.Body.String())
		}
	}
	want := []string{"claude_pro", "claude_max_5x", "claude_max_20x"}
	if strings.Join(issued, ",") != strings.Join(want, ",") {
		t.Fatalf("wrong wire plans: %v", issued)
	}
}
