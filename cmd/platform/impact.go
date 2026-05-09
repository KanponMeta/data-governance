package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kanpon/data-governance/internal/lineage/impact"
)

// runImpact implements ./platform impact <asset> [--direction=downstream|upstream] [--depth=N]
// [--column=COL] [--as-of=RFC3339] [--format=table|json].
//
// Uses the same positional/flag splitting as runBackfill so the asset name can
// appear anywhere in the argument list (before or after flags).
func runImpact(args []string) error {
	// Split positional vs flag args so the asset name may appear anywhere.
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

	fs := flag.NewFlagSet("impact", flag.ContinueOnError)
	direction := fs.String("direction", "downstream", "upstream or downstream")
	depth := fs.Int("depth", impact.DefaultDepth, "traversal depth (max 25)")
	column := fs.String("column", "", "optional column-level traversal")
	asOfStr := fs.String("as-of", "", "RFC3339 timestamp for point-in-time query")
	format := fs.String("format", "table", "table or json")

	fs.Usage = func() {
		fmt.Fprintln(os.Stderr,
			"usage: ./platform impact <asset> [--direction=downstream|upstream] [--depth=N] [--column=COL] [--as-of=RFC3339] [--format=table|json]")
		fs.PrintDefaults()
	}

	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	if len(positional) < 1 {
		return fmt.Errorf(
			"usage: ./platform impact <asset> [--direction=downstream|upstream] [--depth=N] [--column=COL] [--as-of=RFC3339] [--format=table|json]",
		)
	}
	assetName := positional[0]

	// Validate depth before any DB access — ErrDepthExceeded check runs here
	// (D-14 layer 1 of 3). This must happen before DATABASE_URL check so the
	// test TestRunImpact_DepthExceeded can validate the error without a DB.
	if *depth > impact.MaxDepth {
		return fmt.Errorf("depth %d exceeds hard cap of 25 (D-14)", *depth)
	}

	if *direction != "upstream" && *direction != "downstream" {
		return fmt.Errorf("--direction must be upstream or downstream (got %q)", *direction)
	}

	if *format != "table" && *format != "json" {
		return fmt.Errorf("--format must be table or json (got %q)", *format)
	}

	var asOf *time.Time
	if *asOfStr != "" {
		t, err := time.Parse(time.RFC3339, *asOfStr)
		if err != nil {
			return fmt.Errorf("--as-of must be RFC3339: %w", err)
		}
		asOf = &t
	}

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return fmt.Errorf("impact: DATABASE_URL is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("impact: open pgx pool: %w", err)
	}
	defer pool.Close()

	q := impact.ImpactQuery{
		Asset:     assetName,
		Direction: *direction,
		Depth:     *depth,
		AsOf:      asOf,
	}
	if *column != "" {
		q.Column = column
	}

	result, err := impact.Analyze(ctx, pool, q)
	if err != nil {
		if errors.Is(err, impact.ErrDepthExceeded) {
			return fmt.Errorf("depth %d exceeds hard cap of 25 (D-14)", *depth)
		}
		return fmt.Errorf("impact: analyze: %w", err)
	}

	switch *format {
	case "json":
		return json.NewEncoder(os.Stdout).Encode(result)
	case "table":
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "DEPTH\tASSET\tCOLUMN")
		for _, n := range result.Nodes {
			col := ""
			if n.Column != nil {
				col = *n.Column
			}
			fmt.Fprintf(tw, "%d\t%s\t%s\n", n.Depth, n.Asset, col)
		}
		return tw.Flush()
	}
	// Unreachable: format validated above.
	return fmt.Errorf("--format must be table or json (got %q)", *format)
}
