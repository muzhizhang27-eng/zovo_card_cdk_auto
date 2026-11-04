// Package outboundproxy validates private, service-specific CONNECT endpoints.
package outboundproxy

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// ParseLoopback accepts only an explicit local HTTP CONNECT endpoint. Proxy
// credentials and remote proxy hosts are deliberately not supported.
func ParseLoopback(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "http" || u.User != nil || u.Opaque != "" ||
		u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, fmt.Errorf("proxy must be a loopback HTTP endpoint with an explicit port")
	}
	ip := net.ParseIP(u.Hostname())
	port, portErr := strconv.Atoi(u.Port())
	if ip == nil || !ip.IsLoopback() || portErr != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("proxy must be a loopback HTTP endpoint with an explicit port")
	}
	return u, nil
}
