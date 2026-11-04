package outboundproxy

import "testing"

func TestParseLoopback(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:18784", "http://[::1]:18785"} {
		if _, err := ParseLoopback(raw); err != nil {
			t.Errorf("valid proxy rejected: %v", err)
		}
	}
	for _, raw := range []string{"", "http://localhost:18784", "https://127.0.0.1:18784", "http://127.0.0.1", "http://127.0.0.1:0", "http://127.0.0.1:65536", "http://127.0.0.1:18784/", "http://127.0.0.1:18784?", "http://127.0.0.1:18784?x=y", "http://127.0.0.1:18784#fragment", "http://user:password@127.0.0.1:18784", "http://192.0.2.1:18784", "://bad"} {
		if _, err := ParseLoopback(raw); err == nil {
			t.Errorf("invalid proxy accepted: %s", raw)
		}
	}
}
