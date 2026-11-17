package lanting

import (
	"strings"
	"testing"
)

const syntheticJWE = "eyJ0.fixture.payload.iv.tag"

func TestExtractSessionMatchesRedemptionRules(t *testing.T) {
	pretty := "{\n  \"user\": { \"name\": \"fixture\" },\n  \"sessionToken\": \"" + syntheticJWE + "\"\n}"
	got := ExtractSession(pretty)
	if got != `{"user":{"name":"fixture"},"sessionToken":"`+syntheticJWE+`"}` {
		t.Fatalf("pretty session = %s", got)
	}
	trailing := "{\n  \"sessionToken\": \"" + syntheticJWE + "\",\n  \"arr\": [1, 2,],\n}"
	got = ExtractSession(trailing)
	if !strings.Contains(got, syntheticJWE) || strings.Contains(got, "\n") || strings.Contains(got, ",]") {
		t.Fatalf("trailing comma session = %s", got)
	}
	preserved := "{\n  \"sessionToken\": \"" + syntheticJWE + "\",\n  \"note\": \"a,b,}] not json\",\n}"
	got = ExtractSession(preserved)
	if !strings.Contains(got, `"note":"a,b,}] not json"`) {
		t.Fatalf("string contents changed: %s", got)
	}
	numbers := "{\n  \"sessionToken\": \"" + syntheticJWE + "\",\n  \"largeId\": 9007199254740993,\n  \"negativeZero\": -0,\n  \"exponent\": 1.2300e+04\n}"
	got = ExtractSession(numbers)
	want := `{"sessionToken":"` + syntheticJWE + `","largeId":9007199254740993,"negativeZero":-0,"exponent":1.2300e+04}`
	if got != want {
		t.Fatalf("number literals = %s", got)
	}
	if ExtractSession(`{"accessToken":"eyJ.fixture.payload"}`) != "" {
		t.Fatal("access token json was accepted")
	}
	if ExtractSession("eyJ.fixture.payload") != "" {
		t.Fatal("bare access token was accepted")
	}
	if ExtractSession(syntheticJWE) != syntheticJWE {
		t.Fatal("bare jwe was rejected")
	}
	if ExtractSession(`{"session_token":"`+syntheticJWE+`"}`) == "" {
		t.Fatal("session_token was rejected")
	}
	if ExtractSession(`{"token":{"sessionToken":"`+syntheticJWE+`"}}`) == "" {
		t.Fatal("nested sessionToken was rejected")
	}
	if ExtractSession("{ nope") != "" {
		t.Fatal("invalid json was accepted")
	}
	if len(strings.Repeat("a", 40)) == 40 && ExtractSession(strings.Repeat("a", 40)) != "" {
		t.Fatal("40 character token was accepted")
	}
	long := strings.Repeat("a", 41)
	if ExtractSession(long) != long {
		t.Fatal("long token was rejected")
	}
}

func TestAcceptActivationSessionSkipsOtherProducts(t *testing.T) {
	grok := "sso=" + strings.Repeat("a", 40)
	if AcceptActivationSession(grok) != "" {
		t.Fatal("grok cookie was accepted")
	}
	xToken := strings.Repeat("ab", 21)
	if AcceptActivationSession(xToken) != "" {
		t.Fatal("bare x token was accepted")
	}
	if AcceptActivationSession(`{"sessionToken":"`+syntheticJWE+`"}`) == "" {
		t.Fatal("chatgpt session json was rejected")
	}
	if AcceptActivationSession(syntheticJWE) != syntheticJWE {
		t.Fatal("chatgpt jwe was rejected")
	}
}
