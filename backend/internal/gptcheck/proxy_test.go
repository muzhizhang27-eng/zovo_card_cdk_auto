package gptcheck

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	fhttp "github.com/bogdanfinn/fhttp"
)

func TestGPTProxyRejectsInvalidConfiguration(t *testing.T) {
	c, err := newClient("http://user:synthetic-secret@127.0.0.1:18785")
	if c != nil || err == nil || strings.Contains(err.Error(), "synthetic-secret") {
		t.Fatalf("invalid proxy must fail closed without credential leak: %v", err)
	}
}

func TestGPTProxyCONNECTRefusalDoesNotFallBack(t *testing.T) {
	var requests atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodConnect || r.Host != "chatgpt.com:443" || r.Header.Get("Authorization") != "" {
			t.Error("unexpected CONNECT target or leaked bearer token")
		}
		w.WriteHeader(http.StatusProxyAuthRequired)
	}))
	defer proxy.Close()
	c, err := newClient(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseIdleConnections()
	req, err := fhttp.NewRequest(http.MethodGet, checkV4URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer synthetic-token")
	if _, err := c.Do(req); err == nil {
		t.Fatal("refused GPT proxy unexpectedly succeeded")
	}
	if requests.Load() != 1 {
		t.Fatalf("unexpected CONNECT count: %d", requests.Load())
	}
}

func TestGPTRequestOriginsAreRestricted(t *testing.T) {
	for _, raw := range []string{checkV4URL, invoicesURL + "synthetic", sessionAuthURL} {
		if err := validateRequestURL(raw); err != nil {
			t.Errorf("valid billing endpoint rejected: %v", err)
		}
	}
	for _, raw := range []string{"http://chatgpt.com/", "https://chatgpt.com:8443/", "https://chatgpt.com.attacker.invalid/", "https://api.openai.com/", "https://invoice.stripe.com/", "http://127.0.0.1/", "https://user:secret@chatgpt.com/"} {
		if _, _, err := requestWithCookies(http.MethodGet, raw, nil, nil); err == nil {
			t.Errorf("unsafe billing origin accepted: %s", raw)
		}
	}
}
