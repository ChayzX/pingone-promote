package app

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"pingone-promote/internal/davinci/davincitest"
	"pingone-promote/internal/gitlab"
	"pingone-promote/internal/gitlab/gitlabtest"
	"pingone-promote/internal/pingone"
	"pingone-promote/internal/store"
	"pingone-promote/internal/tfvars"
)

const (
	draftSourceEnv = "00000000-0000-4000-8000-0000000000a1"
	draftTargetEnv = "00000000-0000-4000-8000-0000000000b2"
	workerSecret   = "worker-secret-CANARY-7731"
)

var leakRE = regexp.MustCompile(`LEAKCANARY-[a-z-]+-[0-9]{4}|\*\*\*\*\*\*|` + workerSecret + `|` + gitlabtest.Token)

type draftHarness struct {
	t        *testing.T
	app      *Server
	server   *httptest.Server
	http     *http.Client
	csrf     string
	gl       *gitlabtest.Fake
	src, dst pingone.Profile
	st       *store.Store
	bodies   []string
}

func repoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func newDraftHarness(t *testing.T) *draftHarness {
	t.Helper()
	if _, err := tfvars.ResolveFormatter(""); err != nil {
		t.Skip("terraform or tofu not on PATH; submissions require the fmt gate")
	}
	t.Setenv("TEST_DV_SECRET", workerSecret)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	src, err := st.SaveProfile(pingone.Profile{Name: "Development", Region: "us", EnvironmentID: draftSourceEnv, ClientID: "worker", SecretMode: "env", SecretEnv: "TEST_DV_SECRET"}, "")
	if err != nil {
		t.Fatal(err)
	}
	dst, err := st.SaveProfile(pingone.Profile{Name: "Prod", Region: "us", EnvironmentID: draftTargetEnv, ClientID: "worker", SecretMode: "env", SecretEnv: "TEST_DV_SECRET"}, "")
	if err != nil {
		t.Fatal(err)
	}
	dv, _ := davincitest.New(t, filepath.Join("..", "davinci", "testdata", "source"), draftSourceEnv)
	gl := gitlabtest.New(t, map[string]string{
		"environments/prod.tfvars": repoFile(t, "internal/promotelegacy/testdata/e2e_target.tfvars"),
		"environments/dev.tfvars":  repoFile(t, "internal/tfvars/testdata/full.tfvars"),
		"customer_secrets.yml":     repoFile(t, "internal/pipelinesecrets/testdata/customer_secrets.yml"),
		".gitlab-ci.yml":           "include: customer_secrets.yml\n",
	})
	h := &draftHarness{t: t, gl: gl, src: src, dst: dst, st: st}
	h.start(dv)
	h.call("POST", "/api/gitlab", map[string]string{"baseUrl": gl.BaseURL(), "projectId": gitlabtest.ProjectID, "token": gitlabtest.Token}, nil, 200)
	return h
}

func (h *draftHarness) start(dv *davincitest.Server) {
	app := New(h.st, "bootstrap-token")
	app.clientFactory = func(p pingone.Profile, secret string) (*pingone.Client, error) {
		if secret != workerSecret {
			h.t.Fatalf("unexpected worker secret")
		}
		return &pingone.Client{BaseURL: dv.URL, HTTP: dv.Client(), AccessToken: davincitest.Token, Profile: p}, nil
	}
	app.gitlabFactory = func(c gitlab.Config, token string) (*gitlab.Client, error) {
		client, err := gitlab.NewClient(c, token)
		if client != nil {
			client.HTTP = h.gl.Client()
		}
		return client, err
	}
	if h.server != nil {
		h.server.Close()
		// The session token for GitLab lives in memory; carry it over as a
		// restarted app would get it from the environment.
		h.t.Setenv("GITLAB_TOKEN", gitlabtest.Token)
	}
	h.app = app
	h.server = httptest.NewServer(app)
	h.t.Cleanup(h.server.Close)
	jar, _ := cookiejar.New(nil)
	h.http = &http.Client{Jar: jar}
	res, err := h.http.Get(h.server.URL + "/?token=bootstrap-token")
	if err != nil {
		h.t.Fatal(err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	var session struct {
		CSRF string `json:"csrf"`
	}
	h.call("GET", "/api/session", nil, &session, 200)
	h.csrf = session.CSRF
}

func (h *draftHarness) call(method, path string, body, out any, status int) map[string]any {
	h.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, h.server.URL+path, reader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", h.csrf)
	req.Header.Set("Origin", h.server.URL)
	res, err := h.http.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	h.bodies = append(h.bodies, method+" "+path+"\n"+string(raw))
	if m := leakRE.Find(raw); m != nil {
		h.t.Fatalf("%s %s leaked a secret (%q)", method, path, m)
	}
	if res.StatusCode != status {
		h.t.Fatalf("%s %s: status %d, want %d: %s", method, path, res.StatusCode, status, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			h.t.Fatalf("decode %s: %v", path, err)
		}
	}
	var generic map[string]any
	json.Unmarshal(raw, &generic)
	return generic
}

type viewT struct {
	ID              string           `json:"id"`
	Revision        int              `json:"revision"`
	NeedsRebase     bool             `json:"needsRebase"`
	Blockers        []string         `json:"blockers"`
	ReadyToSubmit   bool             `json:"readyToSubmit"`
	RequiredSecrets []requiredSecret `json:"requiredSecrets"`
	NewVaultEntries []vaultEntry     `json:"newVaultEntries"`
	Candidates      []string         `json:"secretCandidates"`
	Changes         []tfvars.Change  `json:"changes"`
}

func (h *draftHarness) draft() viewT {
	var inv struct {
		ID    string    `json:"id"`
		Flows []keyName `json:"flows"`
	}
	h.call("POST", "/api/inventories", map[string]string{"profileId": h.src.ID}, &inv, 200)
	if len(inv.Flows) != 3 {
		h.t.Fatalf("inventory flows %+v", inv.Flows)
	}
	var v viewT
	h.call("POST", "/api/drafts", map[string]string{"inventoryId": inv.ID, "targetProfileId": h.dst.ID}, &v, 201)
	return v
}

func (h *draftHarness) patch(v viewT, ops ...map[string]any) viewT {
	h.t.Helper()
	var out viewT
	h.call("PATCH", "/api/drafts/"+v.ID, map[string]any{"revision": v.Revision, "ops": ops}, &out, 200)
	if out.Revision != v.Revision+1 {
		h.t.Fatalf("revision %d -> %d", v.Revision, out.Revision)
	}
	return out
}

func readyDraft(h *draftHarness) viewT {
	v := h.draft()
	if v.Revision != 1 || len(v.Blockers) == 0 {
		h.t.Fatalf("new draft %+v", v)
	}
	v = h.patch(v,
		map[string]any{"op": "select", "kind": "flow", "key": "Login"},
		map[string]any{"op": "select", "kind": "application", "key": "Web-20Login"},
		map[string]any{"op": "acknowledgeSecret", "site": "node:Login/n3sso/hmacKey"},
		map[string]any{"op": "mapSecret", "site": "node:Login/n3sso/hmacKey", "name": "PingOne_DaVinci_flow_Login_hmac", "field": "login_hmac"},
	)
	if len(v.Blockers) != 0 || !v.ReadyToSubmit {
		h.t.Fatalf("blockers %v ready %v", v.Blockers, v.ReadyToSubmit)
	}
	var invs []inventorySummary
	h.call("GET", "/api/inventories", nil, &invs, 200)
	if len(invs) == 0 {
		h.t.Fatal("inventory list empty")
	}
	h.call("GET", "/api/drafts/"+v.ID, nil, nil, 200)
	return v
}

func TestDraftAPIEndToEnd(t *testing.T) {
	h := newDraftHarness(t)

	var envs []map[string]any
	h.call("GET", "/api/environments", nil, &envs, 200)
	if len(envs) != 2 || envs[1]["tfvarsPath"] != "environments/prod.tfvars" {
		t.Fatalf("environments %+v", envs)
	}
	v := readyDraft(h)
	// The secret mapped to a new name is a new Vault entry; the target's
	// existing pipeline secret is reused.
	if len(v.NewVaultEntries) != 1 || v.NewVaultEntries[0].EnvVar != "TF_VAR_PingOne_DaVinci_flow_Login_hmac" ||
		v.NewVaultEntries[0].Vault != "apps/pingone-davinci/${VAULT_PATH_ENV}/pingone/login_hmac@secret" || len(v.Candidates) != 3 {
		t.Fatalf("vault entries %+v candidates %v", v.NewVaultEntries, v.Candidates)
	}

	// Stale revision.
	stale := h.call("PATCH", "/api/drafts/"+v.ID, map[string]any{"revision": v.Revision - 1, "ops": []map[string]any{{"op": "select", "kind": "flow", "key": "Login"}}}, nil, 409)
	if stale["code"] != "stale_revision" {
		t.Fatalf("stale: %v", stale)
	}
	// Invalid op.
	h.call("PATCH", "/api/drafts/"+v.ID, map[string]any{"revision": v.Revision, "ops": []map[string]any{{"op": "setParameter", "key": "Bad Key", "value": "x"}}}, nil, 400)

	var diff struct {
		Changes []tfvars.Change `json:"changes"`
		Summary string          `json:"summary"`
	}
	h.call("GET", "/api/drafts/"+v.ID+"/diff", nil, &diff, 200)
	if len(diff.Changes) == 0 || !strings.Contains(diff.Summary, "New Vault entries to create") {
		t.Fatalf("diff %+v", diff)
	}
	var files struct {
		Tfvars, SecretsTf, CustomerSecrets struct{ Path, Content string }
	}
	h.call("GET", "/api/drafts/"+v.ID+"/file", nil, &files, 200)
	if !strings.Contains(files.Tfvars.Content, "@@secret:PingOne_DaVinci_flow_Login_hmac@@") ||
		!strings.Contains(files.SecretsTf.Content, `variable "PingOne_DaVinci_flow_Login_hmac"`) ||
		// secrets.tf also declares the other environment's secrets.
		!strings.Contains(files.SecretsTf.Content, `variable "davinci_variable_apiToken_value"`) ||
		!strings.Contains(files.CustomerSecrets.Content, "TF_VAR_PingOne_DaVinci_flow_Login_hmac:") {
		t.Fatalf("files:\n%s\n%s\n%s", files.Tfvars.Content, files.SecretsTf.Content, files.CustomerSecrets.Content)
	}

	// Submit in mr mode: one commit with three files and a described MR.
	var sub struct {
		Submission submissionRecord `json:"submission"`
	}
	submit := map[string]any{"revision": v.Revision, "mode": "mr", "branchName": "promote/prod-login", "commitMessage": "Promote Login to prod"}
	h.call("POST", "/api/drafts/"+v.ID+"/submit", submit, &sub, 200)
	commit := h.gl.Commits[sub.Submission.CommitID]
	if commit == nil || len(commit.Actions) != 3 || sub.Submission.MergeRequestIID != 1 {
		t.Fatalf("submission %+v commit %+v", sub, commit)
	}
	mr := h.gl.MRs[0]
	if !strings.Contains(mr.Description, "TF_VAR_PingOne_DaVinci_flow_Login_hmac") || !strings.Contains(mr.Description, "| davinci_flows | `Login` | added |") {
		t.Fatalf("MR description:\n%s", mr.Description)
	}
	if strings.Contains(commit.Message, "LEAKCANARY") || strings.Contains(mr.Description, "LEAKCANARY") {
		t.Fatal("secret in commit or MR text")
	}
	for _, a := range commit.Actions {
		if a["file_path"] == ".gitlab-ci.yml" || leakRE.MatchString(a["content"]) {
			t.Fatalf("bad action %s", a["file_path"])
		}
	}

	// Submitting again reports the existing branch as resumable; resume is idempotent.
	partial := h.call("POST", "/api/drafts/"+v.ID+"/submit", submit, nil, 409)
	details, _ := partial["details"].(map[string]any)
	if partial["code"] != "partial" || details["resumable"] != true {
		t.Fatalf("partial: %v", partial)
	}
	resume := map[string]any{"revision": v.Revision, "mode": "mr", "branchName": "promote/prod-login", "commitMessage": "Promote Login to prod", "resume": true}
	var resumed struct {
		Resumed bool `json:"resumed"`
	}
	h.call("POST", "/api/drafts/"+v.ID+"/submit", resume, &resumed, 200)
	if !resumed.Resumed || len(h.gl.MRs) != 1 {
		t.Fatal("resume did not reuse the existing branch and MR")
	}

	// Revert after merge, in mr mode.
	h.gl.Merge("promote/prod-login")
	var rev struct {
		Submission submissionRecord `json:"submission"`
	}
	h.call("POST", "/api/submissions/"+sub.Submission.ID+"/revert", map[string]any{"mode": "mr"}, &rev, 200)
	if rev.Submission.Kind != "revert" || rev.Submission.MergeRequestIID == 0 {
		t.Fatalf("revert %+v", rev)
	}
	if got, _, _ := h.gl.File(rev.Submission.Branch, "environments/prod.tfvars"); got != repoFile(t, "internal/promotelegacy/testdata/e2e_target.tfvars") {
		t.Fatal("revert did not restore the target tfvars")
	}
	var subs []submissionRecord
	h.call("GET", "/api/submissions", nil, &subs, 200)
	if len(subs) != 2 {
		t.Fatalf("submissions %+v", subs)
	}

	// The draft is now based on stale files: submitting conflicts.
	v = h.patch(v, map[string]any{"op": "setParameter", "key": "note", "value": "second"})
	conflict := h.call("POST", "/api/drafts/"+v.ID+"/submit", map[string]any{"revision": v.Revision, "mode": "branch", "branchName": "promote/prod-2", "commitMessage": "Second"}, nil, 409)
	if conflict["code"] != "conflict" {
		t.Fatalf("conflict: %v", conflict)
	}
	// Rebase, then submit in branch mode: no MR.
	var rebased viewT
	h.call("POST", "/api/drafts/"+v.ID+"/rebase", map[string]any{"revision": v.Revision}, &rebased, 200)
	mrs := len(h.gl.MRs)
	h.call("POST", "/api/drafts/"+v.ID+"/submit", map[string]any{"revision": rebased.Revision, "mode": "branch", "branchName": "promote/prod-2", "commitMessage": "Second"}, &sub, 200)
	if len(h.gl.MRs) != mrs || sub.Submission.Mode != "branch" || !h.gl.HasBranch("promote/prod-2") {
		t.Fatalf("branch mode opened an MR or did not push: %+v", sub)
	}
	// Revert in branch mode.
	h.gl.Merge("promote/prod-2")
	h.call("POST", "/api/submissions/"+sub.Submission.ID+"/revert", map[string]any{"mode": "branch", "branchName": "revert/prod-2"}, &rev, 200)
	if len(h.gl.MRs) != mrs || !h.gl.HasBranch("revert/prod-2") {
		t.Fatal("branch-mode revert opened an MR or did not push")
	}
}

func TestDraftSubmitErrorsAreTyped(t *testing.T) {
	h := newDraftHarness(t)
	v := h.draft()
	blocked := h.call("POST", "/api/drafts/"+v.ID+"/submit", map[string]any{"revision": v.Revision, "commitMessage": "x"}, nil, 422)
	if blocked["code"] != "blocked" {
		t.Fatalf("blocked: %v", blocked)
	}
	v = readyDraft(h)
	// Preflight: the default branch is never a submission branch.
	pre := h.call("POST", "/api/drafts/"+v.ID+"/submit", map[string]any{"revision": v.Revision, "branchName": "main", "commitMessage": "x"}, nil, 424)
	if d, _ := pre["details"].(map[string]any); pre["code"] != "preflight" || d["retryable"] != true {
		t.Fatalf("preflight: %v", pre)
	}
	// Preflight: a protected branch.
	h.gl.Protected["main"] = false
	pre = h.call("POST", "/api/drafts/"+v.ID+"/submit", map[string]any{"revision": v.Revision, "branchName": "promote/x", "commitMessage": "x"}, nil, 424)
	if pre["code"] != "preflight" {
		t.Fatalf("unprotected default: %v", pre)
	}
	h.gl.Protected["main"] = true
	// A file changed since the draft was based.
	h.gl.Edit("main", "customer_secrets.yml", repoFile(t, "internal/pipelinesecrets/testdata/customer_secrets.yml")+"# edited\n")
	conflict := h.call("POST", "/api/drafts/"+v.ID+"/submit", map[string]any{"revision": v.Revision, "branchName": "promote/y", "commitMessage": "x"}, nil, 409)
	if d, _ := conflict["details"].(map[string]any); conflict["code"] != "conflict" || d["fix"] != "rebase" {
		t.Fatalf("conflict: %v", conflict)
	}
	// Stale revision on submit.
	stale := h.call("POST", "/api/drafts/"+v.ID+"/submit", map[string]any{"revision": 1, "commitMessage": "x"}, nil, 409)
	if stale["code"] != "stale_revision" {
		t.Fatalf("stale: %v", stale)
	}
	if len(h.gl.MRs) != 0 {
		t.Fatal("a refused submission opened an MR")
	}
}

func TestDraftsSurviveRestart(t *testing.T) {
	h := newDraftHarness(t)
	v := readyDraft(h)
	dv, _ := davincitest.New(t, filepath.Join("..", "davinci", "testdata", "source"), draftSourceEnv)
	h.start(dv)
	var after viewT
	h.call("GET", "/api/drafts/"+v.ID, nil, &after, 200)
	if !after.NeedsRebase || after.Revision != v.Revision {
		t.Fatalf("after restart %+v", after)
	}
	h.call("POST", "/api/drafts/"+v.ID+"/submit", map[string]any{"revision": after.Revision, "commitMessage": "x"}, nil, 409)
	var rebased viewT
	h.call("POST", "/api/drafts/"+v.ID+"/rebase", map[string]any{"revision": after.Revision}, &rebased, 200)
	if rebased.NeedsRebase || len(rebased.Blockers) != 0 || rebased.Revision != after.Revision+1 {
		t.Fatalf("rebased %+v", rebased)
	}
	var list []map[string]any
	h.call("GET", "/api/drafts", nil, &list, 200)
	if len(list) != 1 {
		t.Fatalf("drafts %v", list)
	}
	// Persisted metadata only: no inventory content or secrets on disk.
	raw, err := os.ReadFile(filepath.Join(h.st.Directory(), store.KindDrafts, v.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if leakRE.Match(raw) || strings.Contains(string(raw), "graphData") || strings.Contains(string(raw), "properties") {
		t.Fatal("draft file holds more than metadata")
	}
}

func TestDraftRoutesKeepLoopbackSecurity(t *testing.T) {
	h := newDraftHarness(t)
	for _, tc := range []struct {
		name   string
		mutate func(*http.Request)
		status int
	}{
		{"no csrf", func(r *http.Request) { r.Header.Del("X-CSRF-Token") }, 403},
		{"cross origin", func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }, 403},
		{"cross site", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, 403},
	} {
		req, _ := http.NewRequest("POST", h.server.URL+"/api/drafts", strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CSRF-Token", h.csrf)
		req.Header.Set("Origin", h.server.URL)
		tc.mutate(req)
		res, err := h.http.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != tc.status {
			t.Errorf("%s: %d", tc.name, res.StatusCode)
		}
	}
	// No cookie.
	res, err := http.Get(h.server.URL + "/api/drafts")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatalf("no cookie: %d", res.StatusCode)
	}
	if csp := res.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
		t.Fatal("CSP missing on draft routes")
	}
}

// Negative control for the leak scan: when the operator marks a value as
// not secret, it is kept literally and the scan would catch it.
func TestLeakScanDetectsLiteralSecrets(t *testing.T) {
	h := newDraftHarness(t)
	v := h.draft()
	v = h.patch(v,
		map[string]any{"op": "select", "kind": "flow", "key": "Login"},
		map[string]any{"op": "markNotSecret", "site": "node:Login/n3sso/hmacKey"},
	)
	req, _ := http.NewRequest("GET", h.server.URL+"/api/drafts/"+v.ID+"/file", nil)
	req.Header.Set("Origin", h.server.URL)
	res, err := h.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !leakRE.Match(raw) {
		t.Fatal("leak scan would not have detected a literal fixture secret")
	}
}
