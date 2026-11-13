package server

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"pingone-promote/internal/gitlab"
)

// Rollout states shown on the Requests page.
const (
	RolloutNotStarted = "not started"
	RolloutRunning    = "running"
	RolloutPassed     = "passed"
	RolloutFailed     = "failed"
	RolloutManual     = "manual"
)

// rolloutState maps a GitLab job status to a rollout state.
func rolloutState(status string) string {
	switch status {
	case "success":
		return RolloutPassed
	case "failed", "canceled":
		return RolloutFailed
	case "manual":
		return RolloutManual
	case "running", "pending", "preparing", "waiting_for_resource":
		return RolloutRunning
	}
	return RolloutNotStarted // created, scheduled, skipped, or no job
}

type rolloutCell struct {
	Env    string
	State  string
	Detail string
	URL    string
}

type requestRow struct {
	Branch    string
	BranchURL string
	MR        *gitlab.RequestMR
	MRState   string
	Rollout   []rolloutCell
	Note      string
}

type requestsView struct {
	Rows    []requestRow
	Rollout []string
	Problem string
	Label   string
	Prefix  string
}

// maxRolloutLookups bounds the pipeline reads of one page view.
const maxRolloutLookups = 20

func (s *Server) requestsPage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	v := requestsView{Rollout: s.cfg.Rollout, Prefix: s.cfg.BranchPrefix, Label: "pingone-promote"}
	if len(s.cfg.GitLab.MRLabels) > 0 {
		v.Label = s.cfg.GitLab.MRLabels[0]
	}
	rows, err := s.cachedRows(ctx, v.Label)
	if err != nil {
		v.Problem = "GitLab could not be read: " + s.redact(err.Error())
		s.log.Warn("requests page", "error", err.Error())
	}
	v.Rows = rows
	s.render(w, r, http.StatusOK, "requests", "Requests", v, false)
}

// RequestsCacheFor is how long the Requests page reuses its GitLab reads,
// so reloading it (or many people opening it) cannot flood GitLab.
const RequestsCacheFor = 30 * time.Second

type requestsCache struct {
	mu   sync.Mutex
	at   time.Time
	rows []requestRow
	err  error
}

func (s *Server) cachedRows(ctx context.Context, label string) ([]requestRow, error) {
	c := &s.reqCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.at.IsZero() && s.now().Sub(c.at) < RequestsCacheFor {
		return c.rows, c.err
	}
	c.rows, c.err = s.requestRows(ctx, label)
	c.at = s.now()
	if c.err != nil && ctx.Err() != nil {
		c.at = time.Time{} // a cancelled read is not cached
	}
	return c.rows, c.err
}

func (s *Server) requestRows(ctx context.Context, label string) ([]requestRow, error) {
	project, err := s.gl.VerifyProject(ctx)
	if err != nil {
		return nil, err
	}
	base := strings.TrimRight(project.WebURL, "/")
	branchURL := func(b string) string { return base + "/-/tree/" + url.PathEscape(b) }
	mrs, err := s.gl.ListRequestMRs(ctx, label, s.cfg.BranchPrefix, 50)
	if err != nil {
		return nil, err
	}
	branches, err := s.gl.ListRequestBranches(ctx, s.cfg.BranchPrefix)
	if err != nil {
		return nil, err
	}
	live := map[string]bool{}
	for _, b := range branches {
		live[b.Name] = true
	}
	var rows []requestRow
	seen := map[string]bool{}
	lookups := 0
	for i := range mrs {
		m := mrs[i]
		if seen[m.SourceBranch] {
			continue
		}
		seen[m.SourceBranch] = true
		row := requestRow{Branch: m.SourceBranch, MR: &m, MRState: m.State}
		if live[m.SourceBranch] {
			row.BranchURL = branchURL(m.SourceBranch)
		}
		row.Rollout = s.notStarted()
		if m.State == "merged" {
			switch sha := m.MergedCommit(); {
			case sha == "":
				row.Note = "GitLab did not report the merge commit."
			case lookups >= maxRolloutLookups:
				row.Note = "Rollout not read (only the latest merged requests are checked)."
			default:
				lookups++
				cells, note := s.rollout(ctx, sha, project.DefaultBranch)
				if cells != nil {
					row.Rollout = cells
				}
				row.Note = note
			}
		}
		rows = append(rows, row)
	}
	var rest []string
	for _, b := range branches {
		if !seen[b.Name] {
			rest = append(rest, b.Name)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(rest)))
	for _, b := range rest {
		rows = append(rows, requestRow{Branch: b, BranchURL: branchURL(b), Rollout: s.notStarted()})
	}
	// Newest first: request branches are named after their request ID,
	// which starts with the time it was made.
	sort.SliceStable(rows, func(i, j int) bool { return requestKey(rows[i].Branch) > requestKey(rows[j].Branch) })
	return rows, nil
}

func (s *Server) notStarted() []rolloutCell {
	out := make([]rolloutCell, 0, len(s.cfg.Rollout))
	for _, env := range s.cfg.Rollout {
		out = append(out, rolloutCell{Env: env, State: RolloutNotStarted})
	}
	return out
}

// rollout reads the default-branch pipeline of a merge commit and the
// apply job of every rollout environment, in rollout order.
func (s *Server) rollout(ctx context.Context, sha, branch string) ([]rolloutCell, string) {
	p, err := s.gl.CommitPipeline(ctx, sha, branch)
	if err != nil {
		return nil, "The pipeline could not be read."
	}
	if p == nil {
		return nil, "No pipeline has run for the merge commit yet."
	}
	jobs, err := s.gl.PipelineJobs(ctx, p.ID)
	if err != nil {
		return nil, "The pipeline jobs could not be read."
	}
	byName := map[string]gitlab.Job{}
	for _, j := range jobs {
		if old, ok := byName[j.Name]; !ok || j.ID > old.ID {
			byName[j.Name] = j
		}
	}
	cells := make([]rolloutCell, 0, len(s.cfg.Rollout))
	for _, env := range s.cfg.Rollout {
		name := strings.ReplaceAll(s.cfg.ApplyJobPattern, "{env}", env)
		c := rolloutCell{Env: env, State: RolloutNotStarted, Detail: "no " + name + " job"}
		if j, ok := byName[name]; ok {
			c.State, c.Detail, c.URL = rolloutState(j.Status), name+": "+j.Status, j.WebURL
		}
		cells = append(cells, c)
	}
	return cells, ""
}

var requestIDRE = regexp.MustCompile(`\d{8}-\d{6}-[a-z0-9]{6}`)

// requestKey orders rows by their request ID (its time), when the branch
// name has one.
func requestKey(branch string) string { return requestIDRE.FindString(branch) }
