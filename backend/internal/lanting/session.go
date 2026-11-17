package lanting

import (
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/tuzi/cdk-recharge-system/internal/cardplatform"
)

// ExtractSession matches the redemption page rule: a Session JSON document
// that contains sessionToken, a bare 5-part JWE, or another long token.
// A 3-part access token is rejected.
func ExtractSession(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if !strings.HasPrefix(s, "{") && strings.Count(s, ".") >= 4 {
		return s
	}
	if !strings.HasPrefix(s, "{") && strings.HasPrefix(s, "eyJ") && strings.Count(s, ".") == 2 {
		return ""
	}
	if strings.HasPrefix(s, "{") {
		obj, canonical, ok := parseSessionJSON(s)
		if !ok {
			return ""
		}
		if strings.TrimSpace(sessionToken(obj)) == "" {
			return ""
		}
		return canonical
	}
	if len(s) > 40 {
		return s
	}
	return ""
}

// AcceptActivationSession is the ChatGPT session the redemption page forwards.
// Grok cookies and bare X auth tokens are handled by other credential fields
// and are not activation tokens for the lanting order.
func AcceptActivationSession(raw string) string {
	session := ExtractSession(raw)
	if session == "" || cardplatform.IsGrokCredential(session) {
		return ""
	}
	if !strings.HasPrefix(session, "{") && strings.Count(session, ".") < 4 && cardplatform.IsXPremiumCredential(session) {
		return ""
	}
	return session
}

func parseSessionJSON(raw string) (map[string]any, string, bool) {
	if obj, ok := decodeObject(raw); ok {
		return obj, compactJSON(raw), true
	}
	cleaned := stripTrailingCommas(raw)
	if obj, ok := decodeObject(cleaned); ok {
		return obj, compactJSON(cleaned), true
	}
	return nil, "", false
}

func decodeObject(raw string) (map[string]any, bool) {
	var obj map[string]any
	if json.Unmarshal([]byte(raw), &obj) != nil || obj == nil {
		return nil, false
	}
	return obj, true
}

func sessionToken(obj map[string]any) string {
	if s := asString(obj["sessionToken"]); s != "" {
		return s
	}
	if s := asString(obj["session_token"]); s != "" {
		return s
	}
	token, _ := obj["token"].(map[string]any)
	if token == nil {
		return ""
	}
	return asString(token["sessionToken"])
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

// stripTrailingCommas removes commas that sit immediately before } or ]
// outside string literals. Characters inside strings are preserved.
func stripTrailingCommas(src string) string {
	var b strings.Builder
	b.Grow(len(src))
	inStr := false
	escaped := false
	for i := 0; i < len(src); i++ {
		ch := src[i]
		if inStr {
			b.WriteByte(ch)
			if escaped {
				escaped = false
			} else if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				inStr = false
			}
			continue
		}
		if ch == '"' {
			inStr = true
			b.WriteByte(ch)
			continue
		}
		if ch == ',' {
			j := i + 1
			for j < len(src) {
				r, size := utf8.DecodeRuneInString(src[j:])
				if r == utf8.RuneError && size == 1 {
					break
				}
				if !unicode.IsSpace(r) {
					break
				}
				j += size
			}
			if j < len(src) && (src[j] == '}' || src[j] == ']') {
				continue
			}
		}
		b.WriteByte(ch)
	}
	return b.String()
}

// compactJSON drops ASCII whitespace outside strings and keeps every literal.
func compactJSON(src string) string {
	var b strings.Builder
	b.Grow(len(src))
	inStr := false
	escaped := false
	for i := 0; i < len(src); i++ {
		ch := src[i]
		if inStr {
			b.WriteByte(ch)
			if escaped {
				escaped = false
			} else if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				inStr = false
			}
			continue
		}
		if ch == '"' {
			inStr = true
			b.WriteByte(ch)
			continue
		}
		if ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n' {
			continue
		}
		b.WriteByte(ch)
	}
	return b.String()
}
