package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"pingone-promote/internal/pingone"
	"pingone-promote/internal/store"
)

// qeMock is a loopback-only configurable PingOne stand-in used for manual and
// Playwright QE runs. It is driven through /__fixture/config and never
// contacts a real PingOne service.
type qeMock struct {
	mu    sync.Mutex
	url   string
	cfg   qeConfig
	log   []string
	proms map[string]*qeProm
	next  int
}
type qeConfig struct {
	Names     []string          `json:"names"`     // names of the first resources in each env
	Count     int               `json:"count"`     // total resources per env
	PageSize  int               `json:"pageSize"`  // embeddedSnapshots / steps page size
	Truncate  bool              `json:"truncate"`  // claim more resources than delivered
	Fail      map[string]int    `json:"fail"`      // path substring -> forced HTTP status
	FailOnce  map[string]int    `json:"failOnce"`  // same but only the next matching request
	DelayMS   map[string]int    `json:"delayMs"`   // path substring -> delay
	Malformed map[string]bool   `json:"malformed"` // path substring -> invalid JSON body
	Org       map[string]string `json:"org"`       // env -> organization id
	StepCount int               `json:"stepCount"` // extra filler steps beyond the selection (0 = none)
	Slow      int               `json:"slowExecuteMS"`
	Pending   int               `json:"pendingPolls"` // GETs returning IN_PROGRESS after execute
}
type qeProm struct {
	id, status, prev string
	body             map[string]any
	polls            int
}

// qeMatch reports whether key ("substring" or "METHOD substring") matches the request.
func qeMatch(key string, r *http.Request) bool {
	if method, rest, ok := strings.Cut(key, " "); ok {
		return r.Method == method && strings.Contains(r.URL.Path, rest)
	}
	return strings.Contains(r.URL.Path, key)
}
func (m *qeMock) name(env string, i int) string {
	if i < len(m.cfg.Names) {
		return m.cfg.Names[i]
	}
	return fmt.Sprintf("Resource %03d", i)
}
func (m *qeMock) count() int {
	if m.cfg.Count > 0 {
		return m.cfg.Count
	}
	return 6
}
func (m *qeMock) rid(env string, i int) string {
	if env == "src" {
		return fmt.Sprintf("s%03d", i)
	}
	return fmt.Sprintf("d%03d", i)
}
func (m *qeMock) rtype(i int) string {
	return []string{"flows", "connections", "notificationTemplates"}[i%3]
}

func (m *qeMock) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	p := r.URL.Path
	if p == "/__fixture/config" {
		var c qeConfig
		if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
			m.mu.Unlock()
			http.Error(w, err.Error(), 400)
			return
		}
		m.cfg = c
		m.mu.Unlock()
		w.Write([]byte(`{}`))
		return
	}
	if p == "/__fixture/log" {
		out := strings.Join(m.log, "\n")
		m.log = nil
		m.mu.Unlock()
		w.Write([]byte(out))
		return
	}
	m.log = append(m.log, r.Method+" "+p+" "+r.Header.Get("Content-Type"))
	var delay time.Duration
	for k, v := range m.cfg.DelayMS {
		if qeMatch(k, r) {
			delay = time.Duration(v) * time.Millisecond
		}
	}
	for k, code := range m.cfg.FailOnce {
		if qeMatch(k, r) {
			delete(m.cfg.FailOnce, k)
			m.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			w.Write([]byte(`{"code":"FORCED","message":"forced failure"}`))
			return
		}
	}
	for k, code := range m.cfg.Fail {
		if qeMatch(k, r) {
			m.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			w.Write([]byte(`{"code":"FORCED","message":"forced failure"}`))
			return
		}
	}
	for k, bad := range m.cfg.Malformed {
		if bad && qeMatch(k, r) {
			m.mu.Unlock()
			w.Write([]byte(`{"oops": [`))
			return
		}
	}
	defer m.mu.Unlock()
	if delay > 0 {
		m.mu.Unlock()
		time.Sleep(delay)
		m.mu.Lock()
	}
	write := func(v any) { w.Header().Set("Content-Type", "application/json"); json.NewEncoder(w).Encode(v) }
	env := "src"
	if strings.Contains(p, "/environments/dst") {
		env = "dst"
	}
	switch {
	case p == "/v1/environments/src" || p == "/v1/environments/dst":
		org := "tenant"
		if o := m.cfg.Org[env]; o != "" {
			org = o
		}
		write(map[string]any{"id": env, "organization": map[string]string{"id": org}})
	case strings.HasSuffix(p, "/promotionMappings/dst"):
		mp := map[string]string{}
		for i := 0; i < m.count(); i += 2 {
			mp[m.rid("src", i)] = m.rid("dst", i)
		}
		write(map[string]any{"resourceMappings": mp})
	case strings.HasSuffix(p, "/snapshots") && r.Method == "POST":
		write(map[string]any{"resource": map[string]string{"id": env}, "snapshotId": "v1", "status": "COMPLETE"})
	case strings.HasSuffix(p, "/embeddedSnapshots"):
		start, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
		size := m.cfg.PageSize
		if size <= 0 {
			size = 1000
		}
		total := m.count()
		end := min(start+size, total)
		var items []any
		for i := start; i < end; i++ {
			id := m.rid(env, i)
			nm := m.name(env, i)
			items = append(items, map[string]any{"id": "rec-" + id, "resource": map[string]string{"id": id}, "snapshotId": "v1", "status": "COMPLETE", "resourceUrl": m.url + "/v1/environments/" + env + "/" + m.rtype(i) + "/" + id, "configuration": map[string]any{"id": id, "name": nm, "clientSecret": "TOPSECRET-" + id}, "referencedResources": []string{}})
		}
		out := map[string]any{"_embedded": map[string]any{"referencedResources": items}, "count": total}
		if m.cfg.Truncate {
			out["count"] = total + 5
		}
		if end < total {
			out["_links"] = map[string]any{"next": map[string]string{"href": m.url + p + "?cursor=" + strconv.Itoa(end)}}
		}
		write(out)
	case p == "/v1/environments/src/promotions" && r.Method == "POST":
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		m.next++
		id := fmt.Sprintf("p%d", m.next)
		m.proms[id] = &qeProm{id: id, status: "READY", body: body}
		write(m.meta(id))
	case strings.HasSuffix(p, "/promotionSteps"):
		id := p[strings.Index(p, "/promotions/")+len("/promotions/"):]
		id = strings.TrimSuffix(id, "/promotionSteps")
		pr := m.proms[id]
		if pr == nil {
			http.Error(w, "no promotion", 404)
			return
		}
		var steps []any
		sel, _ := pr.body["selectedResources"].([]any)
		mapping, _ := pr.body["resourceMapping"].(map[string]any)
		ids := []string{}
		for _, s := range sel {
			ids = append(ids, s.(string))
		}
		sort.Strings(ids)
		for _, sid := range ids {
			i, _ := strconv.Atoi(sid[1:])
			did, _ := mapping[sid].(string)
			if pr.prev != "" {
				steps = append(steps, map[string]any{"action": "PUT", "resourceUrl": m.url + "/v1/environments/dst/" + m.rtype(i) + "/" + m.rid("dst", i), "targetResource": map[string]string{"id": m.rid("dst", i)}, "payload": map[string]any{"name": "old"}})
				continue
			}
			step := map[string]any{"action": "POST", "sourceResource": map[string]string{"id": sid}, "resourceUrl": m.url + "/v1/environments/dst/" + m.rtype(i), "payload": map[string]any{"name": m.name("src", i), "clientSecret": "TOPSECRET"}}
			if did != "" {
				step["action"] = "PUT"
				step["resourceUrl"] = m.url + "/v1/environments/dst/" + m.rtype(i) + "/" + did
				step["targetResource"] = map[string]string{"id": did}
			}
			steps = append(steps, step)
		}
		start, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
		size := m.cfg.PageSize
		if size <= 0 {
			size = 1000
		}
		end := min(start+size, len(steps))
		out := map[string]any{"_embedded": map[string]any{"promotionSteps": steps[start:end]}, "count": len(steps)}
		if end < len(steps) {
			out["_links"] = map[string]any{"next": map[string]string{"href": m.url + p + "?cursor=" + strconv.Itoa(end)}}
		}
		write(out)
	case strings.Contains(p, "/promotions/"):
		id := p[strings.Index(p, "/promotions/")+len("/promotions/"):]
		pr := m.proms[id]
		if pr == nil {
			http.Error(w, `{"code":"NOT_FOUND","message":"nope"}`, 404)
			return
		}
		if r.Method == "POST" {
			switch r.Header.Get("Content-Type") {
			case pingone.ExecuteType:
				if m.cfg.Pending > 0 {
					pr.status = "IN_PROGRESS"
					pr.polls = m.cfg.Pending
				} else {
					pr.status = "COMPLETED"
				}
				if pr.prev != "" && pr.status == "COMPLETED" {
					m.proms[pr.prev].status = "ROLLED_BACK"
				}
			case pingone.RollbackType:
				m.next++
				nid := fmt.Sprintf("p%d", m.next)
				m.proms[nid] = &qeProm{id: nid, status: "READY", prev: id, body: pr.body}
				write(m.meta(nid))
				return
			default:
				http.Error(w, "wrong media type", 400)
				return
			}
		} else if pr.status == "IN_PROGRESS" {
			pr.polls--
			if pr.polls <= 0 {
				pr.status = "COMPLETED"
			}
		}
		write(m.meta(id))
	default:
		http.Error(w, "unexpected fixture request "+r.Method+" "+p, 404)
	}
}
func (m *qeMock) meta(id string) map[string]any {
	pr := m.proms[id]
	out := map[string]any{"errors": []any{}, "id": id, "status": pr.status, "sourceEnvironment": map[string]string{"id": "src"}, "targetEnvironment": map[string]string{"id": "dst"}, "sourceSnapshot": map[string]string{"id": "v1"}}
	if pr.prev != "" {
		out["previousPromotion"] = map[string]string{"id": pr.prev}
	}
	return out
}

// TestQEFixture serves the real application handler (static files read from
// disk) in front of qeMock. Only runs when QE_FIXTURE_FILE is set.
func TestQEFixture(t *testing.T) {
	path := os.Getenv("QE_FIXTURE_FILE")
	if path == "" {
		t.Skip("QE-only fixture")
	}
	st, err := store.Open(os.Getenv("QE_DATA_DIR"))
	if err != nil {
		t.Fatal(err)
	}
	m := &qeMock{proms: map[string]*qeProm{}}
	native := httptest.NewTLSServer(m)
	defer native.Close()
	m.url = native.URL
	srv := New(st, "qe-token")
	// This loopback-only fixture exercises local deployment controls; real
	// environments remain GitLab-only unless explicitly allowlisted.
	srv.localTargets["dst"] = true
	srv.clientFactory = func(p pingone.Profile, secret string) (*pingone.Client, error) {
		return &pingone.Client{BaseURL: native.URL, HTTP: native.Client(), AccessToken: "test-token", Profile: p}, nil
	}
	srv.files = http.FileServer(http.Dir("web"))
	ts := httptest.NewServer(srv)
	defer ts.Close()
	raw, _ := json.Marshal(map[string]string{"url": ts.URL + "/?token=qe-token", "native": native.URL})
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("QE fixture ready")
	select {}
}
