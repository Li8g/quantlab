// Package agentauth implements bearer-token authentication for the
// SaaS ↔ Agent WebSocket connection, per docs/saas-ws-protocol-v1.md §4.3.
//
// Tokens have the format `agt_<AgentID>_<base64-secret>` where AgentID is
// a ULID (26 chars) and the secret is 32 random bytes encoded as URL-safe
// base64. Stored AgentToken.TokenHash is bcrypt(secret) cost=12; the
// AgentID portion is non-secret routing metadata that lets Verify look up
// a single row instead of bcrypt-scanning the whole table.
//
// This file (token.go) handles parse/format only — no DB, no bcrypt.
package agentauth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

const (
	// TokenPrefix is the literal namespace marker prefixed to every Agent
	// bearer token. Distinguishes Agent tokens from user JWTs / future
	// token formats in logs and grep.
	TokenPrefix = "agt_"

	// ULIDLength is the canonical Crockford-base32 ULID length.
	ULIDLength = 26

	// SecretBytes is the number of random bytes in the secret half.
	// 32 bytes = 256 bits, well above bcrypt's effective entropy ceiling
	// (72 bytes input; first 56 effectively material at cost ≥ 10).
	SecretBytes = 32
)

// ErrInvalidFormat is returned by ParseToken when the input does not match
// `agt_<ULID>_<base64>`.
var ErrInvalidFormat = errors.New("agentauth: invalid token format")

// ParsedToken is the decomposed view of a token string.
type ParsedToken struct {
	AgentID string // 26-char ULID
	Secret  string // base64-URL of SecretBytes random bytes (no padding)
}

// ParseToken splits a token into its agent_id and secret components and
// validates the structural invariants (prefix, ULID length, base64
// decodes to exactly SecretBytes). It does NOT touch the database or
// bcrypt; callers verify the secret against AgentToken.TokenHash.
func ParseToken(token string) (ParsedToken, error) {
	if !strings.HasPrefix(token, TokenPrefix) {
		return ParsedToken{}, fmt.Errorf("%w: missing prefix", ErrInvalidFormat)
	}
	rest := token[len(TokenPrefix):]

	// rest = <ULID>_<base64>; find the separator AFTER the ULID's fixed
	// width to be unambiguous (base64 url-safe never contains '_').
	if len(rest) < ULIDLength+1 {
		return ParsedToken{}, fmt.Errorf("%w: too short", ErrInvalidFormat)
	}
	if rest[ULIDLength] != '_' {
		return ParsedToken{}, fmt.Errorf("%w: expected '_' after agent_id", ErrInvalidFormat)
	}

	agentID := rest[:ULIDLength]
	secret := rest[ULIDLength+1:]

	if !isValidULID(agentID) {
		return ParsedToken{}, fmt.Errorf("%w: agent_id not a ULID", ErrInvalidFormat)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(secret)
	if err != nil {
		return ParsedToken{}, fmt.Errorf("%w: secret not base64-url: %v", ErrInvalidFormat, err)
	}
	if len(decoded) != SecretBytes {
		return ParsedToken{}, fmt.Errorf("%w: secret decodes to %d bytes, want %d",
			ErrInvalidFormat, len(decoded), SecretBytes)
	}

	return ParsedToken{AgentID: agentID, Secret: secret}, nil
}

// FormatToken assembles a token from its components. Does not validate
// — callers should pass values from NewSecret / store.NewULID.
func FormatToken(agentID, secret string) string {
	return TokenPrefix + agentID + "_" + secret
}

// NewSecret returns SecretBytes of crypto/rand encoded as URL-safe base64
// (no padding). Used by Service.CreateToken; exposed for tests.
func NewSecret() (string, error) {
	buf := make([]byte, SecretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("agentauth: read random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// isValidULID is a quick syntactic check (length + Crockford-base32
// alphabet). Does NOT validate the timestamp range — a syntactically
// valid ULID is enough for routing.
func isValidULID(s string) bool {
	if len(s) != ULIDLength {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		// Crockford alphabet: 0-9, A-Z minus I L O U
		switch {
		case c >= '0' && c <= '9':
		case c >= 'A' && c <= 'Z':
			if c == 'I' || c == 'L' || c == 'O' || c == 'U' {
				return false
			}
		case c >= 'a' && c <= 'z':
			// ULID is upper-case canonical; reject lower-case to keep
			// indexes case-consistent.
			return false
		default:
			return false
		}
	}
	return true
}
