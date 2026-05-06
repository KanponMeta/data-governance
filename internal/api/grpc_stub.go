package api

import (
	"net/http"
)

// NewGRPCMux returns a Phase 1 placeholder for the /grpc sub-tree. It deliberately
// uses net/http (not connectrpc.com/connect) because the platform's own service
// protos do not exist in Phase 1 — only the connector ABI proto does (Plan 04).
// Phase 2 will replace this with a real connect-go handler stack generated from
// a new proto/platform/v1/platform.proto file.
//
// Migration checklist for Phase 2:
//   - Define proto/platform/v1/platform.proto with PlatformService.Ping
//   - Run `buf generate` to produce internal/api/gen/platformv1connect/...
//   - Replace this file's contents with connectrpc.com/connect-based handlers
//   - Wrap /grpc sub-router with auth.Middleware (Phase 1 leaves it unauthenticated per T-05-07)
func NewGRPCMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/data_governance.v1.PlatformService/Ping", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"message":"phase1-placeholder","ready_for":"phase2","note":"will be replaced by connectrpc.com/connect handler in Phase 2"}`))
	})
	return mux
}
