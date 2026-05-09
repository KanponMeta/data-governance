// Package quality implements Phase 5 data quality rule evaluation, freshness
// SLA scanning, and the dispatcher that routes quality / SLA events to the
// notification subsystem (Plan 05-05 D-18..D-21).
//
// Architecture:
//
//	asset.QualityRule (user-defined)
//	    │
//	    ▼
//	quality.Evaluator (per-rule timeout, persists quality_results, emits events)
//	    │  ┌───────────────────────────┐
//	    └─►│ connector.QueryAggregate │ — strict ctx timeout (Pitfall #10)
//	       └───────────────────────────┘
//
// Rule definitions live in internal/asset/types.go so user code only imports
// "asset". This file re-exports the names so test code referencing
// quality.NullCheck etc. still works.
package quality

import "github.com/kanpon/data-governance/internal/asset"

// Re-exports of asset.* quality types so callers can import a single package.
type (
	// Rule is asset.QualityRule.
	Rule = asset.QualityRule
	// Evaluator interface used by rules to issue queries.
	RuleEvaluator = asset.QualityEvaluator
	// Result is asset.QualityResult.
	Result = asset.QualityResult
)

// Re-exports of concrete rule structs.
type (
	NullCheck    = asset.NullCheck
	RangeCheck   = asset.RangeCheck
	SQLAssertion = asset.SQLAssertion
)

// Re-exports of predicate types.
type (
	AssertionPredicate = asset.AssertionPredicate
	ScalarEqualsZero   = asset.ScalarEqualsZero
	ScalarLessThan     = asset.ScalarLessThan
	RowCountIsZero     = asset.RowCountIsZero
)
