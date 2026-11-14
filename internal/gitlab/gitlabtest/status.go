package gitlabtest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Status adds the read-only endpoints of the hosted server's Requests page
// to a Fake: branch search, merge requests of every state, commit pipelines
// and pipeline jobs. It answers them in-process from a RoundTripper and
// passes every other request to the Fake, so fake.go is unchanged.
type Status struct {
	f         *Fake
	mu        sync.Mutex
	mergeSHA  map[string]string // source branch -> merge commit
	pipelines []*fakePipeline
}

type fakePipeline struct {
	ID   int64
	SHA  string
	Ref  string
	Jobs []fakeJob
}

type fakeJob struct {
	ID           int64
	Name, Status string
}

// NewStatus wraps f.
func NewStatus(f *Fake) *Status {
	return &Status{f: f, mergeSHA: map[string]string{}}
}

// Client is an HTTP client for the Fake that also serves the status
// endpoints.
func (s *Status) Client() *http.Client {
	return &http.Client{Transport: statusTransport{s: s, next: s.f.Client().Transport}}
}

// MergeMR merges branch into the default branch (fast-forward) and records
// the merge commit on its merge request. It returns that commit.
func (s *Status) MergeMR(branch string) string {
	s.f.Merge(branch)
	sha := s.f.Head(s.f.DefaultBranch)
	s.mu.Lock()
	s.mergeSHA[branch] = sha
	s.mu.Unlock()
	return sha
}

// SetPipeline records a pipeline of sha on ref with jobs (name -> GitLab
// status) and returns its ID.
func (s *Status) SetPipeline(sha, ref string, jobs map[string]string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := &fakePipeline{ID: int64(1000 + len(s.pipelines)), SHA: sha, Ref: ref}
	names := make([]string, 0, len(jobs))
	for n := range jobs {
		names = append(names, n)
	}
	sort.Strings(names)
	for i, n := range names {
		p.Jobs = append(p.Jobs, fakeJob{ID: p.ID*100 + int64(i), Name: n, Status: jobs[n]})
	}
	s.pipelines = append(s.pipelines, p)
	return p.ID
}

type statusTransport struct {
	s    *Status
	next http.RoundTripper
}

func (t statusTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == http.MethodGet && r.Header.Get("PRIVATE-TOKEN") == Token {
		rest, ok := strings.CutPrefix(r.URL.EscapedPath(), "/api/v4/projects/"+ProjectID)
		if ok {
			rec := httptest.NewRecorder()
			if t.s.serve(rec, r, rest) {
				t.s.f.mu.Lock()
				t.s.f.Requests = append(t.s.f.Requests, r.Method+" "+r.URL.RequestURI())
				t.s.f.mu.Unlock()
				res := rec.Result()
				res.Request = r
				return res, nil
			}
		}
	}
	return t.next.RoundTrip(r)
}

func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(v)
}

// serve answers a status endpoint and reports whether it did.
func (s *Status) serve(w http.ResponseWriter, r *http.Request, rest string) bool {
	q := r.URL.Query()
	host := "https://" + r.URL.Host + "/group/infra"
	switch {
	case rest == "/repository/branches":
		search := q.Get("search")
		prefix, anchored := strings.CutPrefix(search, "^")
		s.f.mu.Lock()
		var names []string
		for name := range s.f.branches {
			if anchored && strings.HasPrefix(name, prefix) || !anchored && strings.Contains(name, search) {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		var out []map[string]any
		for _, n := range names {
			msg := ""
			if head := s.f.Commits[s.f.heads[n]]; head != nil {
				msg = head.Message
			}
			out = append(out, map[string]any{"name": n, "protected": s.f.Protected[n], "commit": map[string]string{"id": s.f.heads[n], "message": msg}})
		}
		s.f.mu.Unlock()
		if q.Get("page") != "" && q.Get("page") != "1" {
			out = nil
		}
		reply(w, out)
		return true
	case rest == "/merge_requests" && q.Get("state") == "all":
		s.f.mu.Lock()
		mrs := append([]*MR(nil), s.f.MRs...)
		s.f.mu.Unlock()
		s.mu.Lock()
		defer s.mu.Unlock()
		var out []map[string]any
		for i := len(mrs) - 1; i >= 0; i-- {
			mr := mrs[i]
			if l := q.Get("labels"); l != "" && !strings.Contains(","+mr.Labels+",", ","+l+",") {
				continue
			}
			m := map[string]any{"iid": mr.IID, "title": mr.Title, "state": mr.State, "source_branch": mr.SourceBranch,
				"web_url": host + "/-/merge_requests/" + strconv.FormatInt(mr.IID, 10)}
			if mr.State == "merged" {
				m["merge_commit_sha"] = s.mergeSHA[mr.SourceBranch]
			}
			out = append(out, m)
		}
		reply(w, out)
		return true
	case rest == "/pipelines":
		s.mu.Lock()
		defer s.mu.Unlock()
		var out []map[string]any
		for i := len(s.pipelines) - 1; i >= 0; i-- {
			p := s.pipelines[i]
			if p.SHA == q.Get("sha") && (q.Get("ref") == "" || p.Ref == q.Get("ref")) {
				out = append(out, map[string]any{"id": p.ID, "sha": p.SHA, "ref": p.Ref, "status": "running",
					"web_url": host + "/-/pipelines/" + strconv.FormatInt(p.ID, 10)})
			}
		}
		reply(w, out)
		return true
	case strings.HasPrefix(rest, "/pipelines/") && strings.HasSuffix(rest, "/jobs"):
		id, _ := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(rest, "/pipelines/"), "/jobs"), 10, 64)
		s.mu.Lock()
		defer s.mu.Unlock()
		out := []map[string]any{}
		for _, p := range s.pipelines {
			if p.ID != id {
				continue
			}
			for _, j := range p.Jobs {
				out = append(out, map[string]any{"id": j.ID, "name": j.Name, "status": j.Status,
					"web_url": host + "/-/jobs/" + strconv.FormatInt(j.ID, 10)})
			}
		}
		if q.Get("page") != "" && q.Get("page") != "1" {
			out = []map[string]any{}
		}
		reply(w, out)
		return true
	}
	return false
}
