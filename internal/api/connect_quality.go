package api

import (
	"context"
	"net/http"

	"connectrpc.com/connect"
	"github.com/kanpon/data-governance/proto/api/v1"
	"github.com/kanpon/data-governance/proto/api/v1/v1connect"
)

// qualityConnectServer implements QualityService handler with stub responses.
// Query logic will be implemented in subsequent waves; this satisfies the interface.
type qualityConnectServer struct {
	deps ConnectDeps
}

// newQualityConnectHandler returns a ConnectRPC handler for QualityService.
// Actual query implementation follows in subsequent waves.
// Chi handlers (/v1/quality/*) are the primary path during this scaffold phase.
func newQualityConnectHandler(deps ConnectDeps) (string, http.Handler) {
	srv := &qualityConnectServer{deps: deps}
	path, handler := v1connect.NewQualityServiceHandler(srv)
	return path, handler
}

func (s *qualityConnectServer) QualityTrend(ctx context.Context, req *connect.Request[v1.QualityTrendRequest]) (*connect.Response[v1.QualityTrendResponse], error) {
	// Query runs for quality trend
	// Return proto QualityTrendResponse
	return connect.NewResponse(&v1.QualityTrendResponse{
		Points:   []*v1.QualityTrendPoint{},
		AvgScore: 0,
	}), nil
}

func (s *qualityConnectServer) ListAlerts(ctx context.Context, req *connect.Request[v1.ListAlertsRequest]) (*connect.Response[v1.ListAlertsResponse], error) {
	// Query quality alerts
	return connect.NewResponse(&v1.ListAlertsResponse{
		Alerts: []*v1.QualityAlert{},
	}), nil
}

func (s *qualityConnectServer) AcknowledgeAlert(ctx context.Context, req *connect.Request[v1.AcknowledgeAlertRequest]) (*connect.Response[v1.AcknowledgeAlertResponse], error) {
	// Update alert acknowledged flag
	return connect.NewResponse(&v1.AcknowledgeAlertResponse{
		Acknowledged: true,
	}), nil
}
