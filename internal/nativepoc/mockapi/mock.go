// Package mockapi is an in-memory stand-in for PingOne's Configuration
// Management API, for the proof of concept's tests and demonstration. It
// implements only what nativepoc calls, with the shapes that package
// documents. It is not a model of PingOne's matching or dependency rules.
package mockapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
)

// Res is one configuration resource of a mock environment.
type Res struct {
	ID, Kind, Name string // Kind is the collection: flows, connectorInstances...
	Config         map[string]any
	Refs           []string // IDs of resources it depends on
}

type promotion struct {
	ID, Status, Source, Target, SourceSnap, TargetSnap string
	Steps                                              []map[string]any
	undo                                               []map[string]any
	executed, rolledBack                               bool
}

// Mock is the fake service.
type Mock struct {
	*httptest.Server
	mu     sync.Mutex
	Envs   map[string]map[string]Res // environment ID -> resource ID -> resource
	snaps  map[string]map[string]Res
	promos map[string]*promotion
	last   map[string]string // target environment -> last executed promotion
	n      int
	// Writes counts requests that changed an environment.
	Writes int
}

// New starts a mock with the given environments.
func New(envs map[string]map[string]Res) *Mock {
	m := &Mock{Envs: envs, snaps: map[string]map[string]Res{}, promos: map[string]*promotion{}, last: map[string]string{}}
	m.Server = httptest.NewServer(http.HandlerFunc(m.serve))
	return m
}

func clone(v any) any {
	b, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

func (m *Mock) id(prefix string) string {
	m.n++
	return fmt.Sprintf("%s-%04d", prefix, m.n)
}

func (m *Mock) url(env string, r Res) string {
	return m.URL + "/v1/environments/" + env + "/" + r.Kind + "/" + r.ID
}

func fail(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": msg})
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (m *Mock) serve(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if strings.HasSuffix(r.URL.Path, "/as/token") {
		reply(w, 200, map[string]any{"access_token": "mock-token", "expires_in": 3600})
		return
	}
	if r.Header.Get("Authorization") != "Bearer mock-token" {
		fail(w, 401, "ACCESS_FAILED", "no token")
		return
	}
	p := strings.Split(strings.Trim(r.URL.Path, "/"), "/") // v1 environments <env> ...
	if len(p) < 4 || p[0] != "v1" || p[1] != "environments" || m.Envs[p[2]] == nil {
		fail(w, 404, "NOT_FOUND", "no such environment or path")
		return
	}
	env, rest := p[2], p[3:]
	switch {
	case rest[0] == "snapshots" && len(rest) == 1 && r.Method == "POST":
		id := m.id("snap")
		m.snaps[id] = clone2(m.Envs[env])
		reply(w, 201, map[string]any{"snapshotId": id, "status": "IN_PROGRESS", "resource": map[string]string{"id": env}})
	case rest[0] == "snapshots" && len(rest) == 4 && rest[2] == "versions" && r.Method == "GET":
		if m.snaps[rest[3]] == nil {
			fail(w, 404, "NOT_FOUND", "no such snapshot")
			return
		}
		reply(w, 200, map[string]any{"snapshotId": rest[3], "status": "COMPLETE"})
	case rest[0] == "snapshots" && len(rest) == 5 && rest[4] == "embeddedSnapshots" && r.Method == "GET":
		m.listSnapshot(w, r, env, rest[3])
	case rest[0] == "promotions" && len(rest) == 1 && r.Method == "POST":
		m.create(w, r, env)
	case rest[0] == "promotions" && len(rest) == 2 && r.Method == "GET":
		if pr := m.promos[rest[1]]; pr != nil {
			reply(w, 200, m.view(pr))
			return
		}
		fail(w, 404, "NOT_FOUND", "no such promotion")
	case rest[0] == "promotions" && len(rest) == 3 && rest[2] == "promotionSteps" && r.Method == "GET":
		if pr := m.promos[rest[1]]; pr != nil {
			reply(w, 200, map[string]any{"_embedded": map[string]any{"promotionSteps": pr.Steps}})
			return
		}
		fail(w, 404, "NOT_FOUND", "no such promotion")
	case rest[0] == "promotions" && len(rest) == 2 && r.Method == "POST":
		pr := m.promos[rest[1]]
		if pr == nil {
			fail(w, 404, "NOT_FOUND", "no such promotion")
			return
		}
		switch r.Header.Get("Content-Type") {
		case "application/vnd.pingidentity.promotion.execute+json":
			m.execute(w, pr)
		case "application/vnd.pingidentity.promotion.rollback+json":
			m.rollback(w, pr)
		default:
			fail(w, 415, "INVALID_REQUEST", "unknown media type")
		}
	default:
		fail(w, 404, "NOT_FOUND", "no such path")
	}
}

func clone2(in map[string]Res) map[string]Res {
	out := map[string]Res{}
	for k, v := range in {
		v.Config = clone(v.Config).(map[string]any)
		out[k] = v
	}
	return out
}

func (m *Mock) listSnapshot(w http.ResponseWriter, r *http.Request, env, snap string) {
	res := m.snaps[snap]
	if res == nil {
		fail(w, 404, "NOT_FOUND", "no such snapshot")
		return
	}
	ids := make([]string, 0, len(res))
	for id := range res {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	// Two pages, so paging is exercised.
	half, page := (len(ids)+1)/2, ids
	links := map[string]any{}
	if r.URL.Query().Get("page") == "2" {
		page = ids[half:]
	} else {
		page = ids[:half]
		if half < len(ids) {
			links["next"] = map[string]string{"href": m.URL + r.URL.Path + "?page=2"}
		}
	}
	var items []map[string]any
	for _, id := range page {
		x := res[id]
		items = append(items, map[string]any{"resource": map[string]string{"id": x.ID, "name": x.Name}, "resourceUrl": m.url(env, x),
			"status": "COMPLETE", "configuration": x.Config, "referencedResources": x.Refs})
	}
	reply(w, 200, map[string]any{"_embedded": map[string]any{"referencedResources": items}, "_links": links})
}

func (m *Mock) view(pr *promotion) map[string]any {
	return map[string]any{"id": pr.ID, "status": pr.Status, "sourceEnvironment": map[string]string{"id": pr.Source},
		"targetEnvironment": map[string]string{"id": pr.Target}, "sourceSnapshot": map[string]string{"id": pr.SourceSnap},
		"targetSnapshot": map[string]string{"id": pr.TargetSnap}}
}

func match(target map[string]Res, s Res) (Res, bool) {
	for _, t := range target {
		if t.Kind == s.Kind && t.Name == s.Name {
			return t, true
		}
	}
	return Res{}, false
}

func (m *Mock) create(w http.ResponseWriter, r *http.Request, env string) {
	var body struct {
		SourceSnapshot    struct{ ID string } `json:"sourceSnapshot"`
		TargetEnvironment struct{ ID string } `json:"targetEnvironment"`
		Selected          []string            `json:"selectedResources"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || m.Envs[body.TargetEnvironment.ID] == nil || len(body.Selected) == 0 {
		fail(w, 400, "INVALID_DATA", "targetEnvironment and selectedResources are required")
		return
	}
	src := m.snaps[body.SourceSnapshot.ID]
	if src == nil {
		fail(w, 400, "INVALID_DATA", "unknown source snapshot")
		return
	}
	tsnap := m.id("snap")
	m.snaps[tsnap] = clone2(m.Envs[body.TargetEnvironment.ID])
	// The selection and everything it depends on.
	want, queue := map[string]bool{}, append([]string(nil), body.Selected...)
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		x, ok := src[id]
		if !ok {
			fail(w, 400, "INVALID_DATA", "unknown resource "+id)
			return
		}
		if !want[id] {
			want[id] = true
			queue = append(queue, x.Refs...)
		}
	}
	ids := make([]string, 0, len(want))
	for id := range want {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	pr := &promotion{ID: m.id("promo"), Status: "READY", Source: env, Target: body.TargetEnvironment.ID, SourceSnap: body.SourceSnapshot.ID, TargetSnap: tsnap}
	for _, id := range ids {
		s := src[id]
		t, found := match(m.snaps[tsnap], s)
		switch {
		case !found:
			pr.Steps = append(pr.Steps, map[string]any{"action": "CREATE", "resourceUrl": m.url(pr.Target, Res{Kind: s.Kind, ID: "new-" + s.ID}),
				"sourceResource": map[string]string{"id": s.ID, "name": s.Name}, "targetResource": map[string]string{}, "payload": s.Config})
		case !reflect.DeepEqual(clone(t.Config), clone(s.Config)):
			pr.Steps = append(pr.Steps, map[string]any{"action": "UPDATE", "resourceUrl": m.url(pr.Target, t),
				"sourceResource": map[string]string{"id": s.ID, "name": s.Name}, "targetResource": map[string]string{"id": t.ID, "name": t.Name}, "payload": s.Config})
		}
	}
	m.promos[pr.ID] = pr
	reply(w, 201, m.view(pr))
}

func (m *Mock) execute(w http.ResponseWriter, pr *promotion) {
	if pr.Status != "READY" {
		fail(w, 400, "INVALID_REQUEST", "the promotion is not ready to run")
		return
	}
	env := m.Envs[pr.Target]
	for _, s := range pr.Steps {
		tid := s["targetResource"].(map[string]string)["id"]
		src := s["sourceResource"].(map[string]string)
		kind := strings.Split(strings.TrimPrefix(s["resourceUrl"].(string), m.URL+"/v1/environments/"+pr.Target+"/"), "/")[0]
		switch s["action"] {
		case "CREATE":
			id := "new-" + src["id"]
			env[id] = Res{ID: id, Kind: kind, Name: src["name"], Config: clone(s["payload"]).(map[string]any)}
			pr.undo = append(pr.undo, map[string]any{"action": "DELETE", "resourceUrl": m.url(pr.Target, env[id]), "sourceResource": map[string]string{},
				"targetResource": map[string]string{"id": id, "name": src["name"]}, "payload": map[string]any{}})
		case "UPDATE":
			old := env[tid]
			pr.undo = append(pr.undo, map[string]any{"action": "UPDATE", "resourceUrl": m.url(pr.Target, old), "sourceResource": map[string]string{},
				"targetResource": map[string]string{"id": tid, "name": old.Name}, "payload": clone(old.Config)})
			old.Config = clone(s["payload"]).(map[string]any)
			env[tid] = old
		case "DELETE":
			delete(env, tid)
		}
	}
	m.Writes++
	pr.Status, pr.executed = "COMPLETED", true
	m.last[pr.Target] = pr.ID
	reply(w, 200, m.view(pr))
}

func (m *Mock) rollback(w http.ResponseWriter, pr *promotion) {
	if !pr.executed || pr.rolledBack || m.last[pr.Target] != pr.ID {
		fail(w, 400, "INVALID_REQUEST", "only the most recent promotion to a target can be rolled back, once")
		return
	}
	rb := &promotion{ID: m.id("promo"), Status: "READY", Source: pr.Source, Target: pr.Target, SourceSnap: pr.SourceSnap, TargetSnap: m.id("snap"), Steps: pr.undo}
	m.snaps[rb.TargetSnap] = clone2(m.Envs[pr.Target])
	m.promos[rb.ID] = rb
	pr.rolledBack, pr.Status = true, "ROLLED_BACK"
	reply(w, 201, m.view(rb))
}

// Edit changes one top-level field of a resource, as a person editing the
// environment by hand would.
func (m *Mock) Edit(env, id, field string, value any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.Envs[env][id]
	r.Config[field] = value
	m.Envs[env][id] = r
}
