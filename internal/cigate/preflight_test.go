package cigate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"pingone-promote/internal/checks"
)

type gitlabState struct {
	visibility   string
	protected    bool
	approvals    int
	ruleApproval int
	overrideRole any
	triggerer    any
	restrict     any
	failProject  bool
}

func readyState() gitlabState {
	return gitlabState{visibility: "private", protected: true, ruleApproval: 1, overrideRole: "maintainer", triggerer: false, restrict: true}
}

func preflightVerifier(t *testing.T, s gitlabState) *Verifier {
	t.Helper()
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		send := func(v any) { json.NewEncoder(w).Encode(v) }
		switch r.URL.Path {
		case "/api/v4/job":
			send(map[string]any{"id": 11, "status": "running", "pipeline": map[string]any{"id": 9, "project_id": 7}})
		case "/api/v4/projects/7":
			if s.failProject {
				w.WriteHeader(401)
				return
			}
			p := map[string]any{"id": 7, "visibility": s.visibility, "default_branch": "main", "archived": false}
			if s.overrideRole != nil {
				p["ci_pipeline_variables_minimum_override_role"] = s.overrideRole
			}
			if s.triggerer != nil {
				p["allow_pipeline_trigger_approve_deployment"] = s.triggerer
			}
			if s.restrict != nil {
				p["restrict_user_defined_variables"] = s.restrict
			}
			send(p)
		case "/api/v4/projects/7/repository/branches/main":
			send(map[string]any{"name": "main", "protected": s.protected})
		case "/api/v4/projects/7/protected_environments/production":
			rules := []any{}
			if s.ruleApproval > 0 {
				rules = append(rules, map[string]any{"required_approvals": s.ruleApproval})
			}
			send(map[string]any{"name": "production", "required_approval_count": s.approvals, "approval_rules": rules})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)
	cfg := Config{BaseURL: ts.URL, ExpectedURL: ts.URL, ProjectID: "7", ExpectedProjectID: "7", PipelineID: "9", JobID: "11", CommitSHA: strings.Repeat("a", 40), Branch: "main", DefaultBranch: "main", Environment: "production", TargetEnvironmentID: "prod", TargetRegion: "us", JobToken: "job-token", ReadToken: "read-token", ProtectedRef: true, PipelineSource: "web"}
	return &Verifier{Config: cfg, HTTP: ts.Client()}
}

func find(r checks.Report, name string, st checks.Status) bool {
	for _, c := range r.Checks {
		if c.Name == name && c.Status == st {
			return true
		}
	}
	return false
}

func TestPreflightReadyProject(t *testing.T) {
	r := preflightVerifier(t, readyState()).Preflight(context.Background())
	if r.Failed() {
		t.Fatalf("ready project failed:\n%s", r.Markdown())
	}
	if _, w, _ := r.Counts(); w != 0 {
		t.Fatalf("unexpected warnings:\n%s", r.Markdown())
	}
}

func TestPreflightFindsMisconfiguration(t *testing.T) {
	cases := map[string]struct {
		mutate func(*gitlabState)
		name   string
		status checks.Status
	}{
		"public project":       {func(s *gitlabState) { s.visibility = "internal" }, "project", checks.Fail},
		"unprotected branch":   {func(s *gitlabState) { s.protected = false }, "default branch", checks.Fail},
		"no approval":          {func(s *gitlabState) { s.ruleApproval = 0 }, "deployment approval", checks.Fail},
		"developer overrides":  {func(s *gitlabState) { s.overrideRole = "developer" }, "variable overrides", checks.Fail},
		"restriction disabled": {func(s *gitlabState) { s.restrict = false }, "variable overrides", checks.Fail},
		"no one may override":  {func(s *gitlabState) { s.overrideRole = "no_one_allowed" }, "variable overrides", checks.Warn},
		"override hidden":      {func(s *gitlabState) { s.overrideRole = nil }, "variable overrides", checks.Warn},
		"self approval":        {func(s *gitlabState) { s.triggerer = true }, "separation of duties", checks.Warn},
		"count-only approval":  {func(s *gitlabState) { s.ruleApproval = 0; s.approvals = 1 }, "deployment approval", checks.Warn},
		"unreadable project":   {func(s *gitlabState) { s.failProject = true }, "project", checks.Fail},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s := readyState()
			c.mutate(&s)
			r := preflightVerifier(t, s).Preflight(context.Background())
			if !find(r, c.name, c.status) {
				t.Fatalf("expected %s %s:\n%s", c.name, c.status, r.Markdown())
			}
		})
	}
}

func TestPreflightRejectsUntrustedJobContext(t *testing.T) {
	v := preflightVerifier(t, readyState())
	v.Config.ProtectedRef = false
	r := v.Preflight(context.Background())
	if !find(r, "job context", checks.Fail) || len(r.Checks) != 1 {
		t.Fatalf("unprotected ref not stopped early:\n%s", r.Markdown())
	}
}
