package cardplatform

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// ValidateBase prevents a saved upstream address from redirecting the owner's
// API key to an arbitrary server. Additional production origins are operator
// configuration, never an administrator API setting.
func ValidateBase(raw string) error {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	base = strings.TrimSuffix(base, "/openapi/v1")
	base = strings.TrimSuffix(base, "/openapi")
	if base == "" {
		base = defaultBase
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || u.Opaque != "" {
		return fmt.Errorf("card platform address must be an allowed origin without credentials, path, query or fragment")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return fmt.Errorf("card platform address must use HTTPS")
	}
	// Local mock servers remain available during development and automated tests.
	if os.Getenv("SERVER_MODE") != "release" {
		return nil
	}
	if u.Scheme != "https" {
		return fmt.Errorf("production card platform address must use HTTPS")
	}
	allowed := os.Getenv("CARD_API_ALLOWED_ORIGINS")
	if strings.TrimSpace(allowed) == "" {
		allowed = defaultBase
	}
	for _, origin := range strings.Split(allowed, ",") {
		if base == strings.TrimRight(strings.TrimSpace(origin), "/") {
			return nil
		}
	}
	return fmt.Errorf("card platform origin is not permitted by server configuration")
}

func rejectRedirect(_ *http.Request, _ []*http.Request) error {
	// Redirecting writes can replay purchases or leak custom X-API-Key headers.
	return http.ErrUseLastResponse
}
