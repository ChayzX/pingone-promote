// Package pingonetest is a fake PingOne Platform API for tests. It serves a
// tenant described by a JSON fixture:
//
//	{
//	  "environmentId": "<uuid>",
//	  "collections": { "<path>": { "embedded": "<key>", "items": [ {...} ] } },
//	  "objects":     { "<path>": {...} }
//	}
//
// Collections are paged two items at a time so pagination is exercised.
// Only GET is accepted on API paths; every request is recorded.
package pingonetest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Tenant is a fixture.
type Tenant struct {
	EnvironmentID string `json:"environmentId"`
	Collections   map[string]struct {
		Embedded string           `json:"embedded"`
		Items    []map[string]any `json:"items"`
	} `json:"collections"`
	Objects map[string]map[string]any `json:"objects"`
	// Forbidden paths answer 403 (an unlicensed service).
	Forbidden []string `json:"forbidden,omitempty"`
	// Failing paths answer the given HTTP status (an unexpected answer).
	Failing map[string]int `json:"failing,omitempty"`
	// Raw paths answer 200 with this body as XML (not JSON).
	Raw map[string]string `json:"raw,omitempty"`
	// Throttle answers this many requests to each API path with 429 (and
	// Retry-After: 0) before answering normally.
	Throttle int `json:"throttle,omitempty"`
}

// Fake is a running fake.
type Fake struct {
	*httptest.Server
	Tenant       Tenant
	ClientID     string
	ClientSecret string
	WorkerEnvID  string

	mu        sync.Mutex
	Requests  []string // "METHOD path?query"
	throttled map[string]int
	// NonGET counts requests other than GET on the API (must stay 0).
	NonGET int
}

// LoadTenant reads a fixture file.
func LoadTenant(t testing.TB, path string) Tenant {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var tn Tenant
	if err := json.Unmarshal(raw, &tn); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return tn
}

// Start serves a tenant.
func Start(t testing.TB, tn Tenant) *Fake {
	t.Helper()
	f := &Fake{Tenant: tn, ClientID: "fake-client", ClientSecret: "fake-secret-value-not-real", WorkerEnvID: "99999999-9999-4999-8999-999999999999"}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.Requests = append(f.Requests, r.Method+" "+r.URL.RequestURI())
	f.mu.Unlock()
	if strings.HasSuffix(r.URL.Path, "/as/token") {
		id, secret, ok := r.BasicAuth()
		if r.Method != http.MethodPost || !ok || id != f.ClientID || secret != f.ClientSecret || r.URL.Path != "/auth/"+f.WorkerEnvID+"/as/token" {
			http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
			return
		}
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "client_credentials" {
			http.Error(w, `{"error":"unsupported_grant_type"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"fake-access-token","token_type":"Bearer","expires_in":3600}`))
		return
	}
	if r.Method != http.MethodGet {
		f.mu.Lock()
		f.NonGET++
		f.mu.Unlock()
		http.Error(w, "read-only fake", http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("Authorization") != "Bearer fake-access-token" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	prefix := "/api/v1/environments/" + f.Tenant.EnvironmentID + "/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		http.NotFound(w, r)
		return
	}
	p := strings.TrimPrefix(r.URL.Path, prefix)
	for _, fp := range f.Tenant.Forbidden {
		if p == fp || strings.HasPrefix(p, fp+"/") {
			http.Error(w, `{"code":"ACCESS_FAILED"}`, http.StatusForbidden)
			return
		}
	}
	if f.Tenant.Throttle > 0 {
		f.mu.Lock()
		if f.throttled == nil {
			f.throttled = map[string]int{}
		}
		n := f.throttled[r.URL.RequestURI()]
		f.throttled[r.URL.RequestURI()] = n + 1
		f.mu.Unlock()
		if n < f.Tenant.Throttle {
			w.Header().Set("Retry-After", "0")
			http.Error(w, `{"code":"REQUEST_LIMITED"}`, http.StatusTooManyRequests)
			return
		}
	}
	if status, ok := f.Tenant.Failing[p]; ok {
		http.Error(w, `{"code":"INVALID_REQUEST"}`, status)
		return
	}
	if body, ok := f.Tenant.Raw[p]; ok {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(body))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if c, ok := f.Tenant.Collections[p]; ok {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		start, end := page*2, page*2+2
		if start > len(c.Items) {
			start = len(c.Items)
		}
		if end > len(c.Items) {
			end = len(c.Items)
		}
		doc := map[string]any{"_embedded": map[string]any{c.Embedded: c.Items[start:end]}}
		if end < len(c.Items) {
			doc["_links"] = map[string]any{"next": map[string]any{"href": f.URL + r.URL.Path + "?page=" + strconv.Itoa(page+1)}}
		}
		_ = json.NewEncoder(w).Encode(doc)
		return
	}
	if o, ok := f.Tenant.Objects[p]; ok {
		_ = json.NewEncoder(w).Encode(o)
		return
	}
	http.Error(w, `{"code":"NOT_FOUND"}`, http.StatusNotFound)
}

// AuthBase and APIBase are the client overrides for this fake.
func (f *Fake) AuthBase() string { return f.URL + "/auth" }

// APIBase is the API root for this fake.
func (f *Fake) APIBase() string { return f.URL + "/api/v1" }

// Writes reports how many non-GET API requests were made.
func (f *Fake) Writes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.NonGET
}
