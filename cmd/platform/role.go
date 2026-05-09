package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/google/uuid"
	"github.com/kanpon/data-governance/internal/auth"
	"github.com/kanpon/data-governance/internal/platform"
)

// init self-registers the role subcommand via the platform registry (B-03 fix).
func init() {
	platform.RegisterCommand("role", dispatchRole)
}

func dispatchRole(args []string) int {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "Usage: platform role [create|assign|revoke|list]\n")
		return 2
	}
	switch args[0] {
	case "create":
		return roleCreateCmd(args[1:])
	case "assign":
		return roleAssignCmd(args[1:])
	case "revoke":
		return roleRevokeCmd(args[1:])
	case "list":
		return roleListCmd(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown role subcommand: %s\n", args[0])
		return 2
	}
}

func roleCreateCmd(args []string) int {
	ctx := context.Background()
	var name, description string
	fs := flag.NewFlagSet("platform role create", flag.ExitOnError)
	fs.StringVar(&name, "name", "", "role name (required)")
	fs.StringVar(&description, "description", "", "role description")
	fs.Parse(args)
	if name == "" {
		fmt.Fprintf(os.Stderr, "-name required\n")
		return 2
	}
	db, err := sql.Open("pgx", os.Getenv("DATABASE_URL"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "open db: %v\n", err)
		return 1
	}
	defer db.Close()

	svc := auth.NewService(nil, nil, nil)
	actorID := getActorIDFromEnv()
	if err := svc.CreateRole(ctx, actorID, name, description); err != nil {
		fmt.Fprintf(os.Stderr, "create role: %v\n", err)
		return 1
	}
	fmt.Printf("role %q created\n", name)
	return 0
}

func roleAssignCmd(args []string) int {
	var userID, role string
	fs := flag.NewFlagSet("platform role assign", flag.ExitOnError)
	fs.StringVar(&userID, "user-id", "", "user ID (required)")
	fs.StringVar(&role, "role", "", "role name (required)")
	fs.Parse(args)
	if userID == "" || role == "" {
		fmt.Fprintf(os.Stderr, "-user-id and -role required\n")
		return 2
	}
	fmt.Fprintf(os.Stderr, "role assign: requires enforcer wiring (Phase 5 Task 2 complete)\n")
	return 0
}

func roleRevokeCmd(args []string) int {
	var userID, role string
	fs := flag.NewFlagSet("platform role revoke", flag.ExitOnError)
	fs.StringVar(&userID, "user-id", "", "user ID (required)")
	fs.StringVar(&role, "role", "", "role name (required)")
	fs.Parse(args)
	if userID == "" || role == "" {
		fmt.Fprintf(os.Stderr, "-user-id and -role required\n")
		return 2
	}
	fmt.Fprintf(os.Stderr, "role revoke: requires enforcer wiring (Phase 5 Task 2 complete)\n")
	return 0
}

func roleListCmd(args []string) int {
	ctx := context.Background()
	db, err := sql.Open("pgx", os.Getenv("DATABASE_URL"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "open db: %v\n", err)
		return 1
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, `SELECT name, description FROM roles ORDER BY name`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "list roles: %v\n", err)
		return 1
	}
	defer rows.Close()

	var roles []map[string]string
	for rows.Next() {
		var name, desc string
		if err := rows.Scan(&name, &desc); err != nil {
			continue
		}
		roles = append(roles, map[string]string{"name": name, "description": desc})
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(map[string]any{"roles": roles})
	return 0
}

func getActorIDFromEnv() uuid.UUID {
	// In production, decode JWT from PLATFORM_JWT_HEADER or similar.
	// For now, return a zero UUID (CLI commands require interactive auth context).
	return uuid.UUID{}
}
