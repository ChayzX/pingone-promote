// Package davincitest serves recorded DaVinci API fixtures over httptest for
// tests of the inventory reader, translator and promotion engine. It is used
// only from _test.go files.
package davincitest

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"pingone-promote/internal/pingone"
)

// Token is the bearer token the fixture server requires.
const Token = "davincitest-token"

// Server is a fixture-backed DaVinci API. Status overrides a response status
// by path suffix (for example "/variables" -> 403). Requests records every
// request path.
type Server struct {
	*httptest.Server
	Dir      string
	EnvID    string
	mu       sync.Mutex
	Status   map[string]int
	Requests []string
}

// New serves dir for envID and returns the server and a client bound to it.
func New(t testing.TB, dir, envID string) (*Server, *pingone.Client) {
	t.Helper()
	s := &Server{Dir: dir, EnvID: envID, Status: map[string]int{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.Close)
	client := &pingone.Client{BaseURL: s.URL, AccessToken: Token, HTTP: s.Client()}
	return s, client
}

// SetStatus makes requests whose path ends with suffix return status.
func (s *Server) SetStatus(suffix string, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Status[suffix] = status
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.Requests = append(s.Requests, r.Method+" "+r.URL.RequestURI())
	overrides := map[string]int{}
	for k, v := range s.Status {
		overrides[k] = v
	}
	s.mu.Unlock()
	if r.Method != http.MethodGet {
		http.Error(w, `{"code":"METHOD_NOT_ALLOWED","message":"fixture server is read-only"}`, http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+Token {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"code":"UNAUTHORIZED","message":"bad token"}`))
		return
	}
	prefix := "/v1/environments/" + s.EnvID
	path, ok := strings.CutPrefix(r.URL.Path, prefix)
	if !ok {
		http.NotFound(w, r)
		return
	}
	for suffix, status := range overrides {
		if strings.HasSuffix(path, suffix) {
			w.WriteHeader(status)
			w.Write([]byte(`{"code":"REQUEST_FAILED","message":"fixture override"}`))
			return
		}
	}
	file := ""
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case len(parts) == 1 && parts[0] == "variables":
		file = "variables_page1.json"
		if r.URL.Query().Get("cursor") == "p2" {
			file = "variables_page2.json"
		}
	case len(parts) == 1:
		file = parts[0] + ".json"
	case len(parts) == 2 && parts[0] == "flows":
		file = "flow_" + parts[1] + ".json"
	case len(parts) == 3 && parts[0] == "davinciApplications" && parts[2] == "flowPolicies":
		file = "flowPolicies_" + parts[1] + ".json"
	}
	data, err := os.ReadFile(filepath.Join(s.Dir, filepath.Base(file)))
	if file == "" || err != nil {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"code":"NOT_FOUND","message":"no fixture"}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}
