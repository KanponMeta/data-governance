package asset

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kanpon/data-governance/internal/connector"
)

// stubResolver returns errors from Resolve — these tests don't exercise Read/Write,
// only AssetIO.PartitionKey() / construction.
type stubResolver struct{}

func (stubResolver) Resolve(_ string) (connector.Connector, connector.AssetRef, error) {
	return nil, connector.AssetRef{}, errors.New("stub: not used")
}

// TestAssetIOPartitionKeyDefault — non-partitioned runs pass "" and PartitionKey() returns "" (D-09, D-10).
func TestAssetIOPartitionKeyDefault(t *testing.T) {
	a, err := New("foo").Connector("c").Materialize(noopMaterialize).Build()
	require.NoError(t, err)

	io := NewAssetIO(a, stubResolver{}, "")
	require.Equal(t, "", io.PartitionKey())
}

// TestAssetIOPartitionKeySet — partitioned runs receive the partition_key value;
// io.PartitionKey() returns it verbatim (D-10 — partitionKey is per-run input).
func TestAssetIOPartitionKeySet(t *testing.T) {
	a, err := New("foo").Connector("c").Materialize(noopMaterialize).Build()
	require.NoError(t, err)

	io := NewAssetIO(a, stubResolver{}, "2024-01-15")
	require.Equal(t, "2024-01-15", io.PartitionKey())
}
