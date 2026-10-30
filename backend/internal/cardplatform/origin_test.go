package cardplatform

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestProductionOriginAllowlist(t *testing.T) {
	t.Setenv("SERVER_MODE", "release")
	t.Setenv("CARD_API_ALLOWED_ORIGINS", "")
	for _, base := range []string{"", "https://zovocard.com", "https://zovocard.com/openapi/v1/"} {
		if err := ValidateBase(base); err != nil {
			t.Errorf("valid origin rejected: %v", err)
		}
	}
	for _, base := range []string{"http://zovocard.com", "https://zovocard.com.attacker.invalid", "https://zovocard.com@attacker.invalid", "https://zovocard.com:8443", "https://zovocard.com?key=test", "https://zovocard.com/#fragment", "http://169.254.169.254", "http://127.0.0.1", "https://attacker.invalid", "file:///etc/passwd"} {
		if ValidateBase(base) == nil {
			t.Errorf("unsafe origin accepted: %s", base)
		}
	}
}

func TestInvalidProductionOriginNeverReceivesKey(t *testing.T) {
	t.Setenv("SERVER_MODE", "release")
	var hits atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer s.Close()
	c := New(Config{SiteBase: s.URL, APIKey: "synthetic-secret"})
	if _, err := c.GetPlans(context.Background()); err == nil {
		t.Fatal("expected blocked upstream")
	}
	if hits.Load() != 0 {
		t.Fatal("unsafe upstream was contacted")
	}
}

func TestUpstreamRedirectDoesNotLeakKeyOrReplayIssue(t *testing.T) {
	t.Setenv("SERVER_MODE", "debug")
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/collect", http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	c := New(Config{SiteBase: source.URL, APIKey: "synthetic-secret"})
	if _, err := c.GetPlans(context.Background()); err == nil {
		t.Fatal("expected redirect refusal")
	}
	if _, err := c.IssueCDKs(context.Background(), "plus", 1, "synthetic-idempotency"); err == nil {
		t.Fatal("expected issue redirect refusal")
	}
	if _, _, err := c.Preview(context.Background(), "synthetic-code", "test-device"); err == nil {
		t.Fatal("expected public redirect refusal")
	}
	if hits.Load() != 0 {
		t.Fatal("redirect target received a request")
	}
}
