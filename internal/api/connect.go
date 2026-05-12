package api

import (
	"context"
	"net/http"

	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"
	"github.com/kanpon/data-governance/internal/auth"
	"github.com/kanpon/data-governance/internal/event"
	"github.com/kanpon/data-governance/internal/storage/ent"

	"github.com/kanpon/data-governance/proto/api/v1"
	"github.com/kanpon/data-governance/proto/api/v1/v1connect"
)

// ConnectDeps holds dependencies for ConnectRPC handlers.
// Mirrors Deps but without chi-specific fields.
type ConnectDeps struct {
	AuthService    AuthServiceServer
	AssetService   AssetServiceServer
	LineageService LineageServiceServer
	GovernanceService GovernanceServiceServer
	AuthMW         func(http.Handler) http.Handler
	Enforcer       any // casbin.Enforcer
	Issuer         *auth.TokenIssuer
	Events         event.Writer
	Ent            *ent.Client
}

// AuthServiceServer is the interface implemented by the auth connect handler.
type AuthServiceServer interface {
	GetMe(context.Context, *connect.Request[v1.UserResponse]) (*connect.Response[v1.UserResponse], error)
}

// AssetServiceServer is the interface implemented by the asset connect handler.
type AssetServiceServer interface {
	ListAssets(context.Context, *connect.Request[v1.ListAssetsRequest]) (*connect.Response[v1.ListAssetsResponse], error)
	GetAsset(context.Context, *connect.Request[v1.GetAssetRequest]) (*connect.Response[v1.AssetResponse], error)
	ListRuns(context.Context, *connect.Request[v1.ListRunsRequest]) (*connect.Response[v1.ListRunsResponse], error)
	GetRun(context.Context, *connect.Request[v1.GetRunRequest]) (*connect.Response[v1.RunResponse], error)
}

// LineageServiceServer is the interface implemented by the lineage connect handler.
type LineageServiceServer interface {
	Neighborhood(context.Context, *connect.Request[v1.NeighborhoodRequest]) (*connect.Response[v1.NeighborhoodResponse], error)
	Impact(context.Context, *connect.Request[v1.ImpactRequest]) (*connect.Response[v1.ImpactResponse], error)
}

// GovernanceServiceServer is the interface implemented by the governance connect handler.
type GovernanceServiceServer interface {
	ListReviews(context.Context, *connect.Request[v1.ListReviewsRequest]) (*connect.Response[v1.ListReviewsResponse], error)
	GetReview(context.Context, *connect.Request[v1.GetReviewRequest]) (*connect.Response[v1.ReviewResponse], error)
	ApproveReview(context.Context, *connect.Request[v1.ApproveReviewRequest]) (*connect.Response[v1.ReviewResponse], error)
	RejectReview(context.Context, *connect.Request[v1.RejectReviewRequest]) (*connect.Response[v1.ReviewResponse], error)
}

// mountConnectRPC wires ConnectRPC handlers into the chi router.
// During the transition period, chi handlers remain at /v1/* and
// ConnectRPC handlers are mounted at /v1/connect/* alongside them.
// End state (Plan 06-07) will migrate all chi handlers to ConnectRPC.
func mountConnectRPC(deps ConnectDeps, r chi.Router) {
	// Asset connect handler — actual logic migrated in subsequent plans.
	assetPath, assetHandler := newAssetConnectHandler(deps)
	r.Mount(assetPath, assetHandler)

	// Lineage connect handler.
	lineagePath, lineageHandler := newLineageConnectHandler(deps)
	r.Mount(lineagePath, lineageHandler)

	// Governance connect handler.
	governancePath, governanceHandler := newGovernanceConnectHandler(deps)
	r.Mount(governancePath, governanceHandler)
}

// newAssetConnectHandler returns a ConnectRPC handler for AssetService.
// The implementation is a stub that returns unimplemented errors;
// actual query logic will be added in subsequent plans (06-02, 06-03).
func newAssetConnectHandler(deps ConnectDeps) (string, http.Handler) {
	srv := &assetConnectServer{deps: deps}
	path, handler := v1connect.NewAssetServiceHandler(srv)
	return path, handler
}

type assetConnectServer struct {
	deps ConnectDeps
}

func (s *assetConnectServer) ListAssets(ctx context.Context, req *connect.Request[v1.ListAssetsRequest]) (*connect.Response[v1.ListAssetsResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (s *assetConnectServer) GetAsset(ctx context.Context, req *connect.Request[v1.GetAssetRequest]) (*connect.Response[v1.AssetResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (s *assetConnectServer) ListRuns(ctx context.Context, req *connect.Request[v1.ListRunsRequest]) (*connect.Response[v1.ListRunsResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (s *assetConnectServer) GetRun(ctx context.Context, req *connect.Request[v1.GetRunRequest]) (*connect.Response[v1.RunResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

// newLineageConnectHandler returns a ConnectRPC handler for LineageService.
func newLineageConnectHandler(deps ConnectDeps) (string, http.Handler) {
	srv := &lineageConnectServer{deps: deps}
	path, handler := v1connect.NewLineageServiceHandler(srv)
	return path, handler
}

type lineageConnectServer struct {
	deps ConnectDeps
}

func (s *lineageConnectServer) Neighborhood(ctx context.Context, req *connect.Request[v1.NeighborhoodRequest]) (*connect.Response[v1.NeighborhoodResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (s *lineageConnectServer) Impact(ctx context.Context, req *connect.Request[v1.ImpactRequest]) (*connect.Response[v1.ImpactResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

// newGovernanceConnectHandler returns a ConnectRPC handler for GovernanceService.
func newGovernanceConnectHandler(deps ConnectDeps) (string, http.Handler) {
	srv := &governanceConnectServer{deps: deps}
	path, handler := v1connect.NewGovernanceServiceHandler(srv)
	return path, handler
}

type governanceConnectServer struct {
	deps ConnectDeps
}

func (s *governanceConnectServer) ListReviews(ctx context.Context, req *connect.Request[v1.ListReviewsRequest]) (*connect.Response[v1.ListReviewsResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (s *governanceConnectServer) GetReview(ctx context.Context, req *connect.Request[v1.GetReviewRequest]) (*connect.Response[v1.ReviewResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (s *governanceConnectServer) ApproveReview(ctx context.Context, req *connect.Request[v1.ApproveReviewRequest]) (*connect.Response[v1.ReviewResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (s *governanceConnectServer) RejectReview(ctx context.Context, req *connect.Request[v1.RejectReviewRequest]) (*connect.Response[v1.ReviewResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}