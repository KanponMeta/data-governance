package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/google/uuid"

	"github.com/kanpon/data-governance/internal/config"
	"github.com/kanpon/data-governance/internal/connector"
	"github.com/kanpon/data-governance/internal/platform"
	"github.com/kanpon/data-governance/internal/policy"
)

// init self-registers the policy subcommand via the platform registry (B-03 fix).
// Dispatched from cmd/platform/main.go's "policy" case via DispatchCommand.
func init() {
	platform.RegisterCommand("policy", dispatchPolicy)
}

// dispatchPolicy is the entry point for `./platform policy <subcommand>`.
//   show  <asset>                                 — print effective policies for an asset
//   list  <asset> [--source=...]                  — list active rows; optional source filter
//   patch <asset> <column> --mask=... --reason=...— runtime override
//   yaml-reload --config=<path>                   — reload tag→mask defaults
func dispatchPolicy(args []string) int {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "Usage: platform policy [show|list|patch|yaml-reload]\n")
		return 2
	}
	switch args[0] {
	case "show":
		return policyShowCmd(args[1:])
	case "list":
		return policyListCmd(args[1:])
	case "patch":
		return policyPatchCmd(args[1:])
	case "yaml-reload":
		return policyYAMLReloadCmd(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown policy subcommand: %s\n", args[0])
		return 2
	}
}

// openPolicyDB opens a *sql.DB to the platform metadata store using the
// shared config loader. Returned cleanup is always safe to defer.
func openPolicyDB() (*sql.DB, func(), error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, func() {}, fmt.Errorf("load config: %w", err)
	}
	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		return nil, func() {}, fmt.Errorf("open db: %w", err)
	}
	cleanup := func() { _ = db.Close() }
	return db, cleanup, nil
}

// policyShowCmd prints all active policies for an asset.
//
//	platform policy show <asset>
func policyShowCmd(args []string) int {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "Usage: platform policy show <asset>\n")
		return 2
	}
	assetName := args[0]
	db, cleanup, err := openPolicyDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "policy show: %v\n", err)
		return 1
	}
	defer cleanup()
	store := policy.NewStore(db, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rows, err := store.List(ctx, assetName, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "policy show: %v\n", err)
		return 1
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "COLUMN\tSOURCE\tMASK\tALLOW_ROLES\tENFORCEMENT\tSYNC")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			r.Column, r.Source, string(r.Mask), strings.Join(r.AllowRoles, ","),
			r.EnforcementMode, r.SyncStatus)
	}
	if err := tw.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "policy show: flush: %v\n", err)
		return 1
	}
	return 0
}

// policyListCmd lists active policies — with optional --source filter and
// --asset filter (defaults: all assets, all sources).
func policyListCmd(args []string) int {
	fs := flag.NewFlagSet("policy list", flag.ContinueOnError)
	source := fs.String("source", "", "filter by source: builder|runtime|yaml-default")
	asset := fs.String("asset", "", "filter by asset name")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	db, cleanup, err := openPolicyDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "policy list: %v\n", err)
		return 1
	}
	defer cleanup()
	store := policy.NewStore(db, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var assetsToScan []string
	if *asset != "" {
		assetsToScan = []string{*asset}
	} else {
		all, err := store.ListAllAssets(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "policy list: %v\n", err)
			return 1
		}
		assetsToScan = all
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ASSET\tCOLUMN\tSOURCE\tMASK\tALLOW_ROLES\tENFORCEMENT")
	for _, a := range assetsToScan {
		rows, err := store.List(ctx, a, *source)
		if err != nil {
			fmt.Fprintf(os.Stderr, "policy list (%s): %v\n", a, err)
			return 1
		}
		for _, r := range rows {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
				r.Asset, r.Column, r.Source, string(r.Mask),
				strings.Join(r.AllowRoles, ","), r.EnforcementMode)
		}
	}
	tw.Flush()
	return 0
}

// policyPatchCmd applies a runtime override.
//
//	platform policy patch <asset> <column> --mask=hash --roles=a,b --reason="..."
func policyPatchCmd(args []string) int {
	fs := flag.NewFlagSet("policy patch", flag.ContinueOnError)
	mask := fs.String("mask", "", "mask type: hash|redact|partial (required)")
	rolesCSV := fs.String("roles", "", "comma-separated allow_roles")
	reason := fs.String("reason", "", "non-empty reason for the runtime override (required)")
	actorRaw := fs.String("actor", "", "uuid of the requesting principal (defaults to nil)")
	// Allow positional <asset> <column> before the flags.
	if len(args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: platform policy patch <asset> <column> --mask=... --reason=\"...\" [--roles=...]\n")
		return 2
	}
	assetName := args[0]
	column := args[1]
	if err := fs.Parse(args[2:]); err != nil {
		return 2
	}
	if *reason == "" {
		fmt.Fprintf(os.Stderr, "policy patch: --reason is required\n")
		return 2
	}
	mt := connector.MaskType(*mask)
	if !mt.IsValid() {
		fmt.Fprintf(os.Stderr, "policy patch: invalid --mask=%q (want hash|redact|partial)\n", *mask)
		return 2
	}
	var roles []string
	if *rolesCSV != "" {
		roles = strings.Split(*rolesCSV, ",")
	}
	actor := uuid.Nil
	if *actorRaw != "" {
		if u, err := uuid.Parse(*actorRaw); err == nil {
			actor = u
		}
	}

	db, cleanup, err := openPolicyDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "policy patch: %v\n", err)
		return 1
	}
	defer cleanup()
	store := policy.NewStore(db, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	eff, err := store.Patch(ctx, actor, assetName, column, mt, roles, *reason)
	if err != nil {
		fmt.Fprintf(os.Stderr, "policy patch: %v\n", err)
		return 1
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(map[string]any{
		"asset":            eff.Asset,
		"column":           eff.Column,
		"mask":             string(eff.Mask),
		"allow_roles":      eff.AllowRoles,
		"source":           eff.Source,
		"enforcement_mode": eff.EnforcementMode,
		"reason":           eff.Reason,
	})
	return 0
}

// policyYAMLReloadCmd reads a policies.yaml file and re-applies tag → mask
// defaults across asset_metadata.
//
//	platform policy yaml-reload --config=configs/policies.yaml
func policyYAMLReloadCmd(args []string) int {
	fs := flag.NewFlagSet("policy yaml-reload", flag.ContinueOnError)
	cfgPath := fs.String("config", "configs/policies.yaml", "path to policies.yaml")
	actorRaw := fs.String("actor", "", "uuid of the requesting principal")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := policy.LoadYAML(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "policy yaml-reload: %v\n", err)
		return 1
	}

	db, cleanup, err := openPolicyDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "policy yaml-reload: %v\n", err)
		return 1
	}
	defer cleanup()
	store := policy.NewStore(db, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	actor := uuid.Nil
	if *actorRaw != "" {
		if u, err := uuid.Parse(*actorRaw); err == nil {
			actor = u
		}
	}
	applied, err := store.ApplyYAML(ctx, cfg, actor)
	if err != nil {
		fmt.Fprintf(os.Stderr, "policy yaml-reload: %v\n", err)
		return 1
	}
	fmt.Printf("applied=%d\n", applied)
	return 0
}

