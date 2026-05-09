package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/kanpon/data-governance/internal/auth"
	"github.com/kanpon/data-governance/internal/event"
	"github.com/kanpon/data-governance/internal/storage/ent/schemachange"
)

type ackBody struct {
	Reason string `json:"reason"`
}

// ackSchemaChange returns an http.HandlerFunc for POST /v1/schema/changes/{id}/ack.
// The governance-role check is enforced at the router level by RequireRole("governance").
// D-10 semantics: reason is required, ack is idempotent-reject (409 if already acked).
func ackSchemaChange(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idStr := chi.URLParam(r, "id")
		id, err := uuid.Parse(idStr)
		if err != nil {
			writeProblemJSON(w, http.StatusBadRequest, "invalid_id", "id must be a valid UUID")
			return
		}

		var body ackBody
		dec := json.NewDecoder(r.Body)
		if err := dec.Decode(&body); err != nil || body.Reason == "" {
			writeProblemJSON(w, http.StatusBadRequest, "reason_required", "body.reason is required and must be non-empty")
			return
		}

		principal, _ := auth.PrincipalFromContext(r.Context())

		// Check current state — reject if already acknowledged (D-10 ack-once semantic).
		existing, err := deps.Ent.SchemaChange.Get(r.Context(), id)
		if err != nil {
			writeProblemJSON(w, http.StatusNotFound, "schema_change_not_found", "schema change not found")
			return
		}
		if existing.AcknowledgedAt != nil {
			writeProblemJSON(w, http.StatusConflict, "already_acknowledged",
				"this schema change was already acknowledged")
			return
		}

		now := time.Now().UTC()
		updated, err := deps.Ent.SchemaChange.UpdateOneID(id).
			SetAcknowledgedAt(now).
			SetAcknowledgedBy(principal.UserID).
			SetAcknowledgementReason(body.Reason).
			Save(r.Context())
		if err != nil {
			writeProblemJSON(w, http.StatusInternalServerError, "ack_failed", err.Error())
			return
		}

		// Emit schema.break_acknowledged event (D-21).
		_ = deps.Events.Append(r.Context(), event.Event{
			Type:         event.EventTypeSchemaBreakAcknowledged,
			OccurredAt:   now,
			ActorID:      &principal.UserID,
			ResourceType: "schema_change",
			ResourceID:   id.String(),
			Payload: map[string]any{
				"schema_change_id": id.String(),
				"asset":            existing.Asset,
				"reason":           body.Reason,
				"acknowledged_by":  principal.UserID.String(),
			},
		})

		writeJSONResponse(w, http.StatusOK, updated)
	}
}

// listSchemaChanges returns an http.HandlerFunc for GET /v1/schema/changes.
// Requires ?asset=NAME query parameter. Optional ?column=COL for column-level filtering.
// Results ordered by observed_at ASC (META-05 timeline, D-12).
func listSchemaChanges(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		asset := r.URL.Query().Get("asset")
		if asset == "" {
			writeProblemJSON(w, http.StatusBadRequest, "asset_required", "?asset=NAME is required")
			return
		}

		column := r.URL.Query().Get("column")
		q := deps.Ent.SchemaChange.Query().
			Where(schemachange.AssetEQ(asset)).
			Order(schemachange.ByObservedAt())

		if column != "" {
			q = q.Where(schemachange.ColumnNameEQ(column))
		}

		rows, err := q.All(r.Context())
		if err != nil {
			writeProblemJSON(w, http.StatusInternalServerError, "list_failed", err.Error())
			return
		}

		writeJSONResponse(w, http.StatusOK, map[string]any{"changes": rows})
	}
}

// writeProblemJSON writes an RFC 7807 problem+json response.
// This is the api-package local version (the exported WriteProblem is also available).
func writeProblemJSON(w http.ResponseWriter, status int, errType, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":   errType,
		"status": status,
		"detail": detail,
	})
}

// writeJSONResponse writes v as JSON with the given status code.
func writeJSONResponse(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
