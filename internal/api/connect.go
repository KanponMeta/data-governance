package api

import (
	"context"
	"net/http"

	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/kanpon/data-governance/internal/auth"
	"github.com/kanpon/data-governance/internal/event"
	"github.com/kanpon/data-governance/internal/storage/ent"
	"github.com/kanpon/data-governance/internal/storage/ent/assetversion"
	"github.com/kanpon/data-governance/internal/storage/ent/run"
	"github.com/kanpon/data-governance/internal/storage/ent/runstep"

	"github.com/kanpon/data-governance/proto/api/v1"
	"github.com/kanpon/data-governance/proto/api/v1/v1connect"
	"google.golang.org/protobuf/types/known/timestamppb"
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
// Implemented in plan 06-02 — queries AssetVersion, Run, and RunStep from ent.
func newAssetConnectHandler(deps ConnectDeps) (string, http.Handler) {
	srv := &assetConnectServer{deps: deps}
	path, handler := v1connect.NewAssetServiceHandler(srv)
	return path, handler
}

// assetConnectServer handles ConnectRPC requests for AssetService.
// Full implementation: ListAssets, GetAsset, ListRuns, GetRun with ent queries.
type assetConnectServer struct {
	deps ConnectDeps
}

func (s *assetConnectServer) ListAssets(ctx context.Context, req *connect.Request[v1.ListAssetsRequest]) (*connect.Response[v1.ListAssetsResponse], error) {
	if s.deps.Ent == nil {
		return nil, connect.NewError(connect.CodeInternal, nil)
	}

	page := int(req.Msg.Page)
	if page < 1 {
		page = 1
	}
	pageSize := int(req.Msg.PageSize)
	if pageSize < 1 {
		pageSize = 50
	}
	if pageSize > 100 {
		pageSize = 100
	}
	offset := (page - 1) * pageSize

	// Query asset_versions for assets, ordered by created_at DESC.
	versions, err := s.deps.Ent.AssetVersion.Query().
		Order(ent.Desc(assetversion.FieldCreatedAt)).
		Limit(pageSize).
		Offset(offset).
		All(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	total, err := s.deps.Ent.AssetVersion.Query().Count(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	assets := make([]*v1.AssetResponse, 0, len(versions))
	for _, v := range versions {
		a := &v1.AssetResponse{
			Name:                 v.Asset,
			State:               "active", // AssetVersion doesn't have governance_state field; default to active
			LastMaterializeState: v.DriftStatus,
			Description:         v.Description,
		}
		if v.Tags != nil {
			a.Tags = v.Tags
		}
		if !v.CreatedAt.IsZero() {
			a.LastMaterializedAt = timestamppb.New(v.CreatedAt)
		}
		assets = append(assets, a)
	}

	return connect.NewResponse(&v1.ListAssetsResponse{
		Assets: assets,
		Total:  int32(total),
		Page:   int32(page),
	}), nil
}

func (s *assetConnectServer) GetAsset(ctx context.Context, req *connect.Request[v1.GetAssetRequest]) (*connect.Response[v1.AssetResponse], error) {
	if s.deps.Ent == nil {
		return nil, connect.NewError(connect.CodeInternal, nil)
	}

	assetName := req.Msg.Name
	if assetName == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}

	// Get latest version for this asset.
	versions, err := s.deps.Ent.AssetVersion.Query().
		Where(assetversion.Asset(assetName)).
		Order(ent.Desc(assetversion.FieldCreatedAt)).
		Limit(1).
		All(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	if len(versions) == 0 {
		return nil, connect.NewError(connect.CodeNotFound, nil)
	}

	v := versions[0]
	a := &v1.AssetResponse{
		Name:                 v.Asset,
		State:               "active",
		LastMaterializeState: v.DriftStatus,
		Description:         v.Description,
	}
	if v.Tags != nil {
		a.Tags = v.Tags
	}
	if !v.CreatedAt.IsZero() {
		a.LastMaterializedAt = timestamppb.New(v.CreatedAt)
	}

	return connect.NewResponse(a), nil
}

func (s *assetConnectServer) ListRuns(ctx context.Context, req *connect.Request[v1.ListRunsRequest]) (*connect.Response[v1.ListRunsResponse], error) {
	if s.deps.Ent == nil {
		return nil, connect.NewError(connect.CodeInternal, nil)
	}

	assetName := req.Msg.AssetName
	page := int(req.Msg.Page)
	if page < 1 {
		page = 1
	}
	pageSize := int(req.Msg.PageSize)
	if pageSize < 1 {
		pageSize = 50
	}
	if pageSize > 100 {
		pageSize = 100
	}
	offset := (page - 1) * pageSize

	query := s.deps.Ent.Run.Query().Order(ent.Desc(run.FieldQueuedAt)).Limit(pageSize).Offset(offset)
	if assetName != "" {
		query.Where(run.AssetName(assetName))
	}

	runs, err := query.All(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	total, err := s.deps.Ent.Run.Query().Where(run.AssetName(assetName)).Count(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	runResponses := make([]*v1.RunResponse, 0, len(runs))
	for _, r := range runs {
		rr := &v1.RunResponse{
			Id:          r.ID.String(),
			AssetName:   r.AssetName,
			State:       r.State,
			QualityState: "",
		}
		rr.QueuedAt = timestamppb.New(r.QueuedAt)
		if r.StartedAt != nil {
			rr.StartedAt = timestamppb.New(*r.StartedAt)
		}
		if r.FinishedAt != nil {
			rr.FinishedAt = timestamppb.New(*r.FinishedAt)
		}

		// Fetch run steps for this run.
		steps, err := s.deps.Ent.RunStep.Query().
			Where(runstep.RunID(r.ID)).
			Order(ent.Asc(runstep.FieldTopoOrder)).
			All(ctx)
		if err == nil {
			for _, step := range steps {
				rs := &v1.RunStepResponse{
					Name:  step.AssetName,
					State: step.State,
				}
				if step.StartedAt != nil {
					rs.StartedAt = timestamppb.New(*step.StartedAt)
				}
				if step.FinishedAt != nil {
					rs.FinishedAt = timestamppb.New(*step.FinishedAt)
				}
				if step.ErrorMessage != "" {
					rs.Error = step.ErrorMessage
				}
				rr.Steps = append(rr.Steps, rs)
			}
		}

		runResponses = append(runResponses, rr)
	}

	return connect.NewResponse(&v1.ListRunsResponse{
		Runs:  runResponses,
		Total: int32(total),
	}), nil
}

func (s *assetConnectServer) GetRun(ctx context.Context, req *connect.Request[v1.GetRunRequest]) (*connect.Response[v1.RunResponse], error) {
	if s.deps.Ent == nil {
		return nil, connect.NewError(connect.CodeInternal, nil)
	}

	runID, err := uuid.Parse(req.Msg.RunId)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}

	r, err := s.deps.Ent.Run.Get(ctx, runID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, nil)
	}

	rr := &v1.RunResponse{
		Id:          r.ID.String(),
		AssetName:   r.AssetName,
		State:       r.State,
		QualityState: "",
	}
	rr.QueuedAt = timestamppb.New(r.QueuedAt)
	if r.StartedAt != nil {
		rr.StartedAt = timestamppb.New(*r.StartedAt)
	}
	if r.FinishedAt != nil {
		rr.FinishedAt = timestamppb.New(*r.FinishedAt)
	}

	// Fetch run steps.
	steps, err := s.deps.Ent.RunStep.Query().
		Where(runstep.RunID(r.ID)).
		Order(ent.Asc(runstep.FieldTopoOrder)).
		All(ctx)
	if err == nil {
		for _, step := range steps {
			rs := &v1.RunStepResponse{
				Name:  step.AssetName,
				State: step.State,
			}
			if step.StartedAt != nil {
				rs.StartedAt = timestamppb.New(*step.StartedAt)
			}
			if step.FinishedAt != nil {
				rs.FinishedAt = timestamppb.New(*step.FinishedAt)
			}
			if step.ErrorMessage != "" {
				rs.Error = step.ErrorMessage
			}
			rr.Steps = append(rr.Steps, rs)
		}
	}

	return connect.NewResponse(rr), nil
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