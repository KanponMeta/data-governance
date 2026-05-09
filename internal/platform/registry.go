// Package platform provides the central extension registry for the data governance
// platform. It decouples Phase 5 plans (05-01 through 05-05) from the core
// router, scheduler, and command dispatcher so that downstream plans can extend
// the platform by calling RegisterRoutes/RegisterScheduler/RegisterCommand without
// editing router.go, main.go, or scheduler.go.
//
// Architecture (B-03 fix):
//   - Each plan package calls RegisterRoutes/RegisterCommand/RegisterScheduler in
//     an init() function (or explicit wiring function).
//   - router.go calls platform.MountAllRoutes(r) once at startup.
//   - main.go calls platform.DispatchCommand(os.Args[1], os.Args[2:]) once.
//   - scheduler.go calls platform.RunSchedulers(ctx) once per tick.
package platform

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"sort"
	"sync"

	"github.com/casbin/casbin/v2"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kanpon/data-governance/internal/auth"
	"github.com/kanpon/data-governance/internal/event"
)

// MountDeps holds the dependencies passed to each mounted route handler.
type MountDeps struct {
	DB          *sql.DB
	AuthMW      func(http.Handler) http.Handler
	Enforcer    *casbin.Enforcer
	AuthService *auth.Service
	Events      event.Writer
	AuditPool   *pgxpool.Pool
	Extra       map[string]any
}

// MountFn is the signature for a route mounting function.
type MountFn func(chi.Router, MountDeps)

// SchedulerFn is the signature for a periodic scheduler function.
type SchedulerFn func(ctx context.Context) error

// CommandFn is the signature for a CLI subcommand dispatcher.
// Returns exit code: 0 = success, 1 = error, 2 = usage error.
type CommandFn func(args []string) int

// --- Route registry ---

var (
	routesMu sync.Mutex
	routes   = map[string]MountFn{}
)

// RegisterRoutes registers a named route mount function.
// Panics if a route with the same name is already registered.
func RegisterRoutes(name string, fn MountFn) {
	routesMu.Lock()
	defer routesMu.Unlock()
	if _, exists := routes[name]; exists {
		panic("platform: duplicate route registration: " + name)
	}
	routes[name] = fn
}

// MountAllRoutes iterates all registered route mount functions in alphabetical
// order and calls each with the provided router and dependencies.
func MountAllRoutes(r chi.Router, deps MountDeps) {
	routesMu.Lock()
	names := make([]string, 0, len(routes))
	for n := range routes {
		names = append(names, n)
	}
	sort.Strings(names)
	fns := make([]MountFn, 0, len(routes))
	for _, n := range names {
		fns = append(fns, routes[n])
	}
	routesMu.Unlock()
	for _, fn := range fns {
		fn(r, deps)
	}
}

// --- Scheduler registry ---

var (
	schedMu    sync.Mutex
	schedulers = map[string]SchedulerFn{}
)

// RegisterScheduler registers a named scheduler function.
// Panics if a scheduler with the same name is already registered.
func RegisterScheduler(name string, fn SchedulerFn) {
	schedMu.Lock()
	defer schedMu.Unlock()
	if _, exists := schedulers[name]; exists {
		panic("platform: duplicate scheduler registration: " + name)
	}
	schedulers[name] = fn
}

// RunSchedulers runs all registered schedulers in parallel and waits for all to
// complete before returning.
func RunSchedulers(ctx context.Context) {
	schedMu.Lock()
	fns := make([]SchedulerFn, 0, len(schedulers))
	for _, fn := range schedulers {
		fns = append(fns, fn)
	}
	schedMu.Unlock()

	var wg sync.WaitGroup
	for _, fn := range fns {
		wg.Add(1)
		go func(f SchedulerFn) {
			defer wg.Done()
			_ = f(ctx)
		}(fn)
	}
	wg.Wait()
}

// --- Command registry ---

var (
	cmdMu     sync.Mutex
	commands  = map[string]CommandFn{}
)

// RegisterCommand registers a named CLI subcommand dispatcher.
// Panics if a command with the same name is already registered.
func RegisterCommand(name string, fn CommandFn) {
	cmdMu.Lock()
	defer cmdMu.Unlock()
	if _, exists := commands[name]; exists {
		panic("platform: duplicate command registration: " + name)
	}
	commands[name] = fn
}

// DispatchCommand looks up the named command and calls it with args.
// Returns 2 if the command is not found (with a message printed to stderr).
func DispatchCommand(name string, args []string) int {
	cmdMu.Lock()
	fn, ok := commands[name]
	cmdMu.Unlock()
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", name)
		return 2
	}
	return fn(args)
}
