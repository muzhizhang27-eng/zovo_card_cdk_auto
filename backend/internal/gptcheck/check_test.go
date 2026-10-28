package gptcheck

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

const accountFixture = `{"default_account":"a","accounts":{"a":{"account":{"account_id":"a","plan_type":"plus"},"entitlement":{"has_active_subscription":true}}}}`

func TestRefreshExpiredSessionAndGetInvoices(t *testing.T) {
	calls := 0
	send := func(_, url string, headers map[string]string) (int, []byte, error) {
		calls++
		if calls == 1 {
			return 401, []byte(`{"error":"expired"}`), nil
		}
		if headers["Authorization"] != "Bearer fresh" {
			t.Fatal("refreshed token not used")
		}
		if strings.Contains(url, "/invoices") {
			return 200, []byte(`{"data":[{"id":"fixture","hosted_invoice_url":"https://invoice.stripe.com/i/fixture"}]}`), nil
		}
		return 200, []byte(accountFixture), nil
	}
	result, err := check(`{"accessToken":"expired","sessionToken":"session"}`, send, func(token string) (string, error) {
		if token != "session" {
			t.Fatal("wrong session")
		}
		return "fresh", nil
	})
	if err != nil || len(result.Invoices) != 1 || calls != 3 {
		t.Fatalf("result=%+v error=%v calls=%d", result, err, calls)
	}
}

func TestRefreshFailureIsVisible(t *testing.T) {
	_, err := check(`{"accessToken":"expired","sessionToken":"session"}`, func(string, string, map[string]string) (int, []byte, error) { return 401, []byte(`{}`), nil }, func(string) (string, error) { return "", errors.New("刷新 session 失败 HTTP 403") })
	if ErrorCode(err) != "session_refresh_failed" || !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("err=%v", err)
	}
}

func TestTokenOnlyAndPartialInvoiceFailure(t *testing.T) {
	result, err := check(`{"accessToken":"valid"}`, func(_, url string, _ map[string]string) (int, []byte, error) {
		if strings.Contains(url, "/invoices") {
			return http.StatusForbidden, nil, nil
		}
		return 200, []byte(accountFixture), nil
	}, func(string) (string, error) { t.Fatal("unneeded refresh"); return "", nil })
	if err != nil || len(result.Warnings) != 1 || result.Invoices == nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestStatusBeforeJSONAndMissingAccount(t *testing.T) {
	for _, tc := range []struct {
		status     int
		body, code string
	}{
		{401, "<html>unauthorized</html>", "credential_expired"},
		{401, `{"error":{"code":"token_revoked"}}`, "credential_revoked"},
		{403, "<html>challenge</html>", "upstream_unavailable"},
		{200, `{}`, "upstream_unavailable"},
	} {
		_, err := check("token", func(string, string, map[string]string) (int, []byte, error) { return tc.status, []byte(tc.body), nil }, nil)
		if ErrorCode(err) != tc.code {
			t.Fatalf("status %d: error=%v", tc.status, err)
		}
	}
}
