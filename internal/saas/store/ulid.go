// ulid.go — single source of ULID generation for all Tier 2 business
// IDs (UserID, InstanceID, LotID, ClientOrderID).
//
// MonotonicEntropy makes ULIDs generated within the same millisecond
// monotonically increasing, so B-tree index inserts stay sequential
// even under high write rate (TradeRecord, SpotLot during live trading).
//
// See docs/saas-tier2-schema-v1.md §2.3 (ID 体系, frozen 2026-05-20).
package store

import (
	cryptorand "crypto/rand"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

var (
	ulidMu      sync.Mutex
	ulidEntropy *ulid.MonotonicEntropy
)

func init() {
	ulidEntropy = ulid.Monotonic(cryptorand.Reader, 0)
}

// NewULID returns a 26-char ULID string using the package's
// monotonic-entropy source seeded by crypto/rand. Thread-safe.
func NewULID() string {
	ulidMu.Lock()
	defer ulidMu.Unlock()
	return ulid.MustNew(ulid.Timestamp(time.Now()), ulidEntropy).String()
}
