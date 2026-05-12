package api

import (
	"context"
	"net/http"

	"connectrpc.com/connect"
	"github.com/kanpon/data-governance/proto/api/v1"
	"github.com/kanpon/data-governance/proto/api/v1/v1connect"
	emptypb "google.golang.org/protobuf/types/known/emptypb"
)

// newAdminConnectHandler returns a ConnectRPC handler for AdminService.
// Stub implementation — AdminService methods are not yet implemented.
// Full implementation will be added in subsequent plans.
func newAdminConnectHandler(deps ConnectDeps) (string, http.Handler) {
	srv := &adminConnectServer{deps: deps}
	return v1connect.NewAdminServiceHandler(srv)
}

// adminConnectServer handles ConnectRPC requests for AdminService.
// Stub implementation - all methods return Unimplemented.
type adminConnectServer struct {
	deps ConnectDeps
}

func (s *adminConnectServer) ListUsers(ctx context.Context, req *connect.Request[v1.ListUsersRequest]) (*connect.Response[v1.ListUsersResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (s *adminConnectServer) AssignRole(ctx context.Context, req *connect.Request[v1.AssignRoleRequest]) (*connect.Response[v1.UserResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (s *adminConnectServer) RemoveRole(ctx context.Context, req *connect.Request[v1.RemoveRoleRequest]) (*connect.Response[v1.UserResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (s *adminConnectServer) ListRoles(ctx context.Context, req *connect.Request[v1.ListRolesRequest]) (*connect.Response[v1.ListRolesResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (s *adminConnectServer) CreateRole(ctx context.Context, req *connect.Request[v1.CreateRoleRequest]) (*connect.Response[v1.RoleResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (s *adminConnectServer) DeleteRole(ctx context.Context, req *connect.Request[v1.DeleteRoleRequest]) (*connect.Response[emptypb.Empty], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (s *adminConnectServer) ListPolicies(ctx context.Context, req *connect.Request[v1.ListPoliciesRequest]) (*connect.Response[v1.ListPoliciesResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (s *adminConnectServer) CreatePolicy(ctx context.Context, req *connect.Request[v1.CreatePolicyRequest]) (*connect.Response[v1.ColumnPolicyResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (s *adminConnectServer) UpdatePolicy(ctx context.Context, req *connect.Request[v1.UpdatePolicyRequest]) (*connect.Response[v1.ColumnPolicyResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (s *adminConnectServer) DeletePolicy(ctx context.Context, req *connect.Request[v1.DeletePolicyRequest]) (*connect.Response[emptypb.Empty], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}