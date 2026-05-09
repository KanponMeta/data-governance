package api

import "github.com/kanpon/data-governance/internal/metadata"

// metadataHandler constructs a metadata.Handler from the api.Deps.
// It is called from the router to bind the PATCH/GET metadata endpoints.
// Callers must check deps.Ent != nil before calling.
func metadataHandler(deps Deps) *metadata.Handler {
	return metadata.NewHandler(metadata.NewStore(deps.Ent), deps.Events)
}
