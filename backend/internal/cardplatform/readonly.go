package cardplatform

import (
	"net/http"
	"time"
)

// NewReadOnly is used by the exact-code lookup broker. In particular, never
// forward the owner's authentication header to an upstream redirect target.
func NewReadOnly(cfg Config) *Client {
	client := New(cfg)
	client.client.Timeout = 25 * time.Second
	client.client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return client
}
