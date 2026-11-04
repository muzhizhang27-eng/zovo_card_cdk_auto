package cardplatform

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/outboundproxy"
)

// Reuse connection pools across the short-lived API clients created by handlers.
var proxyTransports sync.Map

type unavailableTransport struct{ err error }

func (t unavailableTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, t.err
}

func newProxyTransport(raw string) (http.RoundTripper, error) {
	proxyURL, err := outboundproxy.ParseLoopback(raw)
	if err != nil {
		return nil, fmt.Errorf("CARD_API_PROXY_URL: %w", err)
	}
	return &http.Transport{
		Proxy: func(req *http.Request) (*url.URL, error) {
			// This tunnel is dedicated to Zovo. A saved API origin or redirect
			// must never route another provider through it or bypass the proxy.
			if req.URL.Scheme != "https" || req.URL.Hostname() != "zovocard.com" ||
				(req.URL.Port() != "" && req.URL.Port() != "443") || req.URL.User != nil {
				return nil, fmt.Errorf("card platform proxy only permits https://zovocard.com:443")
			}
			return proxyURL, nil
		},
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}, nil
}

// NewHTTPClient scopes the optional SG egress tunnel to the card platform only.
// A configured proxy always fails closed; it never falls back to a direct dial.
// With no explicit configuration, retain the pre-migration transport behavior.
func NewHTTPClient(timeout time.Duration) *http.Client {
	client := &http.Client{Timeout: timeout, CheckRedirect: rejectRedirect}
	raw := strings.TrimSpace(os.Getenv("CARD_API_PROXY_URL"))
	if raw == "" {
		return client
	}
	if transport, ok := proxyTransports.Load(raw); ok {
		client.Transport = transport.(http.RoundTripper)
		return client
	}
	transport, err := newProxyTransport(raw)
	if err != nil {
		client.Transport = unavailableTransport{err}
		return client
	}
	actual, _ := proxyTransports.LoadOrStore(raw, transport)
	client.Transport = actual.(http.RoundTripper)
	return client
}
