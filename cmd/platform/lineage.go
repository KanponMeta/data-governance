package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/kanpon/data-governance/internal/lineage/openlineage"
	entpkg "github.com/kanpon/data-governance/internal/storage/ent"
)

// dispatchLineage routes ./platform lineage <subcommand>.
func dispatchLineage(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: ./platform lineage {export} ...")
	}
	switch args[0] {
	case "export":
		return runLineageExport(args[1:])
	default:
		return fmt.Errorf("unknown lineage subcommand: %s (try export)", args[0])
	}
}

// runLineageExport implements ./platform lineage export --asset=<asset> [--since=RFC3339] [--format=openlineage] [--namespace=platform].
// D-18: exports stored lineage rows as OpenLineage RunEvent JSON array to stdout.
func runLineageExport(args []string) error {
	fs := flag.NewFlagSet("lineage export", flag.ContinueOnError)
	assetName := fs.String("asset", "", "asset name (required)")
	sinceStr := fs.String("since", "", "RFC3339 timestamp; if omitted, all succeeded runs are exported")
	format := fs.String("format", "openlineage", "output format: only openlineage is supported (D-18)")
	namespace := fs.String("namespace", "platform", "OpenLineage namespace")

	fs.Usage = func() {
		fmt.Fprintln(os.Stderr,
			"usage: ./platform lineage export --asset=NAME [--since=RFC3339] [--format=openlineage] [--namespace=platform]")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return err
	}
	if *assetName == "" {
		return fmt.Errorf("usage: ./platform lineage export --asset=NAME [--since=RFC3339] [--format=openlineage]")
	}
	if *format != "openlineage" {
		return fmt.Errorf("unsupported_format: only openlineage is supported (got %q)", *format)
	}

	var since time.Time
	if *sinceStr != "" {
		t, err := time.Parse(time.RFC3339, *sinceStr)
		if err != nil {
			return fmt.Errorf("--since must be RFC3339: %w", err)
		}
		since = t
	}

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return fmt.Errorf("lineage export: DATABASE_URL is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	entClient, err := entpkg.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("lineage export: open ent client: %w", err)
	}
	defer entClient.Close()

	translator := openlineage.NewDefault(entClient, *namespace)
	events, err := translator.TranslateAsset(ctx, *assetName, since)
	if err != nil {
		return fmt.Errorf("lineage export: translate: %w", err)
	}

	return json.NewEncoder(os.Stdout).Encode(events)
}
