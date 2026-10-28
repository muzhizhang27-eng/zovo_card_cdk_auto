package handler

import (
	"database/sql"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestXIssueInfersProductWithoutQueryParameter(t *testing.T) {
	c, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	c.SetMaxOpenConns(1)
	old := db.DB
	db.DB = c
	t.Cleanup(func() { db.DB = old; c.Close() })
	for _, q := range []string{`CREATE TABLE site_settings(key TEXT PRIMARY KEY,value TEXT,updated_at DATETIME)`, `CREATE TABLE cardplatform_cdk_codes(upstream_id INTEGER,code TEXT UNIQUE NOT NULL,code_prefix TEXT,plan TEXT,fee_amount_minor INTEGER,status TEXT,payment_country TEXT,created_at DATETIME)`} {
		if _, err = c.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	issued := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/openapi/v1/gpt-direct/plans":
			if r.URL.Query().Get("product") != "x" {
				t.Error("X issue fetched GPT catalogue")
			}
			w.Write([]byte(`{"code":0,"data":{"version":1,"plans":{"x_plus_monthly":{"enabled":true,"currency":"USD"}},"registry":[{"key":"plus_monthly","label":"X Plus"}]}}`))
		case "/openapi/v1/gpt-direct/cdks":
			var b map[string]any
			if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
				t.Error(err)
			}
			if b["plan"] != "plus_monthly" || b["payment_country"] != "US" {
				t.Error("wrong X issue parameters")
			}
			issued++
			w.Write([]byte(`{"code":0,"data":{"requested":1,"issued":[{"id":1,"code":"ZC-FAKE-TEST-X-CODE","plan":"plus_monthly","payment_country":"US"}]}}`))
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer s.Close()
	if e := db.SetSetting("card_api_base", s.URL); e != nil {
		t.Fatal(e)
	}
	if e := db.SetSetting("card_api_key", "fixture-key"); e != nil {
		t.Fatal(e)
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/issue", CardPlatformIssueCDKs)
	req := httptest.NewRequest("POST", "/issue", strings.NewReader(`{"plan":"plus_monthly","count":1,"funding_confirmed":true}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 || issued != 1 {
		t.Fatalf("X issue failed %d %s", w.Code, w.Body.String())
	}
}
