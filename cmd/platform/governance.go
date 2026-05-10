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

	"github.com/kanpon/data-governance/internal/asset"
	"github.com/kanpon/data-governance/internal/config"
	"github.com/kanpon/data-governance/internal/governance"
	"github.com/kanpon/data-governance/internal/platform"
)

// init self-registers the governance subcommand via the platform registry (B-03 fix).
// Dispatched from cmd/platform/main.go's default branch via DispatchCommand.
func init() {
	platform.RegisterCommand("governance", dispatchGovernance)
}

// dispatchGovernance is the entry point for `./platform governance <subcommand>`.
//
//	submit   <asset> --code-hash=<hash> [--reviewers=a,b]
//	review   <review-id> --approve|--reject --comment=<txt>
//	status   [<asset>]
//	reassign <review-id> <new-reviewer1,new-reviewer2,...>
func dispatchGovernance(args []string) int {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "Usage: platform governance [submit|review|status|reassign]\n")
		return 2
	}
	switch args[0] {
	case "submit":
		return governanceSubmitCmd(args[1:])
	case "review":
		return governanceReviewCmd(args[1:])
	case "status":
		return governanceStatusCmd(args[1:])
	case "reassign":
		return governanceReassignCmd(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown governance subcommand: %s\n", args[0])
		return 2
	}
}

// openGovernanceDB opens a *sql.DB to the platform metadata store.
func openGovernanceDB() (*sql.DB, func(), error) {
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

// newGovernanceWorkflow constructs a Workflow against the live DB.
// queue=nil — CLI does not enqueue notifications (the daemon does).
func newGovernanceWorkflow(db *sql.DB) *governance.Workflow {
	resolver := governance.NewResolver(db, nil)
	checker := governance.NewAutoApprovalChecker(db)
	return governance.NewWorkflow(db, resolver, checker, nil)
}

// ===== submit =====

func governanceSubmitCmd(args []string) int {
	fs := flag.NewFlagSet("governance submit", flag.ContinueOnError)
	codeHash := fs.String("code-hash", "", "asset code_hash to submit (required)")
	reviewersExtra := fs.String("reviewers", "", "comma-separated reviewer roles to add to the pool")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) < 1 {
		fmt.Fprintf(os.Stderr, "Usage: platform governance submit <asset> --code-hash=<hash> [--reviewers=a,b]\n")
		return 2
	}
	assetName := rest[0]
	if *codeHash == "" {
		fmt.Fprintf(os.Stderr, "governance submit: --code-hash is required\n")
		return 2
	}

	db, cleanup, err := openGovernanceDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "governance submit: %v\n", err)
		return 1
	}
	defer cleanup()

	// Resolve asset from registry. The CLI runs out-of-band, so the global
	// registry is empty unless an asset bundle is loaded first — we fall
	// back to a stub asset with the supplied name + code_hash so the
	// auto-approval checker can still query the DB.
	a, err := asset.Default().Get(assetName)
	if err != nil {
		// stub asset for CLI submit when registry is not pre-populated.
		stub, sErr := asset.New(assetName).Connector("cli-stub").
			Materialize(func(_ context.Context, _ asset.AssetIO) (asset.MaterializeResult, error) {
				return asset.MaterializeResult{}, nil
			}).Build()
		if sErr != nil {
			fmt.Fprintf(os.Stderr, "governance submit: build stub asset: %v\n", sErr)
			return 1
		}
		a = stub
	}

	w := newGovernanceWorkflow(db)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	submitter := getActorFromEnv()
	res, err := w.Submit(ctx, assetName, *codeHash, submitter, parseCSV(*reviewersExtra), a, nil, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "governance submit: %v\n", err)
		return 1
	}
	body, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(body))
	return 0
}

// ===== review =====

func governanceReviewCmd(args []string) int {
	fs := flag.NewFlagSet("governance review", flag.ContinueOnError)
	doApprove := fs.Bool("approve", false, "approve the review")
	doReject := fs.Bool("reject", false, "reject the review (comment required)")
	comment := fs.String("comment", "", "decision comment; required for --reject")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) < 1 {
		fmt.Fprintf(os.Stderr, "Usage: platform governance review <review-id> --approve|--reject --comment=<txt>\n")
		return 2
	}
	if *doApprove == *doReject {
		fmt.Fprintf(os.Stderr, "governance review: exactly one of --approve / --reject must be set\n")
		return 2
	}
	if *doReject && strings.TrimSpace(*comment) == "" {
		fmt.Fprintf(os.Stderr, "governance review: --comment is required for --reject\n")
		return 2
	}
	reviewID, err := uuid.Parse(rest[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "governance review: invalid review id %q\n", rest[0])
		return 2
	}

	db, cleanup, err := openGovernanceDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "governance review: %v\n", err)
		return 1
	}
	defer cleanup()

	w := newGovernanceWorkflow(db)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	decider := getActorFromEnv()

	var rev governance.Review
	if *doApprove {
		rev, err = w.Approve(ctx, reviewID, decider, *comment)
	} else {
		rev, err = w.Reject(ctx, reviewID, decider, *comment)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "governance review: %v\n", err)
		return 1
	}
	body, _ := json.MarshalIndent(rev, "", "  ")
	fmt.Println(string(body))
	return 0
}

// ===== status =====

func governanceStatusCmd(args []string) int {
	asset := ""
	if len(args) > 0 {
		asset = args[0]
	}
	db, cleanup, err := openGovernanceDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "governance status: %v\n", err)
		return 1
	}
	defer cleanup()
	w := newGovernanceWorkflow(db)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rows, err := w.Status(ctx, asset)
	if err != nil {
		fmt.Fprintf(os.Stderr, "governance status: %v\n", err)
		return 1
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tASSET\tSTATUS\tSUBMITTED_AT\tDECIDED_AT")
	for _, r := range rows {
		decided := "-"
		if r.DecidedAt != nil {
			decided = r.DecidedAt.Format(time.RFC3339)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			r.ID, r.Asset, r.Status, r.SubmittedAt.Format(time.RFC3339), decided)
	}
	if err := tw.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "governance status: %v\n", err)
		return 1
	}
	return 0
}

// ===== reassign =====

func governanceReassignCmd(args []string) int {
	if len(args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: platform governance reassign <review-id> <reviewer1,reviewer2,...>\n")
		return 2
	}
	reviewID, err := uuid.Parse(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "governance reassign: invalid review id %q\n", args[0])
		return 2
	}
	newReviewers := parseCSV(args[1])
	if len(newReviewers) == 0 {
		fmt.Fprintf(os.Stderr, "governance reassign: at least one reviewer required\n")
		return 2
	}

	db, cleanup, err := openGovernanceDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "governance reassign: %v\n", err)
		return 1
	}
	defer cleanup()
	w := newGovernanceWorkflow(db)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	actor := getActorFromEnv()
	rev, err := w.Reassign(ctx, reviewID, newReviewers, actor)
	if err != nil {
		fmt.Fprintf(os.Stderr, "governance reassign: %v\n", err)
		return 1
	}
	body, _ := json.MarshalIndent(rev, "", "  ")
	fmt.Println(string(body))
	return 0
}

// ===== helpers =====

func parseCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// getActorFromEnv reads ACTOR_ID from env (UUID); falls back to uuid.Nil. CLI
// invocations do not have a JWT principal so the operator either sets
// ACTOR_ID=<uuid> or accepts the all-zeros uuid placeholder.
func getActorFromEnv() uuid.UUID {
	if v := os.Getenv("ACTOR_ID"); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			return id
		}
	}
	return uuid.Nil
}
