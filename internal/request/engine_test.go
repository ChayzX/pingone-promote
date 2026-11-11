package request

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"pingone-promote/internal/exporter"
	"pingone-promote/internal/exporter/exportertest"
	"pingone-promote/internal/gitlab"
	"pingone-promote/internal/gitlab/gitlabtest"
)

const (
	devEnv = "11111111-1111-4111-8111-111111111111"
	reqID  = "20261103-120000-abc123"
)

func terraformBin(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("PINGONE_PROMOTE_TERRAFORM"); p != "" {
		return p
	}
	p, err := exec.LookPath("terraform")
	if err != nil {
		t.Skip("terraform not found; the fmt gate needs it")
	}
	return p
}

func repoFiles(t *testing.T) map[string]string {
	t.Helper()
	root := filepath.Join("..", "promote", "testdata", "repo")
	out := map[string]string{}
	filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		b, _ := os.ReadFile(p)
		out[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	return out
}

func requestJSON(mut func(map[string]any)) []byte {
	m := map[string]any{
		"requestVersion": 1, "requestId": reqID, "requestedBy": "jdoe", "source": "dev",
		"flows":             []map[string]string{{"id": "f0000000000000000000000000000002", "name": "Flow B"}},
		"dependencyChoices": map[string]string{}, "envValues": map[string]any{}, "secretMappings": map[string]string{"davinci_connection_PingOne_clientSecret": "PingOne_DaVinci_connection_PingOne_clientSecret"},
		"mode": "mr", "branchName": "pingone-promote/" + reqID, "title": "PingOne: Flow B from dev", "description": "Adds the subflow.", "dryRun": false,
	}
	if mut != nil {
		mut(m)
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	return b
}

type harness struct {
	fake   exportertest.Fake
	t      *testing.T
	gl     *gitlabtest.Fake
	env    Env
	branch string
}

func newHarness(t *testing.T, req []byte) *harness {
	t.Helper()
	return newHarnessOn(t, req, "pingone-promote/"+reqID)
}

func newHarnessOn(t *testing.T, req []byte, branch string) *harness {
	t.Helper()
	tf := terraformBin(t)
	files := repoFiles(t)
	gl := gitlabtest.New(t, files)
	head := gl.CreateBranch(branch, "main", map[string]string{gitlab.RequestFile: string(req)})
	ws := t.TempDir()
	for p, c := range gl.Files(branch) {
		full := filepath.Join(ws, filepath.FromSlash(p))
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte(c), 0o644)
	}
	fake := exportertest.Build(t, t.TempDir(), exportertest.FixtureDir("dev"))
	client, err := gitlab.NewClient(gitlab.Config{BaseURL: gl.BaseURL(), ProjectID: gitlabtest.ProjectID, TokenEnv: "T"}, gitlabtest.Token)
	if err != nil {
		t.Fatal(err)
	}
	client.HTTP = gl.Client()
	env := Env{Workspace: ws, Rollout: []string{"qa", "stage", "prod"},
		Exporter:     &exporter.Runner{Config: exporter.Config{Version: fake.Version, SHA256: fake.SHA256, Path: fake.Path}},
		Credentials:  exporter.Credentials{ClientID: "c", ClientSecret: "s", WorkerEnvironmentID: devEnv, RegionCode: "NA"},
		TerraformBin: tf, Committer: GitLabCommitter{S: &gitlab.Submitter{Client: client, Formatter: tf}},
		Branch: branch, Head: head, DefaultBranch: "main", ProjectURL: "https://gitlab.example.invalid/group/config"}
	return &harness{t: t, gl: gl, env: env, branch: branch, fake: fake}
}

func (h *harness) run(req []byte) *Result {
	h.t.Helper()
	r, err := Parse(req)
	if err != nil {
		h.t.Fatal(err)
	}
	return Run(context.Background(), h.env, r)
}

func TestDryRunCommitsNothing(t *testing.T) {
	req := requestJSON(func(m map[string]any) { m["dryRun"] = true })
	h := newHarness(t, req)
	before := len(h.gl.CommitOrder)
	res := h.run(req)
	if res.Status != StatusPreviewed || res.Error != nil {
		t.Fatalf("%+v", res.Error)
	}
	if len(h.gl.CommitOrder) != before || len(h.gl.MRs) != 0 {
		t.Fatal("a dry run must not commit or open an MR")
	}
	if !strings.Contains(res.Diff, "+resource \"pingone_davinci_flow\" \"pingcli__Flow-0020-B-0020-Subflow\"") {
		t.Fatalf("diff:\n%s", res.Diff)
	}
	roles := map[string]string{}
	for _, c := range res.Changes {
		roles[c.Address] = c.Role + "/" + c.Action
	}
	if roles["pingone_davinci_flow.pingcli__Flow-0020-B"] != "selected/updated" ||
		roles["pingone_davinci_connector_instance.pingcli__PingOne"] != "dependency/kept_differs" {
		t.Fatalf("roles: %v", roles)
	}
	if res.Summary.Added != 3 || res.Summary.KeptDiffers != 1 {
		t.Fatalf("summary %+v", res.Summary)
	}
	if len(res.RequiredSecrets) != 0 {
		// No new block needs a secret: the kept connection is not written.
		t.Fatalf("secrets: %+v", res.RequiredSecrets)
	}
	if _, ok := h.gl.Files(h.branch)[gitlab.RequestFile]; !ok {
		t.Fatal("request file must stay after a preview")
	}
	if res.DiffURL == "" || res.BranchURL == "" {
		t.Fatalf("urls: %q %q", res.DiffURL, res.BranchURL)
	}
}

func TestSubmitMakesOneCommitAndRerunIsNoop(t *testing.T) {
	req := requestJSON(nil)
	h := newHarness(t, req)
	before := len(h.gl.CommitOrder)
	res := h.run(req)
	if res.Status != StatusSubmitted || res.Error != nil {
		t.Fatalf("%s %+v", res.Status, res.Error)
	}
	if len(h.gl.CommitOrder) != before+1 || len(h.gl.MRs) != 1 {
		t.Fatalf("want one commit and one MR, got %d commits %d MRs", len(h.gl.CommitOrder)-before, len(h.gl.MRs))
	}
	files := h.gl.Files(h.branch)
	if _, ok := files[gitlab.RequestFile]; ok {
		t.Fatal("the commit must delete the request file")
	}
	c := h.gl.Commits[res.CommitID]
	for _, want := range []string{"Requested-by: jdoe", "Request-id: " + reqID, gitlab.RequestTrailer + reqID} {
		if !strings.Contains(c.Message, want) {
			t.Fatalf("commit message lacks %q:\n%s", want, c.Message)
		}
	}
	mr := h.gl.MRs[0]
	if !strings.Contains(mr.Description, "Requested-by: jdoe\nRequest-id: "+reqID) || mr.Labels != "pingone-promote" || mr.Title != "PingOne: Flow B from dev" {
		t.Fatalf("MR: %+v", mr)
	}
	if res.MRUrl == "" || res.MRIid != 1 || !strings.HasSuffix(res.DiffURL, res.CommitID) {
		t.Fatalf("result urls: %+v", res)
	}
	if !strings.Contains(files["definitions/pingone_davinci_flow.tf"], "pingcli__Flow-0020-B-0020-Subflow") {
		t.Fatal("subflow not committed")
	}
	// Re-running the same job (same checked-out commit) is a no-op.
	again := h.run(req)
	if again.Status != StatusAlreadySubmitted || again.CommitID != res.CommitID || again.MRUrl != res.MRUrl {
		t.Fatalf("re-run: %s %+v", again.Status, again.Error)
	}
	if len(h.gl.CommitOrder) != before+1 || len(h.gl.MRs) != 1 {
		t.Fatal("a re-run must not commit or open a second MR")
	}
	// A repeated submit after DaVinci re-pushed the request file: the prior
	// commit is found by its trailer and reported.
	h.env.Head = h.gl.Edit(h.branch, gitlab.RequestFile, string(req))
	h.env.PriorCommit = func(id string) (string, bool) { return res.CommitID, id == reqID }
	again = h.run(req)
	if again.Status != StatusAlreadySubmitted || again.MRUrl != res.MRUrl || len(h.gl.MRs) != 1 {
		t.Fatalf("re-submit: %+v", again)
	}
}

func TestBranchModeDeletesRequestFileWithoutMR(t *testing.T) {
	req := requestJSON(func(m map[string]any) { m["mode"] = "branch" })
	h := newHarness(t, req)
	res := h.run(req)
	if res.Status != StatusSubmitted || len(h.gl.MRs) != 0 || res.MRUrl != "" {
		t.Fatalf("%+v", res)
	}
	if _, ok := h.gl.Files(h.branch)[gitlab.RequestFile]; ok {
		t.Fatal("branch mode must delete the request file too")
	}
}

// A project whose push rules require another branch namespace (CIAM-) sets
// the prefix; the job then runs only on CIAM-<requestId>.
func TestCustomBranchPrefix(t *testing.T) {
	req := requestJSON(func(m map[string]any) { m["branchName"] = "CIAM-" + reqID; m["mode"] = "branch" })
	h := newHarnessOn(t, req, "CIAM-"+reqID)
	h.env.BranchPrefix = "CIAM-"
	res := h.run(req)
	if res.Status != StatusSubmitted || res.Branch != "CIAM-"+reqID {
		t.Fatalf("%+v %+v", res, res.Error)
	}
	if _, ok := h.gl.Files(h.branch)[gitlab.RequestFile]; ok {
		t.Fatal("the request file must be deleted")
	}
	// The same branch with the default prefix configured is refused, and so is
	// a request that names the default-prefix branch while the job uses CIAM-.
	h2 := newHarnessOn(t, req, "CIAM-"+reqID)
	if res := h2.run(req); res.Error == nil || res.Error.Code != ErrPreflight {
		t.Fatalf("default prefix must refuse a CIAM- branch: %+v", res.Error)
	}
	other := requestJSON(nil)
	h3 := newHarnessOn(t, other, "CIAM-"+reqID)
	h3.env.BranchPrefix = "CIAM-"
	if res := h3.run(other); res.Error == nil || !strings.Contains(res.Error.Message, "branchName: must be CIAM-"+reqID) {
		t.Fatalf("a request naming pingone-promote/<id> must be refused under CIAM-: %+v", res.Error)
	}
	if len(h3.gl.MRs) != 0 {
		t.Fatal("nothing may be opened on a refusal")
	}
}

func TestRefusalsCommitNothing(t *testing.T) {
	req := requestJSON(nil)
	cases := map[string]struct {
		mut  func(*Env)
		code string
	}{
		"default branch":  {func(e *Env) { e.Branch = "main" }, ErrPreflight},
		"foreign branch":  {func(e *Env) { e.Branch = "pingone-promote/20261103-120000-zzzzzz" }, ErrPreflight},
		"source in chain": {func(e *Env) { e.Rollout = []string{"dev", "qa"} }, ErrPreflight},
		"not allowed":     {func(e *Env) { e.AllowedSources = []string{"sandbox"} }, ErrPreflight},
		"missing env":     {func(e *Env) { e.Rollout = []string{"qa", "stage", "prod", "perf"} }, ErrBlocked},
		"moved head":      {func(e *Env) { e.Head = strings.Repeat("a", 40) }, ErrConflict},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, req)
			tc.mut(&h.env)
			before := len(h.gl.CommitOrder)
			res := h.run(req)
			if res.Error == nil || res.Error.Code != tc.code {
				t.Fatalf("got %s %+v", res.Status, res.Error)
			}
			if len(h.gl.CommitOrder) != before || len(h.gl.MRs) != 0 {
				t.Fatal("nothing may be committed")
			}
			if _, ok := h.gl.Files(h.branch)[gitlab.RequestFile]; !ok {
				t.Fatal("the request file must stay")
			}
		})
	}
}

func TestAmbiguousFlowNameListsCandidates(t *testing.T) {
	req := requestJSON(func(m map[string]any) { m["flows"] = []map[string]string{{"name": "Flow"}}; m["dryRun"] = true })
	h := newHarness(t, req)
	res := h.run(req)
	if res.Error == nil || res.Error.Type != "validation" || len(res.Error.Candidates) != 4 {
		t.Fatalf("%+v", res.Error)
	}
}

func TestParseIsStrict(t *testing.T) {
	cases := map[string][]byte{
		"unknown field":  requestJSON(func(m map[string]any) { m["target"] = "prod" }),
		"remove is gone": requestJSON(func(m map[string]any) { m["remove"] = []string{"pingone_davinci_flow.pingcli__Old"} }),
		"old version":    requestJSON(func(m map[string]any) { m["requestVersion"] = 0 }),
		"bad id":         requestJSON(func(m map[string]any) { m["requestId"] = "../x" }),
		"bad branch":     requestJSON(func(m map[string]any) { m["branchName"] = "main" }),
		"bad mode":       requestJSON(func(m map[string]any) { m["mode"] = "push" }),
		"bad choice":     requestJSON(func(m map[string]any) { m["dependencyChoices"] = map[string]string{"pingone_davinci_flow.x": "maybe"} }),
		"secret value": requestJSON(func(m map[string]any) {
			m["envValues"] = map[string]any{"qa": map[string]any{"davinci_variable_x_value": "-----BEGIN RSA PRIVATE KEY-----"}}
		}),
		"named secret": requestJSON(func(m map[string]any) {
			m["envValues"] = map[string]any{"qa": map[string]any{"api_token": "abc"}}
		}),
		"no selection": requestJSON(func(m map[string]any) { m["flows"] = []any{} }),
		"trailing":     append(requestJSON(nil), []byte(" {}")...),
		"too big":      append(requestJSON(nil), make([]byte, MaxSize)...),
	}
	for name, b := range cases {
		_, err := Parse(b)
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Fatalf("%s: accepted (%v)", name, err)
		}
		if name == "old version" && ve.Code != CodeVersion {
			t.Fatalf("old version code %s", ve.Code)
		}
	}
	if _, err := Parse(requestJSON(nil)); err != nil {
		t.Fatal(err)
	}
}

func TestLogLineHasNoDiff(t *testing.T) {
	r := newResult()
	r.RequestID, r.Status, r.Diff = reqID, StatusPreviewed, "+secret-free diff"
	if l := r.LogLine(); !strings.HasPrefix(l, LogPrefix) || strings.Contains(l, "secret-free diff") {
		t.Fatal(l)
	}
}
