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
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/cardplatform"
)

const fixtureCode = "ZC-0123456789-0123456789"

func fixtureUpstream(t *testing.T, status string) (*cardplatform.Client, *int) {
	t.Helper()
	orders := new(int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("X-API-Key") != "synthetic-owner-key" {
			t.Error("lookup performed a mutation or lost authentication")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/openapi/v1/gpt-direct/cdks":
			if r.URL.Query().Get("q") != fixtureCode {
				t.Error("lookup did not search the full code")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"total": 3, "list": []map[string]any{
				{"id": 1, "code": fixtureCode[:15] + "***", "status": "consumed"},
				{"id": 2, "code": "ZC-9999999999-9999999999", "status": "consumed"},
				{"id": 3, "code": fixtureCode, "status": status, "plan": "plus"},
			}}})
		case "/openapi/v1/gpt-direct/cdk-orders":
			*orders++
			if r.URL.Query().Get("cdk_id") != "3" {
				t.Error("order queried before full-code match")
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"list":[{"cdk_id":3,"account_email":"fixture@example.test","completed_at":"2026-10-28T00:00:00Z","card_number":"must-not-be-exposed","redemption_token":"must-not-be-exposed"}]}}`))
		default:
			t.Error("unexpected upstream path")
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("SERVER_MODE", "debug")
	return cardplatform.New(cardplatform.Config{SiteBase: srv.URL, APIKey: "synthetic-owner-key"}), orders
}

func TestAuthoritativeLifecycleAndExactCode(t *testing.T) {
	for _, tc := range []struct {
		upstream, want string
		used           bool
	}{
		{"consumed", "used", true}, {"unused", "unused", false}, {"reserved", "processing", false},
		{"frozen", "disabled", false}, {"expired", "expired", false},
	} {
		t.Run(tc.upstream, func(t *testing.T) {
			client, orders := fixtureUpstream(t, tc.upstream)
			got, err := Lookup(context.Background(), client, strings.ToLower(fixtureCode))
			if err != nil || got.Status != tc.want || got.Used != tc.used {
				t.Fatalf("got %+v, err %v", got, err)
			}
			if tc.want == "unused" && (*orders != 0 || got.AccountEmail != "") {
				t.Fatal("unused code inherited a previous account/order")
			}
			body, _ := json.Marshal(got)
			if strings.Contains(string(body), "must-not-be-exposed") || strings.Contains(string(body), "synthetic-owner-key") {
				t.Fatal("private upstream fields escaped")
			}
		})
	}
}

type unavailableUpstream struct{}

func (unavailableUpstream) ListCDKsQuery(context.Context, cardplatform.CDKListQuery) (*cardplatform.CDKListResult, error) {
	return nil, errors.New("secret upstream failure")
}
func (unavailableUpstream) ListCDKOrdersQuery(context.Context, cardplatform.CDKOrderListQuery) (json.RawMessage, error) {
	panic("unexpected order query")
}

func TestUnavailableIsNotNotFound(t *testing.T) {
	_, err := Lookup(context.Background(), unavailableUpstream{}, fixtureCode)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error=%v", err)
	}
	w := httptest.NewRecorder()
	Handler(unavailableUpstream{}).ServeHTTP(w, httptest.NewRequest("POST", "/lookup", strings.NewReader(`{"code":"`+fixtureCode+`"}`)))
	if w.Code != 503 || strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestSocketQueryAndRestrictedProtocol(t *testing.T) {
	client, _ := fixtureUpstream(t, "consumed")
	// macOS has a short Unix socket path limit; t.TempDir includes the full
	// test name and can exceed it under /var/folders.
	dir, err := os.MkdirTemp("", "cdk-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "lookup.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: Handler(client)}
	go srv.Serve(listener)
	t.Cleanup(func() { _ = srv.Close() })
	got, err := Query(context.Background(), path, fixtureCode)
	if err != nil || got.Status != "used" || got.AccountEmail != "fixture@example.test" {
		t.Fatalf("got %+v err=%v", got, err)
	}
	for _, body := range []string{`{"code":"` + fixtureCode + `","path":"/wallet"}`, `{"code":"` + fixtureCode + `"} {}`} {
		w := httptest.NewRecorder()
		Handler(client).ServeHTTP(w, httptest.NewRequest("POST", "/lookup", strings.NewReader(body)))
		if w.Code != 400 {
			t.Fatalf("accepted arbitrary protocol: %d", w.Code)
		}
	}
	if _, err := Query(context.Background(), path+".missing", fixtureCode); !errors.Is(err, ErrUnavailable) {
		t.Fatal("socket failure hidden")
	}
}

func TestLookupDoesNotFollowAuthenticatedRedirects(t *testing.T) {
	t.Setenv("SERVER_MODE", "debug")
	requests := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++ }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	client := cardplatform.NewReadOnly(cardplatform.Config{SiteBase: source.URL, APIKey: "synthetic-owner-key"})
	_, err := Lookup(context.Background(), client, fixtureCode)
	if !errors.Is(err, ErrUnavailable) || requests != 0 {
		t.Fatal("authenticated lookup followed redirect")
	}
}
