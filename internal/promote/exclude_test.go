package promote

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zclconf/go-cty/cty"

	"pingone-promote/internal/exporter"
	"pingone-promote/internal/exporter/exportertest"
	"pingone-promote/internal/hclblocks"
	"pingone-promote/internal/neutralize"
)

const (
	subflowAddr  = "pingone_davinci_flow.pingcli__Flow-0020-B-0020-Subflow"
	pingOneAddr  = "pingone_davinci_connector_instance.pingcli__PingOne"
	pingOneIDVar = "pingone_davinci_connector_instance_pingcli__PingOne_id"
)

var rollout = []string{"qa", "stage", "prod"}

func flowWithCompanions(flow string) []neutralize.Excluded {
	out := []neutralize.Excluded{{Address: flow}}
	for _, c := range exporter.Companions(flow) {
		out = append(out, neutralize.Excluded{Address: c})
	}
	return out
}

func managed(ex []neutralize.Excluded) []neutralize.Excluded {
	out := append([]neutralize.Excluded(nil), ex...)
	for i := range out {
		out[i].Managed = true
	}
	return out
}

func mergeWhole(t *testing.T, head Repo, excluded []neutralize.Excluded, choices map[string]Choice) (*neutralize.Result, *MergeResult) {
	t.Helper()
	exp := neutralizedExcluding(t, exportertest.FixtureDir("dev"), excluded)
	res, err := Merge(MergeRequest{Export: exp, Head: head, WholeEnvironment: true, Choices: choices, SourceEnv: "dev", Rollout: rollout})
	if err != nil {
		t.Fatal(err)
	}
	return exp, res
}

func actionsOf(res *MergeResult) map[string]Action {
	out := map[string]Action{}
	for _, c := range res.Changes {
		out[c.Address] = c.Action
	}
	return out
}

func blockBytes(t *testing.T, repo MapRepo, p, key string) []byte {
	t.Helper()
	f, err := hclblocks.Load(p, repo[p])
	if err != nil {
		t.Fatal(err)
	}
	i, _ := f.Find(key)
	if i < 0 {
		t.Fatalf("%s: %s is missing", p, key)
	}
	return f.Blocks[i].Text(f.Src)
}

// Whole environment: everything new is added, nothing is deleted, anything
// that differs from Git stays the Git version, and a re-run is a no-op.
func TestWholeEnvironmentAddsNewKeepsGitAndIsIdempotent(t *testing.T) {
	head := readTree(t, filepath.Join("testdata", "repo"))
	_, res := mergeWhole(t, head, nil, nil)
	if res.Blocked() {
		t.Fatalf("blockers: %+v", res.Blockers)
	}
	got := actionsOf(res)
	for _, a := range []string{subflowAddr, "pingone_davinci_flow_deploy.pingcli__Flow-0020-B-0020-Subflow", "pingone_davinci_flow_enable.pingcli__Flow-0020-B-0020-Subflow"} {
		if got[a] != ActionAdded {
			t.Errorf("%s: %s", a, got[a])
		}
	}
	// Flow B and the PingOne connection differ from Git: kept, never updated.
	if got[flowB] != ActionKeptDiffers || got[pingOneAddr] != ActionKeptDiffers {
		t.Errorf("differing resources must be kept: %s %s", got[flowB], got[pingOneAddr])
	}
	for _, c := range res.Changes {
		if c.Role != RoleEnvironment {
			t.Errorf("%s has role %s", c.Address, c.Role)
		}
		if c.Action == ActionUpdated {
			t.Errorf("%s was updated without a choice", c.Address)
		}
	}
	after := apply(head, res)
	// Nothing is deleted and no existing block changes.
	for _, p := range sortedKeys(head) {
		if !strings.HasPrefix(p, "definitions/") || !strings.HasSuffix(p, ".tf") {
			continue
		}
		before, err := hclblocks.Scan(head[p], p)
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range before {
			if !bytes.Equal(b.Text(head[p]), blockBytes(t, after, p, b.Key())) {
				t.Fatalf("%s: %s changed", p, b.Key())
			}
		}
	}
	// A second run against the result changes nothing.
	exp2 := neutralizedExcluding(t, exportertest.FixtureDir("dev"), nil)
	res2, err := Merge(MergeRequest{Export: exp2, Head: after, WholeEnvironment: true, SourceEnv: "dev", Rollout: rollout})
	if err != nil {
		t.Fatal(err)
	}
	if len(res2.Files) != 0 {
		t.Fatalf("a re-run must be a no-op, it changes %v", res2.ChangedPaths())
	}
	for a, act := range actionsOf(res2) {
		if act == ActionAdded || act == ActionUpdated || act == ActionMoved {
			t.Errorf("re-run: %s %s", a, act)
		}
	}
	// A choice still updates a differing resource.
	_, upd := mergeWhole(t, head, nil, map[string]Choice{flowB: ChoiceUpdate})
	if actionsOf(upd)[flowB] != ActionUpdated {
		t.Fatalf("update choice: %v", actionsOf(upd)[flowB])
	}
}

// Excluding a flow in whole-environment scope: not exported, its Git blocks
// are byte-identical, everything else is still brought in.
func TestWholeEnvironmentExcludeFlowLeavesGitBlocks(t *testing.T) {
	head := readTree(t, filepath.Join("testdata", "repo"))
	ex := managed(flowWithCompanions(flowB))
	exp, res := mergeWhole(t, head, ex, map[string]Choice{flowB: ChoiceUpdate})
	for _, e := range ex {
		if exp.Block(e.Address) != nil {
			t.Fatalf("%s was exported", e.Address)
		}
	}
	got := actionsOf(res)
	for _, e := range ex {
		if got[e.Address] != ActionExcluded {
			t.Errorf("%s: %s", e.Address, got[e.Address])
		}
	}
	if got[subflowAddr] != ActionAdded {
		t.Errorf("the rest of the environment is still added: %v", got[subflowAddr])
	}
	after := apply(head, res)
	for _, e := range ex {
		typ, _, _ := strings.Cut(e.Address, ".")
		p := "definitions/" + typ + ".tf"
		if !bytes.Equal(blockBytes(t, head, p, "resource."+e.Address), blockBytes(t, after, p, "resource."+e.Address)) {
			t.Fatalf("%s changed", e.Address)
		}
	}
	for _, c := range res.Changes {
		if c.Address == flowB && (c.Role != RoleExcluded || !strings.Contains(c.Reason, "Git-managed copy kept")) {
			t.Fatalf("excluded change: %+v", c)
		}
	}
}

// An excluded dependency that Git already manages behaves exactly like keep.
func TestExcludingManagedDependencyBehavesLikeKeep(t *testing.T) {
	head := readTree(t, filepath.Join("testdata", "repo"))
	keep := mergeFlowB(t, head, map[string]Choice{pingOneAddr: ChoiceKeep})

	exp := neutralizedExcluding(t, exportertest.FixtureDir("dev"), []neutralize.Excluded{{Address: pingOneAddr, Managed: true}}, flowB)
	res, err := Merge(MergeRequest{Export: exp, Head: head, Selected: selectedFor(flowB), SourceEnv: "dev", Rollout: rollout,
		Choices: map[string]Choice{pingOneAddr: ChoiceExclude}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Blocked() {
		t.Fatalf("blockers: %+v", res.Blockers)
	}
	if len(res.Files) != len(keep.Files) {
		t.Fatalf("files: exclude %v, keep %v", res.ChangedPaths(), keep.ChangedPaths())
	}
	for p, b := range keep.Files {
		if !bytes.Equal(b, res.Files[p]) {
			t.Errorf("%s differs between exclude and keep", p)
		}
	}
	for _, c := range res.Changes {
		if c.Address == pingOneAddr {
			if c.Action != ActionExcluded || c.Reason != "excluded; Git-managed copy kept" || c.Role != RoleDependency {
				t.Fatalf("change: %+v", c)
			}
		}
	}
	if strings.Contains(string(res.Files["definitions/variables.tf"]), pingOneIDVar) || strings.Contains(string(res.Files["variables.tf"]), pingOneIDVar) {
		t.Fatal("a managed dependency needs no ID variable")
	}
}

func idVarValues(drop string) map[string]map[string]cty.Value {
	vals := envValues()
	for env, id := range map[string]string{"qa": "c2000000000000000000000000000002", "stage": "c3000000000000000000000000000002", "prod": "c4000000000000000000000000000002"} {
		if drop != pingOneIDVar {
			vals[env][pingOneIDVar] = cty.StringVal(id)
		}
		delete(vals[env], "davinci_application_Web_oauth_redirect_uris") // no application in this request
	}
	return vals
}

// An excluded dependency that Git does not manage: ID variable declared in
// both modules, the source overlay valued, a blocker per other environment.
func TestExcludingUnmanagedDependencyNeedsPerEnvironmentIDs(t *testing.T) {
	head := MapRepo{"customer_secrets.yml": []byte(fixtureCustomerSecrets)}
	exp := neutralizedExcluding(t, exportertest.FixtureDir("dev"), []neutralize.Excluded{{Address: pingOneAddr}}, flowB)
	run := func(vals map[string]map[string]cty.Value) *MergeResult {
		res, err := Merge(MergeRequest{Export: exp, Head: head, Selected: selectedFor(flowB), SourceEnv: "dev", Rollout: rollout,
			EnvValues: vals, Choices: map[string]Choice{pingOneAddr: ChoiceExclude}})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	res := run(idVarValues(pingOneIDVar))
	after := apply(head, res)
	if !strings.Contains(string(after["definitions/variables.tf"]), `variable "`+pingOneIDVar+`"`) ||
		!strings.Contains(string(after["variables.tf"]), `variable "`+pingOneIDVar+`"`) {
		t.Fatal("the ID variable must be declared in definitions/variables.tf and variables.tf")
	}
	if !strings.Contains(string(after["main.tf"]), pingOneIDVar+" = var."+pingOneIDVar) {
		t.Fatalf("module input missing:\n%s", after["main.tf"])
	}
	if !strings.Contains(string(after["dev.tfvars"]), pingOneIDVar+" = \"c0000000000000000000000000000002\"") {
		t.Fatalf("source overlay:\n%s", after["dev.tfvars"])
	}
	seen := map[string]bool{}
	for _, b := range res.Blockers {
		if b.Code != BlockMissingValue || b.Variable != pingOneIDVar {
			t.Fatalf("unexpected blocker: %+v", b)
		}
		if b.Address != pingOneAddr || !strings.Contains(b.Message, pingOneAddr) || !strings.Contains(b.Message, b.Env+".tfvars") {
			t.Fatalf("the blocker must name the dependency: %+v", b)
		}
		seen[b.Env] = true
	}
	if len(seen) != 3 || seen["dev"] {
		t.Fatalf("want a missing_value blocker for qa, stage and prod only: %+v", res.Blockers)
	}
	var found bool
	for _, c := range res.Changes {
		if c.Address == pingOneAddr {
			found = true
			if c.Action != ActionExcluded || c.Reason != "excluded; referenced by ID variable `"+pingOneIDVar+"`" || c.Role != RoleDependency {
				t.Fatalf("change: %+v", c)
			}
		}
	}
	if !found {
		t.Fatal("excluded dependency missing from the changes")
	}
	// With the values supplied, nothing blocks and the root passes check.
	res = run(idVarValues(""))
	if res.Blocked() {
		t.Fatalf("blockers: %+v", res.Blockers)
	}
	after = apply(head, res)
	dir := t.TempDir()
	writeTree(t, dir, after)
	rep, err := CheckRoot(context.Background(), CheckOptions{Root: dir, Envs: []string{"dev", "qa", "stage", "prod"}, CustomerSecrets: after["customer_secrets.yml"]})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK {
		t.Fatalf("check: %+v", rep.Problems)
	}
	for _, env := range rollout {
		if !strings.Contains(string(after[env+".tfvars"]), pingOneIDVar) {
			t.Fatalf("%s.tfvars lacks the ID", env)
		}
	}
}

// The embedded (subflow) case: terraformer v0.6.0 leaves the reference, the
// neutralizer repairs it, and the merge declares the variable.
func TestExcludingUnmanagedSubflowUsesIDVariable(t *testing.T) {
	head := readTree(t, filepath.Join("testdata", "repo"))
	ex := flowWithCompanions(subflowAddr)
	ex[0].ID = "f0000000000000000000000000000003"
	exp := neutralizedExcluding(t, exportertest.FixtureDir("dev"), ex, flowB)
	res, err := Merge(MergeRequest{Export: exp, Head: head, Selected: selectedFor(flowB), SourceEnv: "dev", Rollout: rollout})
	if err != nil {
		t.Fatal(err)
	}
	v := neutralize.ExcludedVariableName(subflowAddr)
	after := apply(head, res)
	if !strings.Contains(string(after["dev.tfvars"]), v+" = \"f0000000000000000000000000000003\"") {
		t.Fatalf("dev.tfvars:\n%s", after["dev.tfvars"])
	}
	for _, b := range res.Blockers {
		if b.Code != BlockMissingValue || b.Variable != v || b.Env == "dev" {
			t.Fatalf("unexpected blocker %+v", b)
		}
	}
	if len(res.Blockers) != 3 {
		t.Fatalf("blockers: %+v", res.Blockers)
	}
	flow := string(blockBytes(t, after, "definitions/pingone_davinci_flow.tf", "resource."+flowB))
	if strings.Contains(flow, subflowAddr) || !strings.Contains(flow, "var."+v) {
		t.Fatalf("flow B:\n%s", flow)
	}
	// The subflow and its companions are neither added nor in Git.
	for _, e := range ex {
		typ, label, _ := strings.Cut(e.Address, ".")
		if bytes.Contains(after["definitions/"+typ+".tf"], []byte(`"`+label+`"`)) {
			t.Fatalf("%s was written", e.Address)
		}
	}
}
