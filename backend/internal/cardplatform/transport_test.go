package cardplatform

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestScopedProxyRejectsInvalidConfigurationWithoutDial(t *testing.T) {
	t.Setenv("CARD_API_PROXY_URL", "http://secret:password@127.0.0.1:18784")
	c := NewHTTPClient(time.Second)
	_, err := c.Get("https://zovocard.com/health")
	if err == nil || !strings.Contains(err.Error(), "CARD_API_PROXY_URL") || strings.Contains(err.Error(), "password") {
		t.Fatalf("expected sanitized proxy configuration failure, got %v", err)
	}
	if _, ok := c.Transport.(unavailableTransport); !ok {
		t.Fatal("invalid proxy can fall back to a network transport")
	}
}

func TestScopedProxyOnlyDialsConfiguredEndpoint(t *testing.T) {
	rt, err := newProxyTransport("http://127.0.0.1:18784")
	if err != nil {
		t.Fatal(err)
	}
	transport := rt.(*http.Transport)
	var dials []string
	transport.DialContext = func(_ context.Context, _, address string) (net.Conn, error) {
		dials = append(dials, address)
		return nil, errors.New("synthetic unavailable proxy")
	}
	c := &http.Client{Transport: transport, Timeout: time.Second}
	for _, target := range []string{"https://zovocard.com/health", "https://zovocard.com:443/health"} {
		if _, err := c.Get(target); err == nil {
			t.Fatal("unavailable proxy unexpectedly succeeded")
		}
	}
	if len(dials) != 2 || dials[0] != "127.0.0.1:18784" || dials[1] != "127.0.0.1:18784" {
		t.Fatalf("proxy failure caused unexpected dial: %v", dials)
	}
	for _, target := range []string{"http://zovocard.com/", "https://zovocard.com:8443/", "https://chatgpt.com/", "https://zovocard.com.attacker.invalid/", "http://lookup/lookup", "http://127.0.0.1:8788/"} {
		if _, err := c.Get(target); err == nil {
			t.Errorf("unexpected allowed target %s", target)
		}
	}
	if len(dials) != 2 {
		t.Fatalf("non-Zovo target reached network: %v", dials)
	}
}

func TestScopedProxyCONNECTAndTLS(t *testing.T) {
	var upstreamHits, proxyHits atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits.Add(1)
		if r.Host != "zovocard.com" || r.URL.Path != "/health" {
			t.Error("original target changed inside tunnel")
		}
		_, _ = io.WriteString(w, "synthetic-ok")
	}))
	defer upstream.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		if r.Method != http.MethodConnect || r.Host != "zovocard.com:443" || r.Header.Get("X-API-Key") != "" {
			t.Error("unexpected CONNECT authority or leaked API key")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		remote, err := net.Dial("tcp", upstream.Listener.Addr().String())
		if err != nil {
			t.Error(err)
			return
		}
		defer remote.Close()
		local, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer local.Close()
		_, _ = io.WriteString(local, "HTTP/1.1 200 Connection Established\r\n\r\n")
		go func() { _, _ = io.Copy(remote, local); _ = remote.Close() }()
		_, _ = io.Copy(local, remote)
	}))
	defer proxy.Close()
	rt, err := newProxyTransport(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := rt.(*http.Transport)
	defer transport.CloseIdleConnections()
	// Trust only this local synthetic TLS server; production has no TLS override.
	transport.TLSClientConfig = upstream.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	transport.TLSClientConfig.ServerName = "127.0.0.1"
	c := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	resp, err := c.Get("https://zovocard.com/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "synthetic-ok" || upstreamHits.Load() != 1 || proxyHits.Load() != 1 {
		t.Fatalf("unexpected tunnel result: %q upstream=%d proxy=%d", body, upstreamHits.Load(), proxyHits.Load())
	}
}

func TestAllCardClientsRetainScopedProxyOnCONNECTFailure(t *testing.T) {
	var requests atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodConnect || r.Host != "zovocard.com:443" || r.Header.Get("X-API-Key") != "" {
			t.Error("unexpected CONNECT request")
		}
		w.WriteHeader(http.StatusProxyAuthRequired)
	}))
	defer proxy.Close()
	t.Setenv("CARD_API_PROXY_URL", proxy.URL)
	t.Setenv("SERVER_MODE", "release")
	previousDefault := http.DefaultTransport
	client := New(Config{SiteBase: "https://zovocard.com", APIKey: "synthetic-key"})
	if _, err := client.GetPlans(context.Background()); err == nil {
		t.Fatal("GetPlans bypassed refused proxy")
	}
	if _, err := client.IssueCDKs(context.Background(), "plus", 1, "synthetic-idempotency"); err == nil {
		t.Fatal("IssueCDKs bypassed refused proxy")
	}
	if _, _, err := client.Preview(context.Background(), "synthetic-code", "synthetic-device"); err == nil {
		t.Fatal("Preview bypassed refused proxy")
	}
	readonly := NewReadOnly(client.cfg)
	if _, err := readonly.GetPlans(context.Background()); err == nil {
		t.Fatal("read-only lookup bypassed refused proxy")
	}
	if requests.Load() != 4 || http.DefaultTransport != previousDefault {
		t.Fatalf("unexpected requests=%d or mutated global transport", requests.Load())
	}
}

func TestScopedProxyRetainsRedirectRefusalAndSharesPool(t *testing.T) {
	t.Setenv("CARD_API_PROXY_URL", "http://127.0.0.1:18784")
	a, b := NewHTTPClient(time.Second), NewHTTPClient(time.Second)
	if a.Transport == nil || a.Transport != b.Transport {
		t.Fatal("card clients do not share the scoped connection pool")
	}
	if a.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Fatal("redirects may leak credentials or replay writes")
	}
	t.Setenv("CARD_API_PROXY_URL", "")
	if NewHTTPClient(time.Second).Transport != nil {
		t.Fatal("unconfigured proxy changed existing transport behavior")
	}
}
