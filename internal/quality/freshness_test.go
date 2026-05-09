package quality_test

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/kanpon/data-governance/internal/event"
	"github.com/kanpon/data-governance/internal/notification"
	"github.com/kanpon/data-governance/internal/quality"
)

// stubQueue captures InsertTx calls.
type stubQueue struct {
	mu     sync.Mutex
	insert []notification.NotificationDispatchArgs
}

func (s *stubQueue) Insert(_ context.Context, args notification.NotificationDispatchArgs) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.insert = append(s.insert, args)
	return nil
}

func (s *stubQueue) InsertTx(_ context.Context, _ *sql.Tx, args notification.NotificationDispatchArgs) error {
	return s.Insert(context.Background(), args)
}

// stubEvents collects Event.Append calls.
type stubEvents struct {
	mu   sync.Mutex
	evts []event.Event
}

func (s *stubEvents) Append(_ context.Context, e event.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evts = append(s.evts, e)
	return nil
}

func (s *stubEvents) Types() []event.EventType {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]event.EventType, len(s.evts))
	for i, e := range s.evts {
		out[i] = e.Type
	}
	return out
}

// TestScanner_NoBreach_WhenLastSucceededRecent — a row with last_succeeded_at
// inside the freshness budget yields zero rows from the SELECT, hence zero
// breaches emitted.
func TestScanner_NoBreach_WhenLastSucceededRecent(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectQuery(`SELECT asset_name, freshness_max_lag_seconds, last_succeeded_at\s+FROM schedules`).
		WillReturnRows(sqlmock.NewRows([]string{"asset_name", "freshness_max_lag_seconds", "last_succeeded_at"}))

	q := &stubQueue{}
	ev := &stubEvents{}
	scanner := quality.NewScanner(db, q, ev)
	n, err := scanner.Scan(context.Background())
	require.NoError(t, err)
	require.Equal(t, 0, n)
	require.Empty(t, ev.Types())
	require.Empty(t, q.insert)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestScanner_Breach_WhenStale verifies emission when one row is returned.
func TestScanner_Breach_WhenStale(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	rows := sqlmock.NewRows([]string{"asset_name", "freshness_max_lag_seconds", "last_succeeded_at"}).
		AddRow("orders", 3600, time.Now().Add(-2*time.Hour))
	mock.ExpectQuery(`SELECT asset_name, freshness_max_lag_seconds, last_succeeded_at\s+FROM schedules`).
		WillReturnRows(rows)
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE schedules SET freshness_breach_emitted_at = NOW\(\) WHERE asset_name = \$1`).
		WithArgs("orders").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	q := &stubQueue{}
	ev := &stubEvents{}
	scanner := quality.NewScanner(db, q, ev)
	n, err := scanner.Scan(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Contains(t, ev.Types(), event.EventTypeSLABreached)
	require.Len(t, q.insert, 1)
	require.Equal(t, "sla.breached", q.insert[0].EventType)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestScanner_NeverRun_BreachAfterCreatedAtPlusMaxLag — a row with NULL
// last_succeeded_at still produces a breach (created_at-based path).
func TestScanner_NeverRun_BreachAfterCreatedAtPlusMaxLag(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	rows := sqlmock.NewRows([]string{"asset_name", "freshness_max_lag_seconds", "last_succeeded_at"}).
		AddRow("never_ran", 60, nil)
	mock.ExpectQuery(`SELECT asset_name, freshness_max_lag_seconds, last_succeeded_at\s+FROM schedules`).
		WillReturnRows(rows)
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE schedules SET freshness_breach_emitted_at = NOW\(\)`).
		WithArgs("never_ran").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	q := &stubQueue{}
	ev := &stubEvents{}
	scanner := quality.NewScanner(db, q, ev)
	n, err := scanner.Scan(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Contains(t, ev.Types(), event.EventTypeSLABreached)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestScanner_DedupBy_FreshnessBreachEmittedAt — the SQL SELECT itself encodes
// the dedup window. We assert the query body contains the dedup predicate.
// The actual dedup behavior is exercised against the live DB via integration
// tests; here we check the SQL contract.
func TestScanner_DedupBy_FreshnessBreachEmittedAt(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectQuery(`freshness_breach_emitted_at IS NULL\s+OR freshness_breach_emitted_at < NOW\(\) - interval`).
		WillReturnRows(sqlmock.NewRows([]string{"asset_name", "freshness_max_lag_seconds", "last_succeeded_at"}))

	q := &stubQueue{}
	ev := &stubEvents{}
	scanner := quality.NewScanner(db, q, ev)
	_, err = scanner.Scan(context.Background())
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestScanner_EmitsSLABreachEvent_AndEnqueuesNotification confirms both side-
// effects fire for one breached row.
func TestScanner_EmitsSLABreachEvent_AndEnqueuesNotification(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	rows := sqlmock.NewRows([]string{"asset_name", "freshness_max_lag_seconds", "last_succeeded_at"}).
		AddRow("orders", 3600, time.Now().Add(-2*time.Hour))
	mock.ExpectQuery(`SELECT asset_name`).WillReturnRows(rows)
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE schedules SET freshness_breach_emitted_at`).
		WithArgs("orders").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	q := &stubQueue{}
	ev := &stubEvents{}
	scanner := quality.NewScanner(db, q, ev)
	n, err := scanner.Scan(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Contains(t, ev.Types(), event.EventTypeSLABreached)
	require.Len(t, q.insert, 1)
}
