package codex

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestParseJWTTokenAcceptsStringAudience(t *testing.T) {
	payload, err := json.Marshal(map[string]any{
		"aud": "codex",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "account-123",
			"chatgpt_user_id":    "user-123",
		},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	token := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." +
		base64.RawURLEncoding.EncodeToString(payload) + ".signature"

	claims, err := ParseJWTToken(token)
	if err != nil {
		t.Fatalf("ParseJWTToken() error = %v", err)
	}
	if len(claims.Aud) != 1 || claims.Aud[0] != "codex" {
		t.Fatalf("Aud = %#v", claims.Aud)
	}
}

func TestParseJWTTokenRejectsMalformedCompactSerialization(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{}`))
	tests := []string{
		"." + payload + ".signature",
		"header..signature",
		"header." + payload + ".",
		"header." + payload + ".signature.extra",
	}
	for _, token := range tests {
		if _, err := ParseJWTToken(token); err == nil {
			t.Fatalf("ParseJWTToken(%q) unexpectedly succeeded", token)
		}
	}
}

func TestParseJWTTokenRejectsPaddedPayload(t *testing.T) {
	payload := base64.URLEncoding.EncodeToString([]byte(`{"sub":"padding"}`))
	if _, err := ParseJWTToken("header." + payload + ".signature"); err == nil {
		t.Fatal("ParseJWTToken() accepted padded JWT payload")
	}
}

func TestParseJWTTokenRejectsInvalidUTF8Payload(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString([]byte{'{', '"', 'e', 'm', 'a', 'i', 'l', '"', ':', '"', 0xff, '"', '}'})
	if _, err := ParseJWTToken("header." + payload + ".signature"); err == nil {
		t.Fatal("ParseJWTToken() accepted invalid UTF-8 payload")
	}
}

func makeTestJWT(payload map[string]any) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payloadBytes, _ := json.Marshal(payload)
	claims := base64.RawURLEncoding.EncodeToString(payloadBytes)
	// A non-empty signature keeps the token acceptable to the strict compact
	// serialization checks.
	return header + "." + claims + ".signature"
}

func TestGetPlanType(t *testing.T) {
	var nilClaims *JWTClaims
	if got := nilClaims.GetPlanType(); got != DefaultPlanType {
		t.Fatalf("nilClaims.GetPlanType() = %q, want %q", got, DefaultPlanType)
	}

	emptyClaims := &JWTClaims{}
	if got := emptyClaims.GetPlanType(); got != DefaultPlanType {
		t.Fatalf("emptyClaims.GetPlanType() = %q, want %q", got, DefaultPlanType)
	}

	whitespaceClaims := &JWTClaims{
		CodexAuthInfo: CodexAuthInfo{
			ChatgptPlanType: "   ",
		},
	}
	if got := whitespaceClaims.GetPlanType(); got != DefaultPlanType {
		t.Fatalf("whitespaceClaims.GetPlanType() = %q, want %q", got, DefaultPlanType)
	}

	proClaims := &JWTClaims{
		CodexAuthInfo: CodexAuthInfo{
			ChatgptPlanType: "pro",
		},
	}
	if got := proClaims.GetPlanType(); got != "pro" {
		t.Fatalf("proClaims.GetPlanType() = %q, want %q", got, "pro")
	}
}

func TestParseJWTToken_MissingPlanTypeDefaultsToFree(t *testing.T) {
	jwtWithoutPlan := makeTestJWT(map[string]any{
		"email": "user@example.com",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acc-12345",
		},
	})

	claims, errParse := ParseJWTToken(jwtWithoutPlan)
	if errParse != nil {
		t.Fatalf("ParseJWTToken failed: %v", errParse)
	}
	if got := claims.GetPlanType(); got != "free" {
		t.Fatalf("claims.GetPlanType() = %q, want %q", got, "free")
	}

	jwtWithPlan := makeTestJWT(map[string]any{
		"email": "user@example.com",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acc-12345",
			"chatgpt_plan_type":  "team",
		},
	})

	claimsTeam, errParseTeam := ParseJWTToken(jwtWithPlan)
	if errParseTeam != nil {
		t.Fatalf("ParseJWTToken failed: %v", errParseTeam)
	}
	if got := claimsTeam.GetPlanType(); got != "team" {
		t.Fatalf("claimsTeam.GetPlanType() = %q, want %q", got, "team")
	}
}
