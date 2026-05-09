package audit

import (
	"encoding/json"
	"fmt"

	"github.com/gowebpki/jcs"
)

// CanonicalJSON returns the IETF RFC 8785 JCS (JSON Canonicalization Scheme)
// bytes for v. JCS guarantees bit-identical serialization across marshallers
// and call sequences, which is required for deterministic hash computation.
func CanonicalJSON(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("audit: marshal: %w", err)
	}
	return jcs.Transform(raw)
}
