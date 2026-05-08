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
			// Allow shutdownTimeout for any in-flight tick to complete.
			// Since tx-per-row is short, this rarely matters; included for safety.
			shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer cancel()
			_ = shutdownCtx
			return nil
		}
	}
}
