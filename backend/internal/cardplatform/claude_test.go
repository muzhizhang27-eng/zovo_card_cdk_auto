package cardplatform

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClaudeCatalogueIsScopedAndUSOnly(t *testing.T) {
	t.Setenv("SERVER_MODE", "debug")
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/openapi/v1/gpt-direct/plans" || r.URL.Query().Get("product") != "claude" {
			t.Error("Claude catalogue queried another product or performed a write")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"version":1,"payment_regions":[{"country":"PH","currency":"PHP"}],"plans":{"claude_pro":{"enabled":true,"currency":"USD","serviceFeeUsdMinor":15},"claude_max_5x":{"enabled":false,"currency":"USD"},"plus":{"enabled":true}},"registry":[{"key":"pro","label":"Claude Pro"},{"key":"max_5x","label":"Claude Max 5x"},{"key":"max_20x","label":"Claude Max 20x"}]}}`))
	}))
	defer s.Close()
	plans, err := New(Config{SiteBase: s.URL, APIKey: "fixture-key"}).GetPlans(context.Background(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	rows := plans.SellablePlans()
	if len(rows) != 1 || rows[0].Key != "pro" || rows[0].ServiceFeeUsdMinor != 15 {
		t.Fatalf("Claude product isolation failed: %+v", rows)
	}
	if len(plans.PaymentRegions) != 1 || plans.PaymentRegions[0].Country != "US" || plans.PaymentRegions[0].Currency != "USD" {
		t.Fatalf("invalid Claude region: %+v", plans.PaymentRegions)
	}
}

func TestClaudePlanWireNames(t *testing.T) {
	for _, plan := range []string{"pro", "max_5x", "max_20x"} {
		if !IsClaudePlan(plan) || !IsClaudePlan("claude_"+plan) || ClaudeIssuePlan("claude_"+plan) != "claude_"+plan {
			t.Fatalf("Claude plan mapping failed: %s", plan)
		}
	}
	for _, plan := range []string{"plus", "pro_20x", "claude_plus", "premium_monthly", "grok_super_monthly_half"} {
		if IsClaudePlan(plan) {
			t.Fatalf("cross-product Claude match: %s", plan)
		}
	}
}
