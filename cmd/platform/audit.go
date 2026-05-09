package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kanpon/data-governance/internal/audit"
	"github.com/kanpon/data-governance/internal/platform"
)

// init self-registers the audit subcommand via the platform registry (B-03 fix).
func init() {
	platform.RegisterCommand("audit", dispatchAudit)
}

func dispatchAudit(args []string) int {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "Usage: platform audit [verify|export]\n")
		return 2
	}
	switch args[0] {
	case "verify":
		return auditVerifyCmd(args[1:])
	case "export":
		return auditExportCmd(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown audit subcommand: %s\n", args[0])
		return 2
	}
}

func auditVerifyCmd(args []string) int {
	var from, to int64
	var outputFormat string
	fs := flag.NewFlagSet("platform audit verify", flag.ExitOnError)
	fs.Int64Var(&from, "from", 1, "starting sequence (inclusive)")
	fs.Int64Var(&to, "to", 0, "ending sequence (inclusive, 0=latest)")
	fs.StringVar(&outputFormat, "format", "table", "output format: table, json")
	fs.Parse(args)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	db, err := openAuditDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "audit verify: open db: %v\n", err)
		return 1
	}
	defer db.Close()

	if to == 0 {
		var maxSeq int64
		err := db.QueryRowContext(ctx, "SELECT COALESCE(MAX(seq),0) FROM audit.audit_log").Scan(&maxSeq)
		if err != nil {
			fmt.Fprintf(os.Stderr, "audit verify: get max seq: %v\n", err)
			return 1
		}
		to = maxSeq
	}

	result, err := audit.Verify(ctx, db, from, to)
	if err != nil {
		fmt.Fprintf(os.Stderr, "audit verify: %v\n", err)
		return 1
	}

	if !result.OK {
		if outputFormat == "json" {
			enc, _ := json.Marshal(map[string]any{
				"ok":           false,
				"mismatch_seq": *result.MismatchSeq,
				"stored_hash":  result.StoredHash,
				"computed_hash": result.ComputedHash,
			})
			fmt.Println(string(enc))
		} else {
			fmt.Printf("MISMATCH · seq=%d · stored=%x · computed=%x\n",
				*result.MismatchSeq, result.StoredHash, result.ComputedHash)
		}
		return 2
	}

	if outputFormat == "json" {
		enc, _ := json.Marshal(map[string]any{"ok": true, "scanned": result.Scanned})
		fmt.Println(string(enc))
	} else {
		fmt.Printf("OK · scanned=%d\n", result.Scanned)
	}
	return 0
}

func auditExportCmd(args []string) int {
	var from, to string
	var format string
	var outPath string
	fs := flag.NewFlagSet("platform audit export", flag.ExitOnError)
	fs.StringVar(&from, "from", "", "ISO 8601 start time")
	fs.StringVar(&to, "to", "", "ISO 8601 end time")
	fs.StringVar(&format, "format", "jsonl", "output format: jsonl, csv, json")
	fs.StringVar(&outPath, "out", "", "output file path (default stdout)")
	fs.Parse(args)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	db, err := openAuditDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "audit export: open db: %v\n", err)
		return 1
	}
	defer db.Close()

	fromTime, _ := parseTime(from)
	if from == "" {
		fromTime = time.Now().AddDate(0, 0, -30)
	}
	toTime, _ := parseTime(to)
	if to == "" {
		toTime = time.Now()
	}

	var w io.Writer = os.Stdout
	if outPath != "" {
		f, err := os.Create(outPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "audit export: create file: %v\n", err)
			return 1
		}
		defer f.Close()
		w = f
	}

	af := audit.Format(format)
	_, err = audit.Export(ctx, db, &writeNopCloser{w}, af, fromTime, toTime)
	if err != nil {
		fmt.Fprintf(os.Stderr, "audit export: %v\n", err)
		return 1
	}
	return 0
}

type writeNopCloser struct{ w io.Writer }

func (wc *writeNopCloser) Write(p []byte) (int, error) { return wc.w.Write(p) }

func openAuditDB() (*sql.DB, error) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return nil, fmt.Errorf("DATABASE_URL required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	return db, nil
}

func parseTime(s string) (time.Time, error) {
	return time.Parse(time.RFC3339, s)
}
