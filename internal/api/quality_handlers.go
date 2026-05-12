package api

import (
	"context"
	"net/http"
	"strconv"
	"time"
)

// QualityTrendPoint is one data point in the quality trend.
type QualityTrendPoint struct {
	RunID       string  `json:"run_id"`
	FinishedAt  string  `json:"finished_at"`
	Score       float64 `json:"score"` // 0-100
	State       string  `json:"state"`
	RuleResults []RuleResult `json:"rule_results"`
}

// RuleResult is one rule evaluation result.
type RuleResult struct {
	RuleName  string `json:"rule_name"`
	Passed    bool   `json:"passed"`
	Value     string `json:"value"`
	Threshold string `json:"threshold"`
}

// QualityTrendResponse is GET /v1/quality/trend response.
type QualityTrendResponse struct {
	Asset    string             `json:"asset"`
	Points   []QualityTrendPoint `json:"points"`
	AvgScore float64            `json:"avg_score"`
}

// qualityTrendHandler returns quality trend for an asset.
// Query params: asset (required), runs (optional, default 30).
func qualityTrendHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		asset := r.URL.Query().Get("asset")
		if asset == "" {
			BadRequest(w, "asset parameter is required")
			return
		}

		runs := 30
		if r := r.URL.Query().Get("runs"); r != "" {
			if v, err := strconv.Atoi(r); err == nil && v > 0 && v <= 100 {
				runs = v
			}
		}

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		points, avgScore, err := fetchQualityTrend(ctx, deps, asset, runs)
		if err != nil {
			InternalServerError(w, err.Error())
			return
		}

		writeJSONResponse(w, http.StatusOK, QualityTrendResponse{
			Asset:    asset,
			Points:   points,
			AvgScore: avgScore,
		})
	}
}

func fetchQualityTrend(_ context.Context, _ Deps, _ string, _ int) ([]QualityTrendPoint, float64, error) {
	// Query runs for the asset, ordered by finished_at DESC, limit N
	// For each run, compute quality score:
	// - count passed rules / total rules * 100
	// - if no rules evaluated, score = 100
	//
	// Implementation:
	// runs, err := deps.Ent.Run.Query().
	//   Where(run.AssetName(asset)).
	//   Where(run.FinishedAtNEQ(nil)).
	//   Order(ent.Desc(run.FieldFinishedAt)).
	//   Limit(limit).
	//   All(ctx)
	//
	// For now: return empty trend (will be implemented with actual ent queries)

	return []QualityTrendPoint{}, 0, nil
}

// QualityAlert represents an active quality alert.
type QualityAlert struct {
	ID           string `json:"id"`
	AssetName    string `json:"asset_name"`
	RuleName     string `json:"rule_name"`
	Severity     string `json:"severity"` // critical, warning, info
	Message      string `json:"message"`
	CreatedAt    string `json:"created_at"`
	Acknowledged bool   `json:"acknowledged"`
}

// listAlertsHandler returns active quality alerts.
// GET /v1/quality/alerts
func listAlertsHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		_ = ctx // reserved for future ent query

		// Query quality_alerts table for unacknowledged alerts
		// alerts, err := deps.Ent.QualityAlert.Query().
		//   Where(qualityalert.Acknowledged(false)).
		//   Order(ent.Desc(qualityalert.FieldCreatedAt)).
		//   Limit(50).All(ctx)
		//
		// For now: return empty list
		alerts := []QualityAlert{}

		writeJSONResponse(w, http.StatusOK, map[string]any{
			"alerts": alerts,
		})
	}
}

// acknowledgeAlertHandler acknowledges a quality alert.
// POST /v1/quality/alerts/:id/acknowledge
func acknowledgeAlertHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		alertID := r.PathValue("id")
		if alertID == "" {
			BadRequest(w, "alert id is required")
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		_ = ctx // reserved for future ent query

		// Update quality_alerts set acknowledged = true where id = alertID
		// _, err := deps.Ent.QualityAlert.UpdateOneID(alertID).SetAcknowledged(true).Save(ctx)
		//
		// For now: return success
		writeJSONResponse(w, http.StatusOK, map[string]any{
			"acknowledged": true,
		})
	}
}
