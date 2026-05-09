package metadata

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/kanpon/data-governance/internal/auth"
	"github.com/kanpon/data-governance/internal/event"
	"github.com/kanpon/data-governance/internal/storage/ent/assetversion"
)

// MaxTags is the maximum number of tags accepted in a PATCH request (T-04-07-06).
const MaxTags = 64

// Handler provides HTTP handlers for asset/column metadata endpoints.
type Handler struct {
	store  *Store
	events event.Writer
}

// NewHandler creates a Handler backed by store and events.
func NewHandler(store *Store, events event.Writer) *Handler {
	return &Handler{store: store, events: events}
}

type patchBody struct {
	Description *string   `json:"description"`
	Owner       *string   `json:"owner"`
	Tags        *[]string `json:"tags"`
}

// PatchAsset handles PATCH /v1/assets/{name}/metadata (asset-level).
func (h *Handler) PatchAsset(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	h.patch(w, r, name, nil)
}

// PatchColumn handles PATCH /v1/assets/{name}/columns/{col}/metadata (column-level).
func (h *Handler) PatchColumn(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	col := chi.URLParam(r, "col")
	h.patch(w, r, name, &col)
}

// Get handles GET /v1/assets/{name}/metadata.
// Returns the Resolution JSON with code_default, runtime_override, and effective.
// Returns 404 if the asset has no asset_versions row (asset does not exist).
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	col := r.URL.Query().Get("column")
	var colPtr *string
	if col != "" {
		colPtr = &col
	}

	// Check asset existence via asset_versions.
	exists, err := h.store.ent.AssetVersion.Query().
		Where(assetversion.AssetEQ(name)).
		Exist(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if !exists {
		writeProblem(w, http.StatusNotFound, "asset_not_found",
			fmt.Sprintf("asset %q has no version history", name))
		return
	}

	res, err := h.store.Get(r.Context(), name, colPtr)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// patch is the shared implementation for PatchAsset and PatchColumn.
func (h *Handler) patch(w http.ResponseWriter, r *http.Request, name string, col *string) {
	// Require authenticated principal (RequireRole("governance") is enforced at
	// router level; handler additionally rejects missing principal for defense-in-depth).
	principal, ok := auth.PrincipalFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "authentication_required", "authentication required")
		return
	}

	var body patchBody
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&body); err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_body", "request body must be valid JSON")
		return
	}

	if body.Tags != nil && len(*body.Tags) > MaxTags {
		writeProblem(w, http.StatusBadRequest, "tags_too_many",
			fmt.Sprintf("tags exceeds maximum of %d entries", MaxTags))
		return
	}

	merge := r.URL.Query().Get("merge") == "true"

	// Load before-state for audit event.
	before, _ := h.store.Get(r.Context(), name, col)

	after, err := h.store.Put(r.Context(), PutInput{
		Asset:       name,
		Column:      col,
		Description: body.Description,
		Owner:       body.Owner,
		Tags:        body.Tags,
		SetBy:       principal.UserID,
		Merge:       merge,
	})
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}

	// Emit metadata.updated event (D-17, D-21). Failures are non-fatal.
	payload := event.MetadataUpdatedPayload{
		Asset:       name,
		Column:      col,
		ActorID:     principal.UserID.String(),
		BeforeDesc:  before.Effective.Description,
		BeforeOwner: before.Effective.Owner,
		BeforeTags:  before.Effective.Tags,
		AfterDesc:   after.Description,
		AfterOwner:  after.Owner,
		AfterTags:   after.Tags,
		Merge:       merge,
	}
	_ = h.events.Append(r.Context(), event.Event{
		Type:         event.EventTypeMetadataUpdated,
		OccurredAt:   time.Now().UTC(),
		ActorID:      &principal.UserID,
		ResourceType: "asset_metadata",
		ResourceID:   name,
		Payload:      payload,
	})

	writeJSON(w, http.StatusOK, map[string]any{"effective": after})
}

// writeProblem writes an RFC 7807 problem+json response.
func writeProblem(w http.ResponseWriter, status int, errType, detail string) {
	prob := map[string]any{
		"type":   errType,
		"status": status,
		"detail": detail,
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(prob)
}

// writeJSON writes v as JSON with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
