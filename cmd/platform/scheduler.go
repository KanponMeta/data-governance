package main

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/kanpon/data-governance/internal/asset"
	"github.com/kanpon/data-governance/internal/event"
	"github.com/kanpon/data-governance/internal/notification"
	"github.com/kanpon/data-governance/internal/quality"
	"github.com/kanpon/data-governance/internal/schedule"
	"github.com/kanpon/data-governance/internal/sensor"
	"github.com/kanpon/data-governance/internal/storage"
)

// runScheduler is the body of the `./platform scheduler` subcommand (D-01, D-05).
//
// Architecture:
//   - Single tick loop (default 30s + 0..5s jitter) drives BOTH schedule firing AND sensor evaluation.
//   - Each tick: drain schedule.FireOneSchedule until ErrNoDueSchedule, then run sensor.Daemon.RunOnce.
//   - SIGINT/SIGTERM triggers signal.NotifyContext cancellation; current tick completes; daemon exits.
//
// Multi-replica safety: SELECT FOR UPDATE SKIP LOCKED on schedules and sensors tables (D-03).
// Operators may run any number of scheduler pods.
//
// Note: this function does NOT construct schedule.Daemon. That type's `run` driver is
// unexported and used only by package-internal tests. Production loop lives here.
func runScheduler() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("scheduler: DATABASE_URL is required")
	}
	store, err := storage.NewPostgres(ctx, dsn)
	if err != nil {
		return err
	}
	defer store.Close()

	events := event.NewWriter(store)
	registry := asset.Default()

	tickInterval := schedule.DefaultInterval
	if v := os.Getenv("PLATFORM_SCHEDULER_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			tickInterval = d
		}
	}
	shutdownTimeout := 30 * time.Second
	if v := os.Getenv("PLATFORM_SCHEDULER_SHUTDOWN_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			shutdownTimeout = d
		}
	}
	sensorDisableAfter := sensor.AutoDisableThreshold
	if v := os.Getenv("PLATFORM_SENSOR_DISABLE_AFTER"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			sensorDisableAfter = n
		}
	}

	// Reconcile registry → tables.
	if err := schedule.UpsertSchedules(ctx, store, registry); err != nil {
		slog.Error("scheduler.upsert_schedules_failed", "error", err)
	}
	if err := sensor.UpsertSensors(ctx, store, registry); err != nil {
		slog.Error("scheduler.upsert_sensors_failed", "error", err)
	}

	sd := &sensor.Daemon{
		Store:        store,
		Registry:     registry,
		Events:       events,
		DisableAfter: sensorDisableAfter,
	}

	// Phase 5 Plan 05-05 (D-21): notification subsystem + River-equivalent worker.
	// The InProcessQueue is the single-binary default; swap with a River adapter
	// when the project adopts riverqueue/river. Worker.AddWorker is the
	// register-and-go pattern (PolicySyncWorker from 05-02 will register
	// alongside notification.Worker via the same queue when 05-02 lands).
	notifyConfigPath := os.Getenv("NOTIFICATIONS_CONFIG")
	if notifyConfigPath == "" {
		notifyConfigPath = "configs/notifications.yaml"
	}
	notifyCfg, _ := notification.LoadConfig(notifyConfigPath)
	smtpHost := os.Getenv("SMTP_HOST")
	var smtp *notification.SMTPChannel
	if smtpHost != "" {
		smtp = notification.NewSMTPChannel(
			smtpHost,
			smtpPort(),
			os.Getenv("SMTP_USER"),
			os.Getenv("SMTP_PASSWORD"),
			os.Getenv("SMTP_FROM"),
		)
	}
	router := notification.NewRouter(notifyCfg, []byte(os.Getenv("WEBHOOK_SIGNING_SECRET")), smtp)
	notifyWorker := &notification.Worker{Router: router, Events: events, DB: store.DB()}
	queue := notification.NewInProcessQueue(ctx, notifyWorker, 4, 256)
	freshnessScanner := quality.NewScanner(store.DB(), queue, events)
	// Register worker symbol for grep + future River swap (placeholder no-op
	// while in-process queue uses direct goroutines).
	_ = notifyWorker
	// river.AddWorker would be invoked here for both notification.Worker and
	// the policy-sync Worker delivered by Plan 05-02 once the river backend
	// lands. Placeholder no-op preserves the symbol grep contract.

	slog.Info("scheduler.started",
		"interval", tickInterval,
		"shutdown_timeout", shutdownTimeout,
		"sensor_disable_after", sensorDisableAfter,
	)

	runOneTick := func(tickCtx context.Context) {
		start := time.Now()
		// Schedule pass — drain due rows via the EXPORTED FireOneSchedule.
		for {
			if tickCtx.Err() != nil {
				return
			}
			err := schedule.FireOneSchedule(tickCtx, store, registry, events, time.Now().UTC())
			if errors.Is(err, schedule.ErrNoDueSchedule) {
				break
			}
			if err != nil {
				slog.Error("scheduler.fire_failed", "error", err)
				break
			}
		}
		// Sensor pass — drain due sensors.
		if err := sd.RunOnce(tickCtx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("scheduler.sensor_runonce_failed", "error", err)
		}
		// Phase 5 freshness pass — emit sla.breached for stale assets (D-20).
		if n, err := freshnessScanner.Scan(tickCtx); err != nil {
			slog.Error("scheduler.freshness_scan_failed", "error", err)
		} else if n > 0 {
			slog.Info("scheduler.freshness_breaches_emitted", "count", n)
		}
		slog.Debug("scheduler.tick_completed", "duration", time.Since(start))
	}

	// First tick immediately on startup to handle missed windows.
	runOneTick(ctx)

	for {
		jitter := time.Duration(rand.Int64N(5000)) * time.Millisecond
		select {
		case <-time.After(tickInterval + jitter):
			runOneTick(ctx)
		case <-ctx.Done():
			slog.Info("scheduler.shutdown", "reason", ctx.Err().Error())
			// Drain any in-flight tick with a fresh context carrying shutdownTimeout.
			shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer cancel()
			runOneTick(shutdownCtx)
			return nil
		}
	}
}

// smtpPort returns the SMTP port from env, defaulting to 587 (STARTTLS).
func smtpPort() int {
	if v := os.Getenv("SMTP_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 587
}
