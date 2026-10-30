package app

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"pingone-promote/internal/pingone"
	"strings"
	"sync"
	"testing"
	"time"
)

// gatedNative is a minimal PingOne stand-in whose snapshot creation blocks
// until released, so tests can observe what the app does while an inventory
// load is waiting on PingOne.
type gatedNative struct {
	arrived chan string
	release chan struct{}
}

func (g *gatedNative) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	env := "src"
	if strings.Contains(p, "/environments/dst") {
		env = "dst"
	}
	write := func(v any) { w.Header().Set("Content-Type", "application/json"); json.NewEncoder(w).Encode(v) }
	switch {
	case p == "/v1/environments/"+env:
		write(map[string]any{"id": env, "organization": map[string]string{"id": "tenant"}})
	case strings.HasSuffix(p, "/snapshots") && r.Method == "POST":
		g.arrived <- env
		<-g.release
		write(map[string]any{"resource": map[string]string{"id": env}, "snapshotId": "v1", "status": "COMPLETE"})
	case strings.HasSuffix(p, "/embeddedSnapshots"):
		url := "https://" + r.Host + "/v1/environments/" + env + "/flows/" + env + "-flow"
		write(map[string]any{"_embedded": map[string]any{"referencedResources": []any{map[string]any{
			"resource": map[string]string{"id": env + "-flow"}, "resourceUrl": url,
			"configuration": map[string]any{"name": "Flow " + env}, "status": "COMPLETE",
		}}}, "count": 1})
	default:
		http.NotFound(w, r)
	}
}

func (h *harness) useGatedNative(g *gatedNative) {
	native := httptest.NewTLSServer(g)
	h.t.Cleanup(native.Close)
	h.app.clientFactory = func(p pingone.Profile, secret string) (*pingone.Client, error) {
		return &pingone.Client{BaseURL: native.URL, HTTP: native.Client(), AccessToken: "test-token", Profile: p}, nil
	}
}

// post is a goroutine-safe request helper: it reports status and body instead
// of failing the test from a non-test goroutine.
func (h *harness) post(path string, body any) (int, string) {
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", h.server.URL+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", h.csrf)
	req.Header.Set("Origin", h.server.URL)
	res, err := h.http.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(data)
}

// The browser requests the source and destination inventories together. Both
// must reach PingOne at the same time instead of one waiting for the other.
func TestInventoryLoadsRunConcurrently(t *testing.T) {
	h := setup(t)
	g := &gatedNative{arrived: make(chan string, 2), release: make(chan struct{})}
	h.useGatedNative(g)
	var wg sync.WaitGroup
	statuses := make([]int, 2)
	for i, p := range []pingone.Profile{h.source, h.target} {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			statuses[i], _ = h.post("/api/inventory", map[string]string{"profileId": id})
		}(i, p.ID)
	}
	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case env := <-g.arrived:
			seen[env] = true
		case <-time.After(5 * time.Second):
			close(g.release)
			wg.Wait()
			t.Fatalf("only %v reached PingOne while another load was in flight; loads are serialized", seen)
		}
	}
	close(g.release)
	wg.Wait()
	for i, s := range statuses {
		if s != 200 {
			t.Fatalf("inventory load %d returned HTTP %d", i, s)
		}
	}
}

// A profile saved while its inventory is loading must not leave a result
// built from the old profile identity behind.
func TestInventoryDiscardedWhenProfilesChangeMidLoad(t *testing.T) {
	h := setup(t)
	g := &gatedNative{arrived: make(chan string, 1), release: make(chan struct{})}
	h.useGatedNative(g)
	done := make(chan struct{})
	var status int
	var body string
	go func() {
		status, body = h.post("/api/inventory", map[string]string{"profileId": h.source.ID})
		close(done)
	}()
	select {
	case <-g.arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("inventory load never reached PingOne")
	}
	// Other API calls must still be served while the load waits on PingOne.
	edited := h.source
	edited.Name = "Development (edited)"
	type reply struct {
		status int
		body   string
	}
	saved := make(chan reply, 1)
	go func() { s, b := h.post("/api/profiles", edited); saved <- reply{s, b} }()
	select {
	case r := <-saved:
		if r.status != 200 {
			close(g.release)
			t.Fatalf("profile save failed during an inventory load: HTTP %d %s", r.status, r.body)
		}
	case <-time.After(5 * time.Second):
		close(g.release)
		<-done
		t.Fatal("profile save was blocked behind an in-flight inventory load")
	}
	close(g.release)
	<-done
	if status != 409 || !strings.Contains(body, "changed while resources were loading") {
		t.Fatalf("want 409 discard after a mid-load profile change, got HTTP %d %s", status, body)
	}
	h.app.mu.Lock()
	n := len(h.app.inventories)
	h.app.mu.Unlock()
	if n != 0 {
		t.Fatalf("discarded load still stored %d inventory session(s)", n)
	}
}

// The UI only needs identity, name, type and dependencies. Configuration
// stays server-side for plan inspection and is not sent to the browser.
func TestInventoryResponseOmitsConfiguration(t *testing.T) {
	h := setup(t)
	status, body := h.post("/api/inventory", map[string]string{"profileId": h.source.ID})
	if status != 200 {
		t.Fatalf("inventory load: HTTP %d %s", status, body)
	}
	var view struct {
		Resources []map[string]any `json:"resources"`
	}
	if err := json.Unmarshal([]byte(body), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Resources) == 0 {
		t.Fatalf("no resources in inventory response: %s", body)
	}
	for _, res := range view.Resources {
		if _, ok := res["configuration"]; ok {
			t.Fatalf("inventory response includes configuration for %v", res["id"])
		}
		if res["name"] == "" || res["name"] == nil {
			t.Fatalf("inventory response lost the resource name for %v", res["id"])
		}
	}
	h.app.mu.Lock()
	defer h.app.mu.Unlock()
	for _, session := range h.app.inventories {
		for _, res := range session.Resources {
			if res.Configuration == nil {
				t.Fatalf("server session lost configuration for %s; plan inspection needs it", res.ID)
			}
		}
	}
}
