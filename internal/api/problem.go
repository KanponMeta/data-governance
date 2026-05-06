package api

import (
	"encoding/json"
	"net/http"
)

// Problem represents an RFC 7807 error response.
type Problem struct {
	// Type is a URI reference that identifies the problem type.
	// When absenct, defaults to "about:blank".
	Type string `json:"type,omitempty"`
	// Title is a short, human-readable summary of the problem type.
	Title string `json:"title"`
	// Status is the HTTP status code.
	Status int `json:"status"`
	// Detail is an explanation specific to this occurrence of the problem.
	Detail string `json:"detail,omitempty"`
	// Instance is a URI reference that identifies the specific occurrence.
	Instance string `json:"instance,omitempty"`
}

// WriteProblem writes an RFC 7807 problem+json response to w.
func WriteProblem(w http.ResponseWriter, status int, title, detail string) {
	prob := Problem{
		Type:   "about:blank",
		Title:  title,
		Status: status,
		Detail: detail,
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(prob)
}

// BadRequest writes a 400 problem+json response.
func BadRequest(w http.ResponseWriter, detail string) {
	WriteProblem(w, http.StatusBadRequest, "Bad Request", detail)
}

// Unauthorized writes a 401 problem+json response.
func Unauthorized(w http.ResponseWriter, detail string) {
	WriteProblem(w, http.StatusUnauthorized, "Unauthorized", detail)
}

// Forbidden writes a 403 problem+json response.
func Forbidden(w http.ResponseWriter, detail string) {
	WriteProblem(w, http.StatusForbidden, "Forbidden", detail)
}

// NotFound writes a 404 problem+json response.
func NotFound(w http.ResponseWriter, detail string) {
	WriteProblem(w, http.StatusNotFound, "Not Found", detail)
}

// Conflict writes a 409 problem+json response.
func Conflict(w http.ResponseWriter, detail string) {
	WriteProblem(w, http.StatusConflict, "Conflict", detail)
}

// Gone writes a 410 problem+json response.
func Gone(w http.ResponseWriter, detail string) {
	WriteProblem(w, http.StatusGone, "Gone", detail)
}

// InternalServerError writes a 500 problem+json response.
func InternalServerError(w http.ResponseWriter, detail string) {
	WriteProblem(w, http.StatusInternalServerError, "Internal Server Error", detail)
}

// ServiceUnavailable writes a 503 problem+json response.
func ServiceUnavailable(w http.ResponseWriter, detail string) {
	WriteProblem(w, http.StatusServiceUnavailable, "Service Unavailable", detail)
}
