package testharness

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"
)

// SnowflakeMock emulates the Snowflake REST API for testing.
// It records executed DDL statements and can be configured to return
// fake responses for specific SQL patterns.
type SnowflakeMock struct {
	*httptest.Server
	t          *testing.T
	mu         sync.Mutex
	statements []string
	fakes      map[*regexp.Regexp]interface{}
}

// NewSnowflakeMock returns a mock Snowflake server.
func NewSnowflakeMock(t *testing.T) *SnowflakeMock {
	t.Helper()
	m := &SnowflakeMock{t: t, statements: []string{}, fakes: map[*regexp.Regexp]interface{}{}}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/statements/", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Statement string `json:"statement"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		m.mu.Lock()
		m.statements = append(m.statements, body.Statement)
		m.mu.Unlock()

		// Check if a fake response is registered for this SQL.
		m.mu.Lock()
		var resp any = map[string]any{"data": []any{}, "resultSetMetaData": map[string]any{"numRows": 0}}
		for pattern, fake := range m.fakes {
			if pattern.MatchString(body.Statement) {
				resp = fake
				break
			}
		}
		m.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	m.Server = httptest.NewServer(mux)
	return m
}

// LastDDL returns the most recently executed DDL statement.
func (m *SnowflakeMock) LastDDL() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.statements) == 0 {
		return ""
	}
	return m.statements[len(m.statements)-1]
}

// AllDDL returns all executed DDL statements in order.
func (m *SnowflakeMock) AllDDL() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.statements))
	copy(out, m.statements)
	return out
}

// RegisterPattern associates a SQL regex pattern with a fake JSON response.
func (m *SnowflakeMock) RegisterPattern(pattern string, fakeResponse any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fakes[regexp.MustCompile(pattern)] = fakeResponse
}
