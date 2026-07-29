package codex

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// JWTClaims represents the claims section of a JSON Web Token (JWT).
// It includes standard claims like issuer, subject, and expiration time, as well as
// custom claims specific to OpenAI's authentication.
type JWTClaims struct {
	AtHash        string        `json:"at_hash"`
	Aud           JWTAudience   `json:"aud"`
	AuthProvider  string        `json:"auth_provider"`
	AuthTime      int           `json:"auth_time"`
	Email         string        `json:"email"`
	EmailVerified bool          `json:"email_verified"`
	Exp           int           `json:"exp"`
	Profile       CodexProfile  `json:"https://api.openai.com/profile"`
	CodexAuthInfo CodexAuthInfo `json:"https://api.openai.com/auth"`
	Iat           int           `json:"iat"`
	Iss           string        `json:"iss"`
	Jti           string        `json:"jti"`
	Rat           int           `json:"rat"`
	Sid           string        `json:"sid"`
	Sub           string        `json:"sub"`
}

// JWTAudience accepts either standard JWT audience representation.
type JWTAudience []string

// UnmarshalJSON accepts an audience string or an array of audience strings.
func (audience *JWTAudience) UnmarshalJSON(data []byte) error {
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*audience = JWTAudience{single}
		return nil
	}
	var multiple []string
	if err := json.Unmarshal(data, &multiple); err != nil {
		return err
	}
	*audience = multiple
	return nil
}

// CodexProfile contains profile claims that may supplement top-level identity claims.
type CodexProfile struct {
	Email string `json:"email"`
}

// Organizations defines the structure for organization details within the JWT claims.
// It holds information about the user's organization, such as ID, role, and title.
type Organizations struct {
	ID        string `json:"id"`
	IsDefault bool   `json:"is_default"`
	Role      string `json:"role"`
	Title     string `json:"title"`
}

// CodexAuthInfo contains authentication-related details specific to Codex.
// This includes ChatGPT account information, subscription status, and user/organization IDs.
type CodexAuthInfo struct {
	ChatgptAccountID               string          `json:"chatgpt_account_id"`
	ChatgptAccountIsFedramp        bool            `json:"chatgpt_account_is_fedramp"`
	ChatgptPlanType                string          `json:"chatgpt_plan_type"`
	ChatgptSubscriptionActiveStart any             `json:"chatgpt_subscription_active_start"`
	ChatgptSubscriptionActiveUntil any             `json:"chatgpt_subscription_active_until"`
	ChatgptSubscriptionLastChecked time.Time       `json:"chatgpt_subscription_last_checked"`
	ChatgptUserID                  string          `json:"chatgpt_user_id"`
	Groups                         []any           `json:"groups"`
	Organizations                  []Organizations `json:"organizations"`
	UserID                         string          `json:"user_id"`
}

// ParseJWTToken parses a JWT token string and extracts its claims without performing
// cryptographic signature verification. This is useful for introspecting the token's
// contents to retrieve user information from an ID token after it has been validated
// by the authentication server.
func ParseJWTToken(token string) (*JWTClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid JWT token format: expected 3 parts, got %d", len(parts))
	}
	for _, part := range parts {
		if part == "" {
			return nil, fmt.Errorf("invalid JWT token format: token parts must be non-empty")
		}
	}

	// Decode the claims (payload) part
	claimsData, err := base64URLDecode(parts[1])
	if err != nil {
		return nil, fmt.Errorf("failed to decode JWT claims: %w", err)
	}
	if !utf8.Valid(claimsData) {
		return nil, fmt.Errorf("failed to unmarshal JWT claims: payload is not valid UTF-8")
	}

	var claims JWTClaims
	if err = json.Unmarshal(claimsData, &claims); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JWT claims: %w", err)
	}

	return &claims, nil
}

// base64URLDecode decodes the unpadded Base64 URL encoding required by JWTs.
func base64URLDecode(data string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(data)
}

// GetUserEmail extracts the user's email address from the JWT claims.
func (c *JWTClaims) GetUserEmail() string {
	if email := strings.TrimSpace(c.Email); email != "" {
		return email
	}
	return strings.TrimSpace(c.Profile.Email)
}

// GetAccountID extracts the user's account ID (subject) from the JWT claims.
// It retrieves the unique identifier for the user's ChatGPT account.
func (c *JWTClaims) GetAccountID() string {
	return strings.TrimSpace(c.CodexAuthInfo.ChatgptAccountID)
}

// GetUserID extracts the ChatGPT user ID, falling back to the generic user ID claim.
func (c *JWTClaims) GetUserID() string {
	if userID := strings.TrimSpace(c.CodexAuthInfo.ChatgptUserID); userID != "" {
		return userID
	}
	return strings.TrimSpace(c.CodexAuthInfo.UserID)
}

// GetPlanType returns the raw ChatGPT plan type claim.
func (c *JWTClaims) GetPlanType() string {
	return strings.TrimSpace(c.CodexAuthInfo.ChatgptPlanType)
}

// IsFedRAMPAccount reports whether the selected ChatGPT account uses FedRAMP routing.
func (c *JWTClaims) IsFedRAMPAccount() bool {
	return c.CodexAuthInfo.ChatgptAccountIsFedramp
}

// AccountIDFromMetadata resolves the Codex account_id from an auth metadata map.
// It returns the explicit account_id when present; otherwise it derives it from
// the id_token JWT claims and backfills the metadata map in place.
// Use this everywhere account_id is read so imported auths that only carry an
// id_token (no top-level account_id) are handled consistently.
func AccountIDFromMetadata(metadata map[string]any) string {
	if metadata == nil {
		return ""
	}
	if accountID, ok := metadata["account_id"].(string); ok {
		if trimmed := strings.TrimSpace(accountID); trimmed != "" {
			return trimmed
		}
	}
	idToken, ok := metadata["id_token"].(string)
	if !ok {
		return ""
	}
	idToken = strings.TrimSpace(idToken)
	if idToken == "" {
		return ""
	}
	claims, err := ParseJWTToken(idToken)
	if err != nil || claims == nil {
		return ""
	}
	accountID := strings.TrimSpace(claims.GetAccountID())
	if accountID == "" {
		return ""
	}
	metadata["account_id"] = accountID
	return accountID
}
