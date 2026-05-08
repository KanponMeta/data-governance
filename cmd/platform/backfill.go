package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/kanpon/data-governance/internal/asset"
	"github.com/kanpon/data-governance/internal/backfill"
	"github.com/kanpon/data-governance/internal/event"
	"github.com/kanpon/data-governance/internal/storage"
)

// runBackfill is the body of:
//
//	./platform backfill <asset> --partitions=<spec> [--priority=...] [--max-partitions=N]
//
// D-14 / D-15: it parses the spec into a list of partition keys, validates
// against --max-partitions (Pitfall 6), and mass-enqueues N runs with
// priority='backfill', creating a backfills row that ties them together.
//
// Priority validation happens at CLI parse time (defense-in-depth — the DB
// CHECK constraint and Submit() validation also reject unknown values).
//
// Argument ordering: we accept the asset positional anywhere in the arg list
// (before, after, or between flags). Go's stdlib `flag.Parse` stops at the
// first non-flag token, which would force operators to type
// `backfill --partitions=... <asset>` instead of the more natural
// `backfill <asset> --partitions=...`. We extract any non-flag token as the
// asset name and pass the remainder through to FlagSet.Parse.
func runBackfill(args []string) error {
	// Split positional vs flag args so the asset name may appear anywhere.
	// Anything that does NOT start with "-" is treated as a positional.
	var (
		positional []string
		flagArgs   []string
	)
	for _, a := range args {
		if len(a) > 0 && a[0] == '-' {
			flagArgs = append(flagArgs, a)
		} else {
			positional = append(positional, a)
		}
	}

	fs := flag.NewFlagSet("backfill", flag.ContinueOnError)
	partitionsFlag := fs.String("partitions", "",
		"Date range (2024-01-01:2024-12-31), comma list (us,eu), or single key (2024-01-15)")
	priorityFlag := fs.String("priority", "backfill",
		"Run priority: critical | normal | backfill")
	maxPartitionsFlag := fs.Int("max-partitions", backfill.DefaultMaxPartitions,
		"Reject specs that expand to more than N partitions (Pitfall 6 row-count blowup guard)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr,
			"usage: backfill <asset> --partitions=<spec> [--priority=backfill] [--max-partitions=3650]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	if len(positional) < 1 {
		return errors.New("usage: backfill <asset> --partitions=<spec> [--priority=backfill] [--max-partitions=3650]")
	}
	assetName := positional[0]
	// Validate priority FIRST so an obviously bad CLI invocation surfaces
	// the most specific error (T-03-07-03 — operator gets a useful message,
	// not a generic CHECK constraint violation from the DB).
	if _, ok := backfill.ValidPriorities[*priorityFlag]; !ok {
		return fmt.Errorf("backfill: invalid --priority %q (must be critical|normal|backfill)", *priorityFlag)
	}
	if *partitionsFlag == "" {
		return errors.New("backfill: --partitions is required")
	}
	if *maxPartitionsFlag <= 0 {
		return fmt.Errorf("backfill: --max-partitions must be > 0 (got %d)", *maxPartitionsFlag)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("backfill: DATABASE_URL is required")
	}
	store, err := storage.NewPostgres(ctx, dsn)
	if err != nil {
		return err
	}
	defer store.Close()

	a, err := asset.Default().Get(assetName)
	if err != nil || a == nil {
		return fmt.Errorf("backfill: asset %q not registered", assetName)
	}
	strategy := a.Partitions()
	if strategy == nil {
		return fmt.Errorf("backfill: asset %q has no .Partitions(...) strategy declared", assetName)
	}

	spec, err := backfill.ParsePartitionSpec(strategy, *partitionsFlag, *maxPartitionsFlag)
	if err != nil {
		return err
	}
	spec.Priority = *priorityFlag

	events := event.NewWriter(store)
	id, err := backfill.Submit(ctx, store, events, assetName, spec)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "backfill_id: %s\n", id)
	fmt.Fprintf(os.Stdout, "submitted %d partitions for asset %q (priority=%s, source=%q)\n",
		len(spec.Keys), assetName, spec.Priority, spec.Source)
	return nil
}

// runBackfillStatus is the body of:
//
//	./platform backfill status <backfill_id>
//
// Pretty-prints the aggregated run-state counts for the given backfill_id.
// State counts are printed in alphabetical order so the CLI output is
// deterministic across invocations.
func runBackfillStatus(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: backfill status <backfill_id>")
	}
	id, err := uuid.Parse(args[0])
	if err != nil {
		return fmt.Errorf("backfill status: invalid UUID %q: %w", args[0], err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("backfill status: DATABASE_URL is required")
	}
	store, err := storage.NewPostgres(ctx, dsn)
	if err != nil {
		return err
	}
	defer store.Close()

	s, err := backfill.GetStatus(ctx, store.DB(), id)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Backfill %s (asset %q)\n", s.BackfillID, s.AssetName)
	fmt.Fprintf(os.Stdout, "  Spec:        %s\n", s.PartitionSpec)
	fmt.Fprintf(os.Stdout, "  Total:       %d partitions\n", s.TotalPartitions)
	fmt.Fprintf(os.Stdout, "  Submitted:   %s\n", s.SubmittedAt.Format(time.RFC3339))
	if s.CompletedAt != nil {
		fmt.Fprintf(os.Stdout, "  Completed:   %s\n", s.CompletedAt.Format(time.RFC3339))
	}
	keys := make([]string, 0, len(s.StateCounts))
	for k := range s.StateCounts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(os.Stdout, "  %-12s %d\n", k+":", s.StateCounts[k])
	}
	return nil
}
