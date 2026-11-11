package server

import (
	"net/url"
	"regexp"
	"strings"
	"testing"
)

const (
	flowB       = "pingone_davinci_flow.pingcli__Flow-0020-B"
	flowBID     = "f0000000000000000000000000000002"
	subflowAddr = "pingone_davinci_flow.pingcli__Flow-0020-B-0020-Subflow"
)

var subflowIDVar = strings.NewReplacer("-", "_", ".", "_").Replace(subflowAddr) + "_id"

// selectByID searches by ID and adds the first result.
func selectByID(t *testing.T, b *browser, draft, query, addr string) {
	t.Helper()
	p := b.wait(b.post(draft+"/search", url.Values{"q": {query}}))
	mustContain(t, p.body, addr)
	p = b.post(draft+"/pick", url.Values{"address": {addr}, "op": {"select"}})
	if p.status != 200 || !strings.Contains(p.body, "unselect") {
		t.Fatalf("pick: %d\n%s", p.status, p.body)
	}
}

// previewDraft saves the form with action=preview and waits.
func previewDraft(t *testing.T, b *browser, draft string, form url.Values) page {
	t.Helper()
	form.Set("action", "preview")
	p := b.wait(b.post(draft+"/save", form))
	if p.path != draft+"/preview" {
		t.Fatalf("preview ended at %s:\n%s", p.path, p.body)
	}
	return p
}

var branchRE = regexp.MustCompile(`Request (\d{8}-\d{6}-[a-z0-9]{6})`)

func requestID(t *testing.T, body string) string {
	t.Helper()
	m := branchRE.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no request ID on the page")
	}
	return m[1]
}

func TestEndToEndMergeRequestWithMissingValuesAndRollout(t *testing.T) {
	e := newEnv(t, nil)
	b := e.browser()
	draft := b.newDraft()
	b.post(draft+"/save", mainForm("Jane Doe", nil))

	// Search by name lists type, name and ID; search by ID finds one item.
	p := b.wait(b.post(draft+"/search", url.Values{"q": {"Flow"}}))
	mustContain(t, p.body, flowB, flowBID, "davinci flow", subflowAddr)
	p = b.post(draft+"/search", url.Values{"q": {flowBID}})
	if strings.Contains(p.body, subflowAddr) || !strings.Contains(p.body, flowB) {
		t.Fatalf("search by ID:\n%s", p.body)
	}
	selectByID(t, b, draft, flowBID, flowB)

	// Preview: statuses, actions, warning, readiness.
	p = previewDraft(t, b, draft, mainForm("Jane Doe", nil))
	mustContain(t, p.body, "Summary by type", "changed in source", "updated", "kept", "Readiness by environment",
		"Merging adds and changes resources; it never deletes. A Git revert can delete resources in Terraform &mdash; fix forward with a new request instead.",
		"lifecycle { prevent_destroy = true }", "Submit to GitLab", "Diff")
	for _, word := range []string{"target", "Target", "destination", "Destination"} {
		if strings.Contains(p.body, word) {
			t.Fatalf("the page uses the word %q", word)
		}
	}

	// Exclude an unmanaged dependency: every rollout environment now needs
	// its ID, which blocks submit and shows a form.
	p = b.wait(b.post(draft+"/choices", url.Values{"c:" + subflowAddr: {"exclude"}}))
	mustContain(t, p.body, "Fill in missing values", "blocked", `name="v:qa:`+subflowIDVar+`"`, `name="v:prod:`+subflowIDVar+`"`)
	if strings.Contains(p.body, "Submit to GitLab") {
		t.Fatal("submit offered while blocked")
	}
	if p := b.post(draft+"/submit", url.Values{"mode": {"mr"}}); !strings.Contains(p.body, "The preview has problems") {
		t.Fatalf("blocked submit accepted:\n%s", p.body)
	}

	// A secret-looking value is refused and not kept.
	p = b.post(draft+"/values", url.Values{"v:qa:" + subflowIDVar: {"-----BEGIN RSA PRIVATE KEY-----"}})
	mustContain(t, p.body, "Nothing was saved.", "looks like a secret")
	if strings.Contains(p.body, "BEGIN RSA PRIVATE KEY") {
		t.Fatal("the refused value was shown again")
	}

	vals := url.Values{}
	for env, id := range map[string]string{"qa": "f2000000000000000000000000000003", "stage": "f3000000000000000000000000000003", "prod": "f4000000000000000000000000000003"} {
		vals.Set("v:"+env+":"+subflowIDVar, id)
	}
	p = b.wait(b.post(draft+"/values", vals))
	mustContain(t, p.body, "Submit to GitLab")
	if strings.Contains(p.body, ">blocked<") {
		t.Fatalf("still blocked:\n%s", p.body)
	}
	id := requestID(t, p.body)

	// Submit as a merge request.
	p = b.wait(b.post(draft+"/submit", url.Values{"mode": {"mr"}}))
	mustContain(t, p.body, "Submitted. A merge request is open for review.", "/-/merge_requests/1", "pingone-promote/"+id)
	if len(e.gl.MRs) != 1 || e.gl.MRs[0].SourceBranch != "pingone-promote/"+id || e.gl.MRs[0].Title != "Flow B from dev" {
		t.Fatalf("MRs: %+v", e.gl.MRs)
	}
	head := e.gl.Head("pingone-promote/" + id)
	msg := e.gl.Commits[head].Message
	mustContain(t, msg, "Requested-by: Jane Doe (unverified)", "Request-id: "+id)
	files := e.gl.Files("pingone-promote/" + id)
	mustContain(t, files["qa.tfvars"], subflowIDVar, "f2000000000000000000000000000003")
	for p, c := range files {
		for _, v := range sentinels {
			if strings.Contains(c, v) {
				t.Fatalf("a secret value was committed to %s", p)
			}
		}
	}

	// Requests page: merged, with rollout from the default-branch pipeline.
	p = b.get("/requests")
	mustContain(t, p.body, "pingone-promote/"+id, "!1", "opened", "not started")
	sha := e.status.MergeMR("pingone-promote/" + id)
	e.status.SetPipeline(sha, "main", map[string]string{"apply:qa": "success", "apply:stage": "running", "apply:prod": "manual", "plan:prod": "success"})
	p = b.get("/requests")
	mustContain(t, p.body, "merged")
	row := p.body[strings.Index(p.body, "pingone-promote/"+id):]
	iq, is, ip := strings.Index(row, ">passed<"), strings.Index(row, ">running<"), strings.Index(row, ">manual<")
	if iq < 0 || is < 0 || ip < 0 || !(iq < is && is < ip) {
		t.Fatalf("rollout cells in rollout order:\n%s", row)
	}
}

func TestEndToEndBranchOnly(t *testing.T) {
	e := newEnv(t, nil)
	b := e.browser()
	draft := b.newDraft()
	b.post(draft+"/save", mainForm("Sam", nil))
	selectByID(t, b, draft, flowBID, flowB)
	p := previewDraft(t, b, draft, mainForm("Sam", nil))
	id := requestID(t, p.body)
	runs := len(fakeTerraformer(t).Invocations(t))
	p = b.wait(b.post(draft+"/submit", url.Values{"mode": {"branch"}}))
	if got := len(fakeTerraformer(t).Invocations(t)); got != runs {
		t.Fatalf("submit ran the exporter %d times; it must replay the previewed export", got-runs)
	}
	mustContain(t, p.body, "Submitted to a branch. No merge request was opened.", "pingone-promote/"+id)
	if len(e.gl.MRs) != 0 || !e.gl.HasBranch("pingone-promote/"+id) {
		t.Fatalf("branch only: MRs=%d branch=%v", len(e.gl.MRs), e.gl.HasBranch("pingone-promote/"+id))
	}
	// A branch without a merge request is listed too.
	mustContain(t, b.get("/requests").body, "pingone-promote/"+id, "Branch only, no merge request.")
	// Submitting again is idempotent.
	p = b.wait(b.post(draft+"/submit", url.Values{"mode": {"branch"}}))
	mustContain(t, p.body, "already submitted")
}

func TestEndToEndConflictWhenDefaultBranchMoves(t *testing.T) {
	e := newEnv(t, nil)
	b := e.browser()
	draft := b.newDraft()
	b.post(draft+"/save", mainForm("Kim", nil))
	selectByID(t, b, draft, flowBID, flowB)
	p := previewDraft(t, b, draft, mainForm("Kim", nil))
	id := requestID(t, p.body)
	e.gl.Edit("main", "README.tf", "# someone else merged meanwhile\n")
	commits := len(e.gl.CommitOrder)
	p = b.wait(b.post(draft+"/submit", url.Values{"mode": {"mr"}}))
	mustContain(t, p.body, ConflictMessage, "Preview again")
	if e.gl.HasBranch("pingone-promote/"+id) || len(e.gl.MRs) != 0 || len(e.gl.CommitOrder) != commits {
		t.Fatal("a conflict must commit nothing")
	}
	// Preview again on the new head, then submit.
	p = b.wait(b.post(draft+"/preview", nil))
	mustContain(t, p.body, "Submit to GitLab")
	p = b.wait(b.post(draft+"/submit", url.Values{"mode": {"mr"}}))
	mustContain(t, p.body, "Submitted. A merge request is open for review.")
	if _, ok := e.gl.Files("pingone-promote/" + id)["README.tf"]; !ok {
		t.Fatal("the request branch must start from the new default branch head")
	}
}

func TestEndToEndChangedRequestNeedsNewPreview(t *testing.T) {
	e := newEnv(t, nil)
	b := e.browser()
	draft := b.newDraft()
	b.post(draft+"/save", mainForm("Lee", nil))
	selectByID(t, b, draft, flowBID, flowB)
	previewDraft(t, b, draft, mainForm("Lee", nil))
	// Turning dependencies off after the preview changes the inputs.
	b.post(draft+"/save", mainForm("Lee", func(v url.Values) { v.Del("upstream") }))
	p := b.get(draft + "/preview")
	mustContain(t, p.body, "The request changed after this preview")
	p = b.post(draft+"/submit", url.Values{"mode": {"mr"}})
	mustContain(t, p.body, "The request changed after the preview. Preview again.")
	if len(e.gl.MRs) != 0 {
		t.Fatal("a stale preview was submitted")
	}
}

func TestEndToEndNoChanges(t *testing.T) {
	e := newEnv(t, nil)
	b := e.browser()
	draft := b.newDraft()
	b.post(draft+"/save", mainForm("Ana", nil))
	// Flow A is identical in the source and in Git.
	selectByID(t, b, draft, "f0000000000000000000000000000001", "pingone_davinci_flow.pingcli__Flow-0020-A")
	p := previewDraft(t, b, draft, mainForm("Ana", func(v url.Values) { v.Del("upstream") }))
	mustContain(t, p.body, "Submit to GitLab")
	commits := len(e.gl.CommitOrder)
	p = b.wait(b.post(draft+"/submit", url.Values{"mode": {"mr"}}))
	mustContain(t, p.body, "Git already has all of this. Nothing was committed.")
	if len(e.gl.CommitOrder) != commits || len(e.gl.MRs) != 0 {
		t.Fatal("a no-change request committed something")
	}
}
