package cardplatform

import (
	"encoding/json"
	"errors"
	"strings"
)

func XPaymentCountry(country string) (string, error) {
	country = strings.ToUpper(strings.TrimSpace(country))
	if country == "" {
		country = "US" // 2026-10-24 XPASS 改版:X 默认 US/USD(对齐 Grok)
	}
	switch country {
	case "US", "JP", "PH", "NG", "TR", "EG":
		return country, nil
	}
	return "", errors.New("X supports US, JP, PH, NG, TR and EG only")
}

// 2026-10-24 X 会员并入 XPASS:四档 plus/premium/super/ultra(替换旧 basic/premium/premium_plus)。
func IsXPremiumPlan(plan string) bool {
	plan = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(plan)), "x_")
	switch plan {
	case "plus_monthly", "plus_yearly", "premium_monthly", "premium_yearly",
		"super_monthly", "super_yearly", "ultra_monthly", "ultra_yearly":
		return true
	}
	return false
}

// X cookies are execution credentials, not reusable ChatGPT invoice sessions.
// 2026-10-24 凭据简化:只需 auth_token(裸串或 JSON{auth_token});ct0/billing_email 不再要。
func IsXPremiumCredential(raw string) bool {
	s := strings.TrimSpace(raw)
	if s == "" {
		return false
	}
	if !strings.HasPrefix(s, "{") {
		// 裸 auth_token(x.com cookie 的登录 token,十六进制约 40 位)
		return len(s) >= 16 && !strings.ContainsAny(s, " \t\n")
	}
	var x map[string]json.RawMessage
	if json.Unmarshal([]byte(s), &x) != nil {
		return false
	}
	return len(x["auth_token"]) > 0 || len(x["authToken"]) > 0 ||
		(len(x["cookieHeader"]) > 0 && (len(x["billing_email"]) > 0 || len(x["billingEmail"]) > 0))
}
