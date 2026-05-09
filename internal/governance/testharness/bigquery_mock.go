package testharness

import (
	"testing"
)

// BigQueryMock is a test fixture that records BigQuery PolicyTagManagerClient
// calls for testing Phase 5 masking sync (05-02).
// It stores call metadata in memory for assertion during tests.
type BigQueryMock struct {
	t                     *testing.T
	CreateTaxonomyCalls   int
	CreatePolicyTagCalls  int
	SetIamPolicyCalls     int
	GetTaxonomyCalls      int
	GetPolicyTagCalls     int
}

// NewBigQueryMock returns a mock for BigQuery PolicyTagManagerClient operations.
func NewBigQueryMock(t *testing.T) *BigQueryMock {
	t.Helper()
	return &BigQueryMock{t: t}
}

// RecordCreateTaxonomy increments the CreateTaxonomy call counter.
func (m *BigQueryMock) RecordCreateTaxonomy() {
	m.CreateTaxonomyCalls++
}

// RecordCreatePolicyTag increments the CreatePolicyTag call counter.
func (m *BigQueryMock) RecordCreatePolicyTag() {
	m.CreatePolicyTagCalls++
}

// RecordSetIamPolicy increments the SetIamPolicy call counter.
func (m *BigQueryMock) RecordSetIamPolicy() {
	m.SetIamPolicyCalls++
}

// RecordGetTaxonomy increments the GetTaxonomy call counter.
func (m *BigQueryMock) RecordGetTaxonomy() {
	m.GetTaxonomyCalls++
}

// RecordGetPolicyTag increments the GetPolicyTag call counter.
func (m *BigQueryMock) RecordGetPolicyTag() {
	m.GetPolicyTagCalls++
}
