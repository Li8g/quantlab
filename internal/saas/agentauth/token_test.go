package agentauth

import (
	"errors"
	"strings"
	"testing"
)

func TestParseToken_HappyPath(t *testing.T) {
	secret, err := NewSecret()
	if err != nil {
		t.Fatalf("NewSecret: %v", err)
	}
	agentID := "01HKQ8XYZ0PQRS9TVWX0YZAB12" // 26 chars Crockford
	token := FormatToken(agentID, secret)

	got, err := ParseToken(token)
	if err != nil {
		t.Fatalf("ParseToken: %v", err)
	}
	if got.AgentID != agentID {
		t.Errorf("agent_id: got %q, want %q", got.AgentID, agentID)
	}
	if got.Secret != secret {
		t.Errorf("secret: got %q, want %q", got.Secret, secret)
	}
}

func TestParseToken_RejectsMissingPrefix(t *testing.T) {
	if _, err := ParseToken("01HKQ8XYZ0PQRS9TVWX0YZAB12_abc"); !errors.Is(err, ErrInvalidFormat) {
		t.Fatalf("got %v, want ErrInvalidFormat", err)
	}
}

func TestParseToken_RejectsTooShort(t *testing.T) {
	if _, err := ParseToken("agt_short"); !errors.Is(err, ErrInvalidFormat) {
		t.Fatalf("got %v, want ErrInvalidFormat", err)
	}
}

func TestParseToken_RejectsMissingSeparator(t *testing.T) {
	// 26 chars after prefix but no separator
	bad := "agt_" + strings.Repeat("0", 26) + "X"
	if _, err := ParseToken(bad); !errors.Is(err, ErrInvalidFormat) {
		t.Fatalf("got %v, want ErrInvalidFormat", err)
	}
}

func TestParseToken_RejectsBadULID(t *testing.T) {
	secret, _ := NewSecret()
	bad := "agt_iiiiiiiiiiiiiiiiiiiiiiiii_" + secret // lowercase rejected
	if _, err := ParseToken(bad); !errors.Is(err, ErrInvalidFormat) {
		t.Fatalf("got %v, want ErrInvalidFormat", err)
	}
}

func TestParseToken_RejectsBadBase64(t *testing.T) {
	agentID := "01HKQ8XYZ0PQRS9TVWX0YZAB12"
	bad := FormatToken(agentID, "!!!not-base64!!!")
	if _, err := ParseToken(bad); !errors.Is(err, ErrInvalidFormat) {
		t.Fatalf("got %v, want ErrInvalidFormat", err)
	}
}

func TestParseToken_RejectsWrongSecretLength(t *testing.T) {
	agentID := "01HKQ8XYZ0PQRS9TVWX0YZAB12"
	// 8 bytes raw → 11 chars base64-url no-padding; far short of 32 bytes
	bad := FormatToken(agentID, "AAAAAAAAAAA")
	if _, err := ParseToken(bad); !errors.Is(err, ErrInvalidFormat) {
		t.Fatalf("got %v, want ErrInvalidFormat", err)
	}
}

func TestNewSecretYieldsExpectedLength(t *testing.T) {
	s, err := NewSecret()
	if err != nil {
		t.Fatalf("NewSecret: %v", err)
	}
	// base64 raw url-no-padding of 32 bytes → ceil(32*4/3)=43 chars.
	if len(s) != 43 {
		t.Errorf("NewSecret length = %d, want 43", len(s))
	}
}

func TestIsValidULID_RejectsForbiddenLetters(t *testing.T) {
	// Crockford-base32 excludes I, L, O, U.
	for _, c := range []rune{'I', 'L', 'O', 'U'} {
		bad := strings.Repeat("0", 25) + string(c)
		if isValidULID(bad) {
			t.Errorf("isValidULID(%q) = true, want false", bad)
		}
	}
}
