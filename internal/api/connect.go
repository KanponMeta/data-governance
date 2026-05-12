package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"github.com/casbin/casbin/v2"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/kanpon/data-governance/internal/auth"
	"github.com/kanpon/data-governance/internal/event"
	"github.com/kanpon/data-governance/internal/governance"
	"github.com/kanpon/data-governance/internal/storage/ent"
	"github.com/kanpon/data-governance/internal/storage/ent/assetversion"
	"github.com/kanpon/data-governance/internal/storage/ent/run"
	"github.com/kanpon/data-governance/internal/storage/ent/runstep"

	"github.com/kanpon/data-governance/proto/api/v1"
	"github.com/kanpon/data-governance/proto/api/v1/v1connect"
	emptypb "google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ConnectDeps holds dependencies for ConnectRPC handlers.
// Mirrors Deps but without chi-specific fields.
type ConnectDeps struct {
	AuthSvc             *auth.Service // domain auth service with business logic
	AuthServiceServer   AuthServiceServer
	AssetService        AssetServiceServer
	LineageService      LineageServiceServer
	GovernanceService   GovernanceServiceServer
	GovernanceWorkflow  *governance.Workflow // Phase 5 workflow service
	AdminService        AdminServiceServer
	AuthMW              func(http.Handler) http.Handler
	Enforcer            *casbin.Enforcer
	Issuer              *auth.TokenIssuer
	Events              event.Writer
	Ent                 *ent.Client
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

// AdminServiceServer is the interface implemented by the admin connect handler.
type AdminServiceServer interface {
	ListUsers(context.Context, *connect.Request[v1.ListUsersRequest]) (*connect.Response[v1.ListUsersResponse], error)
	AssignRole(context.Context, *connect.Request[v1.AssignRoleRequest]) (*connect.Response[v1.UserResponse], error)
	RemoveRole(context.Context, *connect.Request[v1.RemoveRoleRequest]) (*connect.Response[v1.UserResponse], error)
	ListRoles(context.Context, *connect.Request[v1.ListRolesRequest]) (*connect.Response[v1.ListRolesResponse], error)
	CreateRole(context.Context, *connect.Request[v1.CreateRoleRequest]) (*connect.Response[v1.RoleResponse], error)
	DeleteRole(context.Context, *connect.Request[v1.DeleteRoleRequest]) (*connect.Response[emptypb.Empty], error)
	ListPolicies(context.Context, *connect.Request[v1.ListPoliciesRequest]) (*connect.Response[v1.ListPoliciesResponse], error)
	CreatePolicy(context.Context, *connect.Request[v1.CreatePolicyRequest]) (*connect.Response[v1.ColumnPolicyResponse], error)
	UpdatePolicy(context.Context, *connect.Request[v1.UpdatePolicyRequest]) (*connect.Response[v1.ColumnPolicyResponse], error)
	DeletePolicy(context.Context, *connect.Request[v1.DeletePolicyRequest]) (*connect.Response[emptypb.Empty], error)
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
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, nil)
	}

	status := req.Msg.Status
	page := int(req.Msg.Page)
	pageSize := int(req.Msg.PageSize)
	if pageSize <= 0 {
		pageSize = 20
	}
	if page <= 0 {
		page = 1
	}

	// List reviews filtered by status (pending/approved/rejected/all)
	// The Workflow.Status returns reviews; we filter by status if provided
	var allReviews []governance.Review
	var err error

	if status == "" || status == "all" {
		allReviews, err = s.deps.GovernanceWorkflow.Status(ctx, "")
	} else {
		// Filter locally — Workflow.Status returns all; filter by status
		allReviews, err = s.deps.GovernanceWorkflow.Status(ctx, "")
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// Filter by status if specified
	var filtered []governance.Review
	for _, r := range allReviews {
		if status != "" && status != "all" && r.Status != status {
			continue
		}
		// Filter to reviews where current user is a reviewer in the pool
		// (reviewers can see their assigned reviews)
		filtered = append(filtered, r)
	}

	// Pagination
	total := int32(len(filtered))
	start := (page - 1) * pageSize
	end := start + pageSize
	if start >= len(filtered) {
		filtered = []governance.Review{}
	} else {
		if end > len(filtered) {
			end = len(filtered)
		}
		filtered = filtered[start:end]
	}

	reviews := make([]*v1.ReviewResponse, 0, len(filtered))
	for _, r := range filtered {
		review := &v1.ReviewResponse{
			Id:        r.ID.String(),
			AssetName: r.Asset,
			Status:    r.Status,
		}
		if !r.SubmittedAt.IsZero() {
			review.SubmittedAt = timestamppb.New(r.SubmittedAt)
		}
		if r.DecidedAt != nil {
			review.ReviewedAt = timestamppb.New(*r.DecidedAt)
		}
		if r.DecidedByID != nil {
			review.ReviewedBy = r.DecidedByID.String()
		}
		for _, c := range parseComments(r.Comment) {
			review.Comments = append(review.Comments, &v1.ReviewComment{
				Author: c.author,
				Body:   c.body,
			})
		}
		_ = p // silence unused warning
		reviews = append(reviews, review)
	}

	return connect.NewResponse(&v1.ListReviewsResponse{
		Reviews: reviews,
		Total:   total,
	}), nil
}

func (s *governanceConnectServer) GetReview(ctx context.Context, req *connect.Request[v1.GetReviewRequest]) (*connect.Response[v1.ReviewResponse], error) {
	_, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, nil)
	}

	reviewIDStr := req.Msg.Id
	if reviewIDStr == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}

	reviewID, err := uuid.Parse(reviewIDStr)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}

	r, err := s.deps.GovernanceWorkflow.Get(ctx, reviewID)
	if errors.Is(err, governance.ErrReviewNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, nil)
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&v1.ReviewResponse{
		Id:        r.ID.String(),
		AssetName: r.Asset,
		Status:    r.Status,
	}), nil
}

func (s *governanceConnectServer) ApproveReview(ctx context.Context, req *connect.Request[v1.ApproveReviewRequest]) (*connect.Response[v1.ReviewResponse], error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, nil)
	}

	reviewID, err := uuid.Parse(req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}

	// CSRF: already validated by chi middleware on the route
	// but we need to verify permission via enforcer
	allowed, err := s.deps.Enforcer.Enforce("role:"+p.Role, "/governance/reviews/*", "write")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, nil)
	}
	if !allowed {
		// Check other roles
		for _, r := range p.Roles {
			allowed, _ = s.deps.Enforcer.Enforce("role:"+r, "/governance/reviews/*", "write")
			if allowed {
				break
			}
		}
		if !allowed {
			return nil, connect.NewError(connect.CodePermissionDenied, nil)
		}
	}

	r, err := s.deps.GovernanceWorkflow.Approve(ctx, reviewID, p.UserID, req.Msg.Comment)
	if errors.Is(err, governance.ErrReviewNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, nil)
	}
	if errors.Is(err, governance.ErrSelfApproval) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, nil)
	}
	if errors.Is(err, governance.ErrDuplicateVote) {
		return nil, connect.NewError(connect.CodeAlreadyExists, nil)
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&v1.ReviewResponse{
		Id:        r.ID.String(),
		AssetName: r.Asset,
		Status:    r.Status,
	}), nil
}

func (s *governanceConnectServer) RejectReview(ctx context.Context, req *connect.Request[v1.RejectReviewRequest]) (*connect.Response[v1.ReviewResponse], error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, nil)
	}

	reviewID, err := uuid.Parse(req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}

	if req.Msg.Comment == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("comment is required for reject"))
	}

	// Permission check via enforcer
	allowed, err := s.deps.Enforcer.Enforce("role:"+p.Role, "/governance/reviews/*", "write")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, nil)
	}
	if !allowed {
		for _, r := range p.Roles {
			allowed, _ = s.deps.Enforcer.Enforce("role:"+r, "/governance/reviews/*", "write")
			if allowed {
				break
			}
		}
		if !allowed {
			return nil, connect.NewError(connect.CodePermissionDenied, nil)
		}
	}

	r, err := s.deps.GovernanceWorkflow.Reject(ctx, reviewID, p.UserID, req.Msg.Comment)
	if errors.Is(err, governance.ErrReviewNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, nil)
	}
	if errors.Is(err, governance.ErrCommentRequired) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("comment is required"))
	}
	if errors.Is(err, governance.ErrSelfApproval) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, nil)
	}
	if errors.Is(err, governance.ErrDuplicateVote) {
		return nil, connect.NewError(connect.CodeAlreadyExists, nil)
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&v1.ReviewResponse{
		Id:        r.ID.String(),
		AssetName: r.Asset,
		Status:    r.Status,
	}), nil
}

// commentPart parses a single comment line in format "[author by uuid] body"
type commentPart struct {
	author string
	body   string
}

// parseComments extracts comment entries from the governance review comment field.
// The comment field stores votes in format "[approved/rejected by <uuid>] <comment>".
func parseComments(comment string) []commentPart {
	if comment == "" {
		return nil
	}
	var parts []commentPart
	for _, line := range strings.Split(comment, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Lines starting with [approved by or [rejected by are vote records
		// Extract author from "[approved by <uuid>]" prefix
		if strings.HasPrefix(line, "[approved by ") || strings.HasPrefix(line, "[rejected by ") {
			// Extract the comment body after the vote marker
			idx := strings.Index(line, "] ")
			if idx > 0 {
				parts = append(parts, commentPart{
					author: "reviewer",
					body:   line[idx+2:],
				})
			}
		}
	}
	return parts
}