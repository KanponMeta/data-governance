package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteProblem(t *testing.T) {
	rr := httptest.NewRecorder()
	WriteProblem(rr, http.StatusBadRequest, "Bad Request", "test detail")

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("expected application/problem+json, got %q", ct)
	}

	var prob Problem
	if err := json.Unmarshal(rr.Body.Bytes(), &prob); err != nil {
		t.Fatalf("failed to unmarshal problem: %v", err)
	}
	if prob.Type != "about:blank" {
		t.Errorf("expected type about:blank, got %q", prob.Type)
	}
	if prob.Title != "Bad Request" {
		t.Errorf("expected title 'Bad Request', got %q", prob.Title)
	}
	if prob.Status != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, prob.Status)
	}
	if prob.Detail != "test detail" {
		t.Errorf("expected detail 'test detail', got %q", prob.Detail)
	}
}

func TestBadRequest(t *testing.T) {
	rr := httptest.NewRecorder()
	BadRequest(rr, "something is wrong")

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

func TestUnauthorized(t *testing.T) {
	rr := httptest.NewRecorder()
	Unauthorized(rr, "invalid token")

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}

func TestForbidden(t *testing.T) {
	rr := httptest.NewRecorder()
	Forbidden(rr, "insufficient permissions")

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rr.Code)
	}
}

func TestNotFound(t *testing.T) {
	rr := httptest.NewRecorder()
	NotFound(rr, "resource not found")

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rr.Code)
	}
}

func TestConflict(t *testing.T) {
	rr := httptest.NewRecorder()
	Conflict(rr, "already exists")

	if rr.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d", rr.Code)
	}
}

func TestGone(t *testing.T) {
	rr := httptest.NewRecorder()
	Gone(rr, "resource deleted")

	if rr.Code != http.StatusGone {
		t.Errorf("expected 410, got %d", rr.Code)
	}
}

func TestInternalServerError(t *testing.T) {
	rr := httptest.NewRecorder()
	InternalServerError(rr, "internal error")

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rr.Code)
	}
}

func TestServiceUnavailable(t *testing.T) {
	rr := httptest.NewRecorder()
	ServiceUnavailable(rr, "service down")

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rr.Code)
	}
}
