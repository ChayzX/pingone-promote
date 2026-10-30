package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"pingone-promote/internal/pingone"
	"pingone-promote/internal/store"
	"strings"
	"sync"
	"testing"
)

type nativeFixture struct {
	mu              sync.Mutex
	url             string
	hidden, changed bool
	executed        []string
	statuses        map[string]string
	created         map[string]any
	// mapping, when set, replaces the resourceMapping the fixture reports for
	// promotions, e.g. to model PingOne reporting resolved destination IDs.
	mapping map[string]string
}

func (f *nativeFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.URL.Path
	if p == "/__fixture/hidden" {
		f.hidden = r.URL.Query().Get("value") == "true"
		w.Write([]byte(`{}`))
		return
	}
	write := func(v any) { w.Header().Set("Content-Type", "application/json"); json.NewEncoder(w).Encode(v) }
	if strings.HasSuffix(p, "/promotionMappings/dst") {
		write(map[string]any{"resourceMappings": map[string]string{"f": "g", "c": "d"}})
		return
	}
	if p == "/v1/environments/src" || p == "/v1/environments/dst" {
		write(map[string]any{"id": strings.TrimPrefix(p, "/v1/environments/"), "organization": map[string]string{"id": "tenant"}})
		return
	}
	env := "src"
	if strings.Contains(p, "/environments/dst") {
		env = "dst"
	}
	if strings.HasSuffix(p, "/snapshots") && r.Method == "POST" {
		write(map[string]any{"resource": map[string]string{"id": env}, "snapshotId": "v1", "status": "COMPLETE"})
		return
	}
	if strings.HasSuffix(p, "/embeddedSnapshots") {
		ids := []string{"f", "c", "n"}
		if env == "dst" {
			ids = []string{"g", "d", "m"}
		}
		types := []string{"flows", "connections", "templates"}
		names := []string{"Customer login", "Identity connector", "Email template"}
		var resources []any
		for i, id := range ids {
			resources = append(resources, map[string]any{"id": "record-" + id, "resource": map[string]string{"id": id}, "snapshotId": "v1", "status": "COMPLETE", "resourceUrl": f.url + "/v1/environments/" + env + "/" + types[i] + "/" + id, "configuration": map[string]string{"id": id, "name": names[i]}, "referencedResources": []string{}})
		}
		write(map[string]any{"_embedded": map[string]any{"referencedResources": resources}, "count": 3})
		return
	}
	if p == "/v1/environments/src/promotions" && r.Method == "POST" {
		json.NewDecoder(r.Body).Decode(&f.created)
		f.statuses["p1"] = "READY"
		write(f.metadata("p1"))
		return
	}
	id := "p1"
	if strings.Contains(p, "/rb1") {
		id = "rb1"
	}
	if strings.HasSuffix(p, "/promotionSteps") {
		body := `{"name":"promoted"}`
		if f.changed {
			body = `{"name":"changed after preview"}`
		}
		step := map[string]any{"action": "PUT", "sourceResource": map[string]string{"id": "f"}, "targetResource": map[string]string{"id": "g"}, "resourceUrl": f.url + "/v1/environments/dst/flows/g", "payload": json.RawMessage(body)}
		if id == "rb1" {
			step["payload"] = map[string]string{"name": "old"}
		}
		out := map[string]any{"_embedded": map[string]any{"promotionSteps": []any{step}}, "count": 1}
		if f.hidden && id == "p1" {
			out["count"] = 2
			if r.URL.Query().Get("cursor") == "2" {
				out["_embedded"] = map[string]any{"promotionSteps": []any{map[string]any{"action": "PUT", "sourceResource": map[string]string{"id": "n"}, "resourceUrl": f.url + "/v1/environments/dst/templates/m", "payload": map[string]string{"body": "unapproved"}}}}
			} else {
				out["_links"] = map[string]any{"next": map[string]string{"href": f.url + p + "?cursor=2"}}
			}
		}
		write(out)
		return
	}
	if strings.Contains(p, "/promotions/") {
		if r.Method == "POST" {
			switch r.Header.Get("Content-Type") {
			case pingone.ExecuteType:
				f.executed = append(f.executed, id)
				f.statuses[id] = "COMPLETED"
				if id == "rb1" {
					f.statuses["p1"] = "ROLLED_BACK"
				}
			case pingone.RollbackType:
				id = "rb1"
				f.statuses[id] = "READY"
			default:
				http.Error(w, "wrong media type", 400)
				return
			}
		}
		write(f.metadata(id))
		return
	}
	http.Error(w, "unexpected fixture request "+r.Method+" "+p, 404)
}
func (f *nativeFixture) metadata(id string) map[string]any {
	p := map[string]any{"errors": []any{}, "id": id, "status": f.statuses[id], "sourceEnvironment": map[string]string{"id": "src"}, "targetEnvironment": map[string]string{"id": "dst"}, "sourceSnapshot": map[string]string{"id": "v1"}, "resourceMapping": map[string]string{"f": "g", "c": "d"}}
	if f.mapping != nil {
		p["resourceMapping"] = f.mapping
	}
	if id == "rb1" {
		p["previousPromotion"] = map[string]string{"id": "p1"}
	}
	return p
}

type harness struct {
	t              *testing.T
	app            *Server
	f              *nativeFixture
	server         *httptest.Server
	http           *http.Client
	csrf           string
	source, target pingone.Profile
}

func setup(t *testing.T) *harness {
	t.Helper()
	t.Setenv("TEST_PING_SECRET", "secret")
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	src, e := st.SaveProfile(pingone.Profile{Name: "Development", Region: "us", EnvironmentID: "src", ClientID: "worker", SecretMode: "env", SecretEnv: "TEST_PING_SECRET"}, "")
	if e != nil {
		t.Fatal(e)
	}
	dst, e := st.SaveProfile(pingone.Profile{Name: "Production", Region: "us", EnvironmentID: "dst", ClientID: "worker", SecretMode: "env", SecretEnv: "TEST_PING_SECRET"}, "")
	if e != nil {
		t.Fatal(e)
	}
	f := &nativeFixture{statuses: map[string]string{}}
	native := httptest.NewTLSServer(f)
	f.url = native.URL
	t.Cleanup(native.Close)
	app := New(st, "bootstrap-token")
	app.clientFactory = func(p pingone.Profile, secret string) (*pingone.Client, error) {
		return &pingone.Client{BaseURL: native.URL, HTTP: native.Client(), AccessToken: "test-token", Profile: p}, nil
	}
	ts := httptest.NewServer(app)
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	h := &harness{t: t, app: app, f: f, server: ts, http: &http.Client{Jar: jar}, source: src, target: dst}
	res, e := h.http.Get(ts.URL + "/?token=bootstrap-token")
	if e != nil {
		t.Fatal(e)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	var session struct {
		CSRF string `json:"csrf"`
	}
	h.call("GET", "/api/session", nil, &session, 200)
	h.csrf = session.CSRF
	return h
}
func (h *harness) call(method, path string, body, out any, status int) {
	h.t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, h.server.URL+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", h.csrf)
	req.Header.Set("Origin", h.server.URL)
	res, e := h.http.Do(req)
	if e != nil {
		h.t.Fatal(e)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	if res.StatusCode != status {
		h.t.Fatalf("%s %s HTTP %d want %d: %s", method, path, res.StatusCode, status, data)
	}
	if out != nil {
		if e = json.Unmarshal(data, out); e != nil {
			h.t.Fatalf("decode %s: %v: %s", path, e, data)
		}
	}
}
func (h *harness) plan() PlanView {
	h.t.Helper()
	var s, d struct {
		ID string `json:"id"`
	}
	h.call("POST", "/api/inventory", map[string]string{"profileId": h.source.ID}, &s, 200)
	h.call("POST", "/api/inventory", map[string]string{"profileId": h.target.ID}, &d, 200)
	var v PlanView
	h.call("POST", "/api/plans", map[string]any{"sourceInventoryId": s.ID, "targetInventoryId": d.ID, "selected": []string{"f"}, "mappings": map[string]string{"f": "g", "c": "d"}, "description": "release"}, &v, 200)
	return v
}
func TestWorkflowSelectionAndDuplicateDeployment(t *testing.T) {
	h := setup(t)
	v := h.plan()
	if !v.CanDeploy {
		t.Fatalf("valid plan blocked %+v", v)
	}
	if fmt.Sprint(h.f.created["excludedResources"]) != "[c n]" {
		t.Fatalf("bad exclusions %+v", h.f.created)
	}
	var deployed PlanView
	h.call("POST", "/api/plans/"+v.ID+"/deploy", map[string]any{"digest": v.Review.Digest, "confirm": true}, &deployed, 200)
	if deployed.Status != "COMPLETED" || len(deployed.Review.Issues) > 0 {
		t.Fatalf("successful deployment reported as issue: %+v", deployed)
	}
	h.call("POST", "/api/plans/"+v.ID+"/deploy", map[string]any{"digest": v.Review.Digest, "confirm": true}, nil, 409)
	if len(h.f.executed) != 1 {
		t.Fatal("execute was replayed")
	}
}
func TestUnselectedTemplateOnLaterPageNeverExecutes(t *testing.T) {
	h := setup(t)
	h.f.hidden = true
	v := h.plan()
	if v.CanDeploy || len(v.Review.Issues) == 0 {
		t.Fatalf("template not blocked %+v", v)
	}
	h.call("POST", "/api/plans/"+v.ID+"/deploy", map[string]any{"digest": v.Review.Digest, "confirm": true}, nil, 409)
	if len(h.f.executed) != 0 {
		t.Fatal("blocked template was executed")
	}
}
func TestChangedNativePlanRequiresNewReview(t *testing.T) {
	h := setup(t)
	v := h.plan()
	h.f.mu.Lock()
	h.f.changed = true
	h.f.mu.Unlock()
	h.call("POST", "/api/plans/"+v.ID+"/deploy", map[string]any{"digest": v.Review.Digest, "confirm": true}, nil, 409)
	if len(h.f.executed) != 0 {
		t.Fatal("changed plan executed")
	}
}
func TestRollbackCreatesDraftThenExecutesSeparatePromotion(t *testing.T) {
	h := setup(t)
	v := h.plan()
	h.call("POST", "/api/plans/"+v.ID+"/deploy", map[string]any{"digest": v.Review.Digest, "confirm": true}, nil, 200)
	var rb PlanView
	h.call("POST", "/api/plans/"+v.ID+"/rollback", map[string]bool{"confirm": true}, &rb, 200)
	if rb.NativeID != "rb1" || rb.Kind != "rollback" || len(h.f.executed) != 1 {
		t.Fatalf("rollback was not separately reviewed %+v", rb)
	}
	h.call("POST", "/api/plans/"+rb.ID+"/deploy", map[string]any{"digest": rb.Review.Digest, "confirm": true}, nil, 200)
	var records []store.Record
	h.call("GET", "/api/history", nil, &records, 200)
	for _, record := range records {
		if record.ID == v.ID && record.Status != "ROLLED_BACK" {
			t.Fatalf("original history still offers rollback after restoration: %+v", record)
		}
	}
	if fmt.Sprint(h.f.executed) != "[p1 rb1]" {
		t.Fatalf("wrong executed IDs %v", h.f.executed)
	}
}
func TestLocalSessionAndCSRFRequired(t *testing.T) {
	h := setup(t)
	req, _ := http.NewRequest("POST", h.server.URL+"/api/inventory", strings.NewReader(`{}`))
	res, e := h.http.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatalf("missing CSRF accepted: %d", res.StatusCode)
	}
	res, e = http.Get(h.server.URL + "/api/history")
	if e != nil {
		t.Fatal(e)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatalf("missing cookie accepted: %d", res.StatusCode)
	}
}

// TestBrowserFixture runs only on an explicit development request. It never
// contacts PingOne and adds no fixture endpoints to the production server.
func TestBrowserFixture(t *testing.T) {
	path := os.Getenv("PINGONE_BROWSER_FIXTURE_FILE")
	if path == "" {
		t.Skip("development-only browser fixture")
	}
	h := setup(t)
	h.server.Config.Handler.(*Server).files = http.FileServer(http.Dir("web"))
	raw, _ := json.Marshal(map[string]string{"url": h.server.URL + "/?token=bootstrap-token", "native": h.f.url, "source": h.source.ID, "target": h.target.ID})
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("Browser fixture ready (test data only)")
	select {}
}

func TestRestartReconstructsPinnedScopeWithoutReplayingExecution(t *testing.T) {
	h := setup(t)
	v := h.plan()
	h.call("POST", "/api/plans/"+v.ID+"/deploy", map[string]any{"digest": v.Review.Digest, "confirm": true}, nil, 200)
	original := h.server.Config.Handler.(*Server)
	restarted := New(original.store, "restarted-token")
	restarted.clientFactory = original.clientFactory
	ts := httptest.NewServer(restarted)
	defer ts.Close()
	h.server = ts
	res, err := h.http.Get(ts.URL + "/?token=restarted-token")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	var session struct {
		CSRF string `json:"csrf"`
	}
	h.call("GET", "/api/session", nil, &session, 200)
	h.csrf = session.CSRF
	var restored PlanView
	h.call("GET", "/api/plans/"+v.ID, nil, &restored, 200)
	if restored.Status != "COMPLETED" || restored.CanDeploy || restored.NativeID != "p1" {
		t.Fatalf("wrong restored status %+v", restored)
	}
	h.call("POST", "/api/plans/"+v.ID+"/deploy", map[string]any{"digest": restored.Review.Digest, "confirm": true}, nil, 409)
	if len(h.f.executed) != 1 {
		t.Fatal("restart replayed an execution")
	}
}

func TestHistoryListsNewestOperationFirst(t *testing.T) {
	h := setup(t)
	first := h.plan()
	second := h.plan()
	var records []store.Record
	h.call("GET", "/api/history", nil, &records, 200)
	if len(records) != 2 || records[0].ID != second.ID || records[1].ID != first.ID {
		t.Fatalf("history is not newest first: %+v", records)
	}
}
