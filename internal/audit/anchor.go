package audit

import (
	"context"
)

// Anchor defines the interface for external timestamp/anchor services that
// record the hash-chain head (seq + self_hash) for third-party attestation.
// v1 ships with a NoopAnchor; future phases may integrate RFC 3161 TSA or
// blockchain anchoring.
type Anchor interface {
	// Push records (seq, selfHash) with an external anchor service.
	// Errors prevent the chain advance from committing.
	Push(ctx context.Context, seq int64, selfHash []byte) error
}

// NoopAnchor is a no-op Anchor for environments where external anchoring
// is not required.
type NoopAnchor struct{}

// Push is a no-op.
func (NoopAnchor) Push(_ context.Context, _ int64, _ []byte) error { return nil }
