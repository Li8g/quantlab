package agentauth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"quantlab/internal/saas/store"
)

// fakeStore is an in-memory TokenStore. NOT thread-safe across goroutines
// in production, but agentauth callers are not concurrent within a single
// auth event, so the mutex is for test sanity.
type fakeStore struct {
	mu   sync.Mutex
	rows map[string]*store.AgentToken // keyed by AgentID
}

func newFakeStore() *fakeStore {
	return &fakeStore{rows: make(map[string]*store.AgentToken)}
}

func (f *fakeStore) GetByAgentID(_ context.Context, agentID string) (*store.AgentToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[agentID]
	if !ok {
		return nil, nil
	}
	// Return a copy so the caller can't mutate fake state.
	cp := *row
	return &cp, nil
}

func (f *fakeStore) Create(_ context.Context, row *store.AgentToken) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.rows[row.AgentID]; ok {
		return errors.New("fakeStore: duplicate agent_id")
	}
	cp := *row
	f.rows[row.AgentID] = &cp
	return nil
}

func (f *fakeStore) Revoke(_ context.Context, agentID string, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[agentID]
	if !ok {
		return nil // idempotent: revoking unknown is silent
	}
	if row.RevokedAt != nil {
		return nil // already revoked, idempotent
	}
	row.RevokedAt = &now
	return nil
}

func (f *fakeStore) TouchLastSeen(_ context.Context, agentID string, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[agentID]
	if !ok {
		return nil
	}
	row.LastSeenAt = &now
	return nil
}

// newSvc wires a fakeStore + bcrypt.MinCost (fast tests). Returns the
// service, the fake store, and a fixed-AgentID generator helper so the
// test can predict the AgentID embedded in the issued token.
func newSvc(t *testing.T, fixedAgentID string) (*Service, *fakeStore) {
	t.Helper()
	fs := newFakeStore()
	svc := NewService(fs).WithBcryptCost(bcrypt.MinCost)
	if fixedAgentID != "" {
		svc = svc.WithULID(func() string { return fixedAgentID })
	}
	return svc, fs
}

func TestService_CreateThenVerify_HappyPath(t *testing.T) {
	ctx := context.Background()
	svc, fs := newSvc(t, "01HKQ8XYZ0PQRS9TVWX0YZAB12")

	created, err := svc.CreateToken(ctx, "01HKACCT00000000000000000A", "macbook")
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if created.AgentID != "01HKQ8XYZ0PQRS9TVWX0YZAB12" {
		t.Errorf("created.AgentID = %q", created.AgentID)
	}
	if got := fs.rows["01HKQ8XYZ0PQRS9TVWX0YZAB12"]; got == nil {
		t.Fatalf("fakeStore missing row")
	} else if got.AccountID != "01HKACCT00000000000000000A" {
		t.Errorf("AccountID = %q", got.AccountID)
	}

	v, err := svc.Verify(ctx, created.Plaintext, time.Unix(1700000000, 0))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if v.AgentID != "01HKQ8XYZ0PQRS9TVWX0YZAB12" || v.AccountID != "01HKACCT00000000000000000A" {
		t.Errorf("Verified = %+v", v)
	}

	// LastSeenAt should be touched.
	row := fs.rows["01HKQ8XYZ0PQRS9TVWX0YZAB12"]
	if row.LastSeenAt == nil {
		t.Errorf("LastSeenAt not touched")
	} else if row.LastSeenAt.Unix() != 1700000000 {
		t.Errorf("LastSeenAt = %v", row.LastSeenAt)
	}
}

func TestService_Verify_InvalidFormat(t *testing.T) {
	svc, _ := newSvc(t, "")
	_, err := svc.Verify(context.Background(), "not-a-token", time.Now())
	if !errors.Is(err, ErrInvalidFormat) {
		t.Fatalf("got %v, want ErrInvalidFormat", err)
	}
}

func TestService_Verify_UnknownAgent(t *testing.T) {
	svc, _ := newSvc(t, "")
	secret, _ := NewSecret()
	stranger := FormatToken("01HKQ8STRANGER000000000000", secret)
	_, err := svc.Verify(context.Background(), stranger, time.Now())
	if !errors.Is(err, ErrUnknownAgent) {
		t.Fatalf("got %v, want ErrUnknownAgent", err)
	}
}

func TestService_Verify_WrongSecret(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t, "01HKQ8XYZ0PQRS9TVWX0YZAB12")
	created, err := svc.CreateToken(ctx, "01HKACCT00000000000000000A", "x")
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	// Swap secret half with a different one (same AgentID prefix).
	wrongSecret, _ := NewSecret()
	tampered := FormatToken(created.AgentID, wrongSecret)
	if tampered == created.Plaintext {
		t.Fatal("test bug: wrongSecret happens to equal real secret")
	}
	_, err = svc.Verify(ctx, tampered, time.Now())
	if !errors.Is(err, ErrBadSecret) {
		t.Fatalf("got %v, want ErrBadSecret", err)
	}
}

func TestService_Verify_Revoked(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t, "01HKQ8XYZ0PQRS9TVWX0YZAB12")
	created, err := svc.CreateToken(ctx, "01HKACCT00000000000000000A", "x")
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if err := svc.Revoke(ctx, created.AgentID, time.Now()); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	_, err = svc.Verify(ctx, created.Plaintext, time.Now())
	if !errors.Is(err, ErrRevoked) {
		t.Fatalf("got %v, want ErrRevoked", err)
	}
}

func TestService_Revoke_Idempotent(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t, "01HKQ8XYZ0PQRS9TVWX0YZAB12")
	created, err := svc.CreateToken(ctx, "01HKACCT00000000000000000A", "x")
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	now := time.Unix(1700000000, 0)
	if err := svc.Revoke(ctx, created.AgentID, now); err != nil {
		t.Fatalf("Revoke 1: %v", err)
	}
	// Second revoke should be a silent no-op, not error and not bump time.
	later := time.Unix(1700001000, 0)
	if err := svc.Revoke(ctx, created.AgentID, later); err != nil {
		t.Fatalf("Revoke 2: %v", err)
	}
}

func TestService_Revoke_UnknownAgent(t *testing.T) {
	svc, _ := newSvc(t, "")
	if err := svc.Revoke(context.Background(), "01HKQ8GHOST00000000000000A", time.Now()); err != nil {
		t.Fatalf("Revoke unknown: %v", err)
	}
}

func TestService_CreateToken_RejectsEmptyAccount(t *testing.T) {
	svc, _ := newSvc(t, "")
	if _, err := svc.CreateToken(context.Background(), "", ""); err == nil {
		t.Fatal("CreateToken with empty accountID should error")
	}
}

// TestService_PlaintextNotPersisted asserts that the bcrypt hash in the
// stored row is NOT the plaintext secret — a regression guard against
// accidentally swapping the hash field with the plaintext.
func TestService_PlaintextNotPersisted(t *testing.T) {
	ctx := context.Background()
	svc, fs := newSvc(t, "01HKQ8XYZ0PQRS9TVWX0YZAB12")
	created, err := svc.CreateToken(ctx, "01HKACCT00000000000000000A", "x")
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	row := fs.rows["01HKQ8XYZ0PQRS9TVWX0YZAB12"]
	if row.TokenHash == created.Plaintext {
		t.Fatal("TokenHash equals plaintext — bcrypt skipped")
	}
	parsed, _ := ParseToken(created.Plaintext)
	if row.TokenHash == parsed.Secret {
		t.Fatal("TokenHash equals raw secret — bcrypt skipped")
	}
	// And it should be a valid bcrypt hash.
	if _, err := bcrypt.Cost([]byte(row.TokenHash)); err != nil {
		t.Errorf("TokenHash not a valid bcrypt hash: %v", err)
	}
}
