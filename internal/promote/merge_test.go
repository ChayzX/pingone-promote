package promote

import (
	"bytes"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zclconf/go-cty/cty"

	"pingone-promote/internal/exporter"
	"pingone-promote/internal/exporter/exportertest"
	"pingone-promote/internal/hclblocks"
	"pingone-promote/internal/neutralize"
)

var update = flag.Bool("update", false, "rewrite golden files")

const flowB = "pingone_davinci_flow.pingcli__Flow-0020-B"

func selectedFor(flow string) []string {
	return append([]string{flow}, exporter.Companions(flow)...)
}

func mergeFlowB(t *testing.T, head MapRepo, choices map[string]Choice) *MergeResult {
	t.Helper()
	exp := neutralized(t, exportertest.FixtureDir("dev"), flowB)
	res, err := Merge(MergeRequest{Export: exp, Head: head, Selected: selectedFor(flowB), Choices: choices,
		SourceEnv: "dev", Rollout: []string{"qa", "stage", "prod"}})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func goldenFile(t *testing.T, name string, got []byte) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(p, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, got) {
		t.Fatalf("%s differs (go test ./internal/promote -update):\n%s", p, got)
	}
}

func TestScopedMergeOfFlowB(t *testing.T) {
	head := readTree(t, filepath.Join("testdata", "repo"))
	res := mergeFlowB(t, head, nil)

	actions := map[string]Action{}
	for _, c := range res.Changes {
		actions[c.Address] = c.Action
	}
	want := map[string]Action{
		flowB: ActionUpdated,
		"pingone_davinci_flow_deploy.pingcli__Flow-0020-B":              ActionUnchanged,
		"pingone_davinci_flow_enable.pingcli__Flow-0020-B":              ActionUnchanged,
		"pingone_davinci_flow.pingcli__Flow-0020-B-0020-Subflow":        ActionAdded,
		"pingone_davinci_flow_deploy.pingcli__Flow-0020-B-0020-Subflow": ActionAdded,
		"pingone_davinci_flow_enable.pingcli__Flow-0020-B-0020-Subflow": ActionAdded,
		"pingone_davinci_connector_instance.pingcli__PingOne":           ActionKeptDiffers,
		"pingone_davinci_connector_instance.pingcli__Http":              ActionUnchanged,
		"pingone_davinci_variable.pingcli__apiKey_company":              ActionUnchanged,
		"pingone_davinci_variable.pingcli__attempts_flowInstance":       ActionUnchanged,
		"pingone_davinci_variable.pingcli__companyName_company":         ActionUnchanged,
	}
	if len(actions) != len(want) {
		t.Fatalf("changes: %+v", res.Changes)
	}
	for a, act := range want {
		if actions[a] != act {
			t.Errorf("%s: got %s, want %s", a, actions[a], act)
		}
	}
	if res.Blocked() {
		t.Fatalf("blockers: %+v", res.Blockers)
	}
	// Only these files change.
	wantFiles := "definitions/pingone_davinci_flow.tf definitions/pingone_davinci_flow_deploy.tf definitions/pingone_davinci_flow_enable.tf dev.tfvars imports.tf"
	if got := strings.Join(res.ChangedPaths(), " "); got != wantFiles {
		t.Fatalf("changed files: %s", got)
	}
	after := apply(head, res)

	// Every block other than the written ones is byte-identical, and
	// nothing is deleted.
	written := map[string]bool{flowB: true,
		"pingone_davinci_flow.pingcli__Flow-0020-B-0020-Subflow": true, "pingone_davinci_flow_deploy.pingcli__Flow-0020-B-0020-Subflow": true,
		"pingone_davinci_flow_enable.pingcli__Flow-0020-B-0020-Subflow": true}
	for _, p := range sortedKeys(head) {
		if !strings.HasPrefix(p, "definitions/") {
			continue
		}
		before, err := hclblocks.Scan(head[p], p)
		if err != nil {
			t.Fatal(err)
		}
		afterFile, err := hclblocks.Load(p, after[p])
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range before {
			i, _ := afterFile.Find(b.Key())
			if i < 0 {
				t.Fatalf("%s: %s was deleted", p, b.Key())
			}
			if written[b.Address()] {
				continue
			}
			if !bytes.Equal(b.Text(head[p]), afterFile.Blocks[i].Text(afterFile.Src)) {
				t.Fatalf("%s: %s changed", p, b.Key())
			}
		}
		// Comments outside blocks survive verbatim.
		for _, line := range strings.Split(string(head[p]), "\n") {
			if strings.HasPrefix(line, "#") && !bytes.Contains(after[p], []byte(line)) {
				t.Fatalf("%s: comment %q lost", p, line)
			}
		}
	}
	// Hunks: removed lines only come from Flow B; added lines only belong
	// to written blocks or are blank separators.
	for _, p := range res.ChangedPaths() {
		if !strings.HasPrefix(p, "definitions/") {
			continue
		}
		oldRanges, newRanges := blockLines(t, p, head[p]), blockLines(t, p, after[p])
		inside := func(r map[string][2]int, line int) string {
			for a, rg := range r {
				if line >= rg[0] && line <= rg[1] {
					return a
				}
			}
			return ""
		}
		for _, op := range DiffLines(splitLines(head[p]), splitLines(after[p])) {
			switch op.Kind {
			case '-':
				if a := inside(oldRanges, op.OldLine); !written[a] {
					t.Fatalf("%s: removed line %d (%q) outside a written block", p, op.OldLine, op.Line)
				}
			case '+':
				if a := inside(newRanges, op.NewLine); !written[a] && strings.TrimSpace(op.Line) != "" {
					t.Fatalf("%s: added line %d (%q) outside a written block", p, op.NewLine, op.Line)
				}
			}
		}
	}
	diff, err := res.Diff(head)
	if err != nil {
		t.Fatal(err)
	}
	goldenFile(t, "flow-b.diff", []byte(diff))

	// The kept shared connection is reported as differing.
	for _, c := range res.Changes {
		if c.Address == "pingone_davinci_connector_instance.pingcli__PingOne" && (c.Status != StatusChangedInSource || c.Reason == "") {
			t.Fatalf("connection report: %+v", c)
		}
	}

	// Re-running the same export is a no-op.
	again := mergeFlowB(t, after, nil)
	if len(again.Files) != 0 {
		t.Fatalf("re-run changed %v", again.ChangedPaths())
	}
}

func TestOptIntoDependencyUpdate(t *testing.T) {
	head := readTree(t, filepath.Join("testdata", "repo"))
	conn := "pingone_davinci_connector_instance.pingcli__PingOne"
	res := mergeFlowB(t, head, map[string]Choice{conn: ChoiceUpdate})
	for _, c := range res.Changes {
		if c.Address == conn && c.Action != ActionUpdated {
			t.Fatalf("%+v", c)
		}
	}
	if !bytes.Contains(res.Files["definitions/pingone_davinci_connector_instance.tf"], []byte(`"value" : "EU"`)) {
		t.Fatal("connection not updated")
	}
	// The trailing hand note in the same file survives.
	if !bytes.Contains(res.Files["definitions/pingone_davinci_connector_instance.tf"], []byte("# Hand-maintained trailing note")) {
		t.Fatal("hand note lost")
	}
}

func TestThreeWayStatus(t *testing.T) {
	head := readTree(t, filepath.Join("testdata", "repo"))
	exp := neutralized(t, exportertest.FixtureDir("dev"), flowB)
	// Base == head: the source changed (changed-in-source).
	res, err := Merge(MergeRequest{Export: exp, Head: head, Base: head, Selected: selectedFor(flowB), SourceEnv: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	if st := statusOf(res, flowB); st != StatusChangedInSource {
		t.Fatalf("got %s", st)
	}
	// Base == live: Git is newer; the Git version is kept.
	base := apply(head, mergeFlowB(t, head, nil))
	newer := MapRepo{}
	for k, v := range base {
		newer[k] = v
	}
	newer["definitions/pingone_davinci_flow.tf"] = bytes.Replace(base["definitions/pingone_davinci_flow.tf"], []byte("Look up the user (v2)"), []byte("Look up the user (v3)"), 1)
	res, err = Merge(MergeRequest{Export: exp, Head: newer, Base: base, Selected: selectedFor(flowB), SourceEnv: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	if st := statusOf(res, flowB); st != StatusGitNewer || actionOf(res, flowB) != ActionKeptDiffers {
		t.Fatalf("got %s %s", st, actionOf(res, flowB))
	}
	// Changed in both: a selected flow blocks until a choice is made.
	res, err = Merge(MergeRequest{Export: exp, Head: newer, Base: head, Selected: selectedFor(flowB), SourceEnv: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	if st := statusOf(res, flowB); st != StatusChangedInBoth || !res.Blocked() {
		t.Fatalf("got %s blocked=%v", st, res.Blocked())
	}
	res, err = Merge(MergeRequest{Export: exp, Head: newer, Base: head, Selected: selectedFor(flowB), SourceEnv: "dev",
		Choices: map[string]Choice{flowB: ChoiceUpdate}})
	if err != nil {
		t.Fatal(err)
	}
	if actionOf(res, flowB) != ActionUpdated {
		t.Fatal("use source must update")
	}
}

func statusOf(r *MergeResult, a string) Status {
	for _, c := range r.Changes {
		if c.Address == a {
			return c.Status
		}
	}
	return ""
}

func actionOf(r *MergeResult, a string) Action {
	for _, c := range r.Changes {
		if c.Address == a {
			return c.Action
		}
	}
	return ""
}

func TestDemanageEmitsRemovedBlock(t *testing.T) {
	head := readTree(t, filepath.Join("testdata", "repo"))
	exp := neutralized(t, exportertest.FixtureDir("dev"), flowB)
	flowC := "pingone_davinci_flow.pingcli__Flow-0020-C"
	res, err := Merge(MergeRequest{Export: exp, Head: head, Selected: selectedFor(flowB), SourceEnv: "dev", Remove: []string{flowC}})
	if err != nil {
		t.Fatal(err)
	}
	removed := string(res.Files["definitions/removed.tf"])
	if !strings.Contains(removed, "from = "+flowC) || !strings.Contains(removed, "destroy = false") {
		t.Fatalf("removed.tf:\n%s", removed)
	}
	if bytes.Contains(res.Files["definitions/pingone_davinci_flow.tf"], []byte(`"pingcli__Flow-0020-C"`)) {
		t.Fatal("flow C block still present")
	}
	if bytes.Contains(res.Files["imports.tf"], []byte("to       = module.davinci."+flowC+"\n")) || bytes.Contains(res.Files["dev.tfvars"], []byte("pingone_davinci_flow.pingcli__Flow-0020-C\"")) {
		t.Fatal("import block or import_ids key for flow C remains")
	}
	blockers, err := ValidateOverlays(apply(head, res), DefaultLayout, []string{"dev", "qa", "stage", "prod"})
	if err != nil || len(blockers) != 0 {
		t.Fatalf("%v %+v", err, blockers)
	}
}

func TestRelabelEmitsMoved(t *testing.T) {
	head := MapRepo{
		"definitions/pingone_davinci_variable.tf": []byte("resource \"pingone_davinci_variable\" \"pingcli__counter_flow\" {\n  name = \"counter\"\n}\n"),
	}
	exp := &neutralize.Result{
		Variables: map[string]*neutralize.Variable{neutralize.EnvironmentIDVariable: {Name: neutralize.EnvironmentIDVariable, Type: "string", Kind: neutralize.KindEnvironmentID}},
		Blocks: []neutralize.Block{{Address: "pingone_davinci_variable.pingcli__counter_flow_One", Type: "pingone_davinci_variable", Label: "pingcli__counter_flow_One",
			File: "pingone_davinci_variable.tf", Text: []byte("resource \"pingone_davinci_variable\" \"pingcli__counter_flow_One\" {\n  name = \"counter\"\n}")}},
		Renames:      []neutralize.Rename{{From: "pingone_davinci_variable.pingcli__counter_flow", To: "pingone_davinci_variable.pingcli__counter_flow_One"}},
		SourceValues: map[string]cty.Value{},
	}
	res, err := Merge(MergeRequest{Export: exp, Head: head, SourceEnv: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res.Files["definitions/moved.tf"]), "from = pingone_davinci_variable.pingcli__counter_flow\n  to   = pingone_davinci_variable.pingcli__counter_flow_One") {
		t.Fatalf("moved.tf: %s", res.Files["definitions/moved.tf"])
	}
	if actionOf(res, "pingone_davinci_variable.pingcli__counter_flow_One") != ActionMoved {
		t.Fatalf("%+v", res.Changes)
	}
}

func TestMissingRolloutValuesAreBlockersPerEnv(t *testing.T) {
	// A new repository: qa has no overlay at all; stage gets only the
	// environment ID.
	exp := neutralized(t, exportertest.FixtureDir("dev"), flowB)
	res, err := Merge(MergeRequest{Export: exp, Head: MapRepo{"customer_secrets.yml": []byte(fixtureCustomerSecrets)}, Selected: selectedFor(flowB),
		SourceEnv: "dev", Rollout: []string{"qa", "stage"},
		EnvValues: map[string]map[string]cty.Value{"stage": {"pingone_environment_id": cty.StringVal(stageEnv)}}})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, b := range res.Blockers {
		got[b.Env] = append(got[b.Env], b.Code+":"+b.Variable)
	}
	if strings.Join(got["qa"], ",") != "missing_overlay:" || strings.Join(got["stage"], ",") != "missing_value:davinci_variable_companyName_value" || len(got["dev"]) != 0 {
		t.Fatalf("blockers: %+v", res.Blockers)
	}
	if !strings.Contains(string(res.Files["customer_secrets.yml"]), "TF_VAR_davinci_variable_apiKey_value") {
		t.Fatal("new secret not declared")
	}
	// Secrets are never accepted as environment values.
	_, err = Merge(MergeRequest{Export: exp, Head: MapRepo{}, SourceEnv: "dev", Rollout: []string{"qa"},
		EnvValues: map[string]map[string]cty.Value{"qa": {"davinci_variable_apiKey_value": cty.StringVal("x")}}})
	if err == nil {
		t.Fatal("secret env value accepted")
	}
	// Values for an environment outside the rollout are refused.
	_, err = Merge(MergeRequest{Export: exp, Head: MapRepo{}, SourceEnv: "dev",
		EnvValues: map[string]map[string]cty.Value{"qa": {"pingone_environment_id": cty.StringVal(qaEnv)}}})
	if err == nil {
		t.Fatal("values for an unknown environment accepted")
	}
}

// TestGitDiffTouchesOnlyExpectedHunks runs the real git diff over the
// merged tree when git is available.
func TestGitDiffTouchesOnlyExpectedHunks(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	head := readTree(t, filepath.Join("testdata", "repo"))
	res := mergeFlowB(t, head, nil)
	dir := t.TempDir()
	writeTree(t, dir, head)
	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	run("init", "-q")
	run("add", "-A")
	run("-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-qm", "base")
	writeTree(t, dir, res.Files)
	stat := run("diff", "--numstat")
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(stat), "\n") {
		f := strings.Fields(l)
		lines = append(lines, f[2]+" -"+f[1])
	}
	// Deletions: one changed node title line in Flow B; dev.tfvars realigns
	// import_ids; nothing else is removed.
	got := strings.Join(lines, "\n")
	for _, l := range lines {
		f := strings.Fields(l)
		switch f[0] {
		case "definitions/pingone_davinci_flow.tf":
			if f[1] != "-1" {
				t.Fatalf("flow file: unexpected deletions\n%s", got)
			}
		case "definitions/pingone_davinci_flow_deploy.tf", "definitions/pingone_davinci_flow_enable.tf", "imports.tf":
			if f[1] != "-0" {
				t.Fatalf("%s must only gain lines\n%s", f[0], got)
			}
		}
	}
	hunks := strings.Count(run("diff", "-U0", "--", "definitions/pingone_davinci_flow.tf"), "\n@@ ")
	if hunks > 3 {
		t.Fatalf("flow file has %d hunks", hunks)
	}
}
