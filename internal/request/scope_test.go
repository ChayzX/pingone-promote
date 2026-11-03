package request

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

const (
	flowBAddr    = "pingone_davinci_flow.pingcli__Flow-0020-B"
	subflowAddr  = "pingone_davinci_flow.pingcli__Flow-0020-B-0020-Subflow"
	pingOneAddr  = "pingone_davinci_connector_instance.pingcli__PingOne"
	pingOneIDVar = "pingone_davinci_connector_instance_pingcli__PingOne_id"
)

func wholeRequest(mut func(map[string]any)) []byte {
	return requestJSON(func(m map[string]any) {
		m["scope"] = "all"
		m["confirmAll"] = true
		m["flows"] = []any{}
		m["title"] = ""
		m["dryRun"] = true
		if mut != nil {
			mut(m)
		}
	})
}

func exportArgv(t *testing.T, h *harness) []string {
	t.Helper()
	invs := h.fake.Invocations(t)
	for i := len(invs) - 1; i >= 0; i-- {
		if len(invs[i].Argv) > 1 && invs[i].Argv[1] == "export" && !slices.Contains(invs[i].Argv, "--list-resources") {
			return invs[i].Argv
		}
	}
	t.Fatal("no export ran")
	return nil
}

func TestWholeEnvironmentPreviewAndSubmit(t *testing.T) {
	req := wholeRequest(nil)
	h := newHarness(t, req)
	res := h.run(req)
	if res.Status != StatusPreviewed || res.Error != nil || res.Scope != "all" {
		t.Fatalf("%s %+v", res.Status, res.Error)
	}
	argv := exportArgv(t, h)
	if slices.Contains(argv, "--include-resources") || slices.Contains(argv, "--include-upstream") || slices.Contains(argv, "--exclude-resources") {
		t.Fatalf("a whole export passes no include or exclude flags: %q", argv)
	}
	if len(res.Selected) != 0 {
		t.Fatalf("selected: %+v", res.Selected)
	}
	for _, c := range res.Changes {
		if c.Role != "environment" || c.Action == "updated" {
			t.Fatalf("whole environment keeps what differs: %+v", c)
		}
	}
	if res.Summary.Added != 3 || res.Summary.KeptDiffers != 2 || res.Summary.Updated != 0 {
		t.Fatalf("summary %+v", res.Summary.Counts)
	}
	flow := res.Summary.ByType["pingone_davinci_flow"]
	if flow.Total != 4 || flow.Added != 1 || flow.KeptDiffers != 1 || flow.Unchanged != 2 {
		t.Fatalf("byType flow: %+v", flow)
	}
	total := 0
	for _, c := range res.Summary.ByType {
		total += c.Total
	}
	if total != len(res.Changes) || len(res.Changes) != 19 {
		t.Fatalf("byType covers %d of %d changes", total, len(res.Changes))
	}
	// Submit: one commit, an MR with the whole-environment title, no deletions.
	req = wholeRequest(func(m map[string]any) { m["dryRun"] = false })
	h = newHarness(t, req)
	res = h.run(req)
	if res.Status != StatusSubmitted || res.Error != nil {
		t.Fatalf("%s %+v", res.Status, res.Error)
	}
	if mr := h.gl.MRs[0]; mr.Title != "[whole environment] PingOne: whole dev environment" {
		t.Fatalf("MR title %q", mr.Title)
	}
	for _, f := range res.Files {
		if f.Action == "delete" && f.Path != ".pingone-promote/request.json" {
			t.Fatalf("whole environment deleted %s", f.Path)
		}
	}
	// A title that has the marker is not prefixed twice; a long one still fits.
	req = wholeRequest(func(m map[string]any) { m["dryRun"] = false; m["title"] = "Bring in dev " + WholeEnvironmentTitle })
	h = newHarness(t, req)
	if res = h.run(req); res.Status != StatusSubmitted || h.gl.MRs[0].Title != "Bring in dev "+WholeEnvironmentTitle {
		t.Fatalf("%s %q", res.Status, h.gl.MRs[0].Title)
	}
	req = wholeRequest(func(m map[string]any) { m["dryRun"] = false; m["title"] = strings.Repeat("x", 255) })
	h = newHarness(t, req)
	if res = h.run(req); res.Status != StatusSubmitted || len(h.gl.MRs[0].Title) != 255 || !strings.HasPrefix(h.gl.MRs[0].Title, WholeEnvironmentTitle+" x") {
		t.Fatalf("%s %q", res.Status, h.gl.MRs[0].Title)
	}
}

func TestScopeValidation(t *testing.T) {
	bad := map[string][]byte{
		"all without confirm":     wholeRequest(func(m map[string]any) { delete(m, "confirmAll") }),
		"all with confirm false":  wholeRequest(func(m map[string]any) { m["confirmAll"] = false }),
		"confirm on selected":     requestJSON(func(m map[string]any) { m["confirmAll"] = true }),
		"unknown scope":           requestJSON(func(m map[string]any) { m["scope"] = "everything" }),
		"selected without items":  requestJSON(func(m map[string]any) { m["scope"] = "selected"; m["flows"] = []any{} }),
		"too many flows selected": requestJSON(func(m map[string]any) { m["flows"] = make([]map[string]string, 21) }),
	}
	for name, b := range bad {
		_, err := Parse(b)
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("%s: accepted (%v)", name, err)
		}
	}
	_, err := Parse(wholeRequest(func(m map[string]any) { delete(m, "confirmAll") }))
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Field != "confirmAll" {
		t.Fatalf("the error must name confirmAll: %v", err)
	}
	// In whole scope the 20-item cap does not apply and the lists may be empty.
	many := make([]map[string]string, 40)
	for i := range many {
		many[i] = map[string]string{"name": "Flow"}
	}
	r, err := Parse(wholeRequest(func(m map[string]any) { m["flows"] = many }))
	if err != nil || !r.Whole() {
		t.Fatalf("whole scope with 40 flows: %v", err)
	}
	if r, err = Parse(requestJSON(nil)); err != nil || r.Scope != "selected" || r.Whole() {
		t.Fatalf("the default scope is selected: %+v %v", r, err)
	}
}

func TestExcludeValidation(t *testing.T) {
	bad := map[string]any{
		"unknown type":              []map[string]string{{"type": "folder", "name": "x"}},
		"type without id or name":   []map[string]string{{"type": "flow"}},
		"address with type":         []map[string]string{{"type": "flow", "address": "pingone_davinci_flow.pingcli__A"}},
		"bad address":               []map[string]string{{"address": "nope"}},
		"bad id":                    []map[string]string{{"type": "flow", "id": "not an id"}},
		"empty entry":               []map[string]string{{}},
		"include also excluded id":  []map[string]string{{"type": "flow", "id": "F0000000000000000000000000000002"}},
		"include also excluded nm":  []map[string]string{{"type": "flow", "name": "flow b"}},
		"unknown field in exclude":  []map[string]any{{"type": "flow", "name": "x", "why": "y"}},
		"more than the cap allowed": make([]map[string]string, MaxExcludes+1),
	}
	for name, ex := range bad {
		_, err := Parse(requestJSON(func(m map[string]any) { m["exclude"] = ex }))
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("%s: accepted (%v)", name, err)
		}
	}
	// The conflict names both entries.
	_, err := Parse(requestJSON(func(m map[string]any) {
		m["exclude"] = []map[string]string{{"type": "flow", "id": "f0000000000000000000000000000002"}}
	}))
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Field != "exclude[0]" || !strings.Contains(ve.Msg, "flows[0]") {
		t.Fatalf("conflict message: %v", err)
	}
	for _, ok := range [][]map[string]string{
		{{"type": "flow", "name": "Flow C"}},
		{{"type": "connection", "name": "PingOne"}},
		{{"type": "variable", "name": "apiKey"}},
		{{"type": "application", "id": "a0000000000000000000000000000001"}},
		{{"type": "flowPolicy", "id": "a0000000000000000000000000000001/b0000000000000000000000000000001"}},
		{{"address": pingOneAddr}},
		// the same name of a different type is not a conflict
		{{"type": "application", "name": "Flow B"}},
	} {
		if _, err := Parse(requestJSON(func(m map[string]any) { m["exclude"] = ok })); err != nil {
			t.Errorf("%v: %v", ok, err)
		}
	}
}

func TestWholeEnvironmentExcludeFlowKeepsItsGitBlocks(t *testing.T) {
	req := wholeRequest(func(m map[string]any) {
		m["dryRun"] = false
		m["exclude"] = []map[string]string{{"type": "flow", "name": "Flow B"}}
	})
	h := newHarness(t, req)
	before := h.gl.Files(h.branch)
	res := h.run(req)
	if res.Status != StatusSubmitted || res.Error != nil {
		t.Fatalf("%s %+v", res.Status, res.Error)
	}
	argv := exportArgv(t, h)
	var excl []string
	for i, a := range argv {
		if a == "--exclude-resources" {
			excl = append(excl, argv[i+1])
		}
	}
	want := []string{"pingone_davinci_flow.pingcli__Flow-0020-B", "pingone_davinci_flow_deploy.pingcli__Flow-0020-B", "pingone_davinci_flow_enable.pingcli__Flow-0020-B"}
	if !slices.Equal(excl, want) || slices.Contains(argv, "--include-resources") {
		t.Fatalf("exclude flags %q in %q", excl, argv)
	}
	var found bool
	for _, c := range res.Changes {
		if strings.Contains(c.Address, "Flow-0020-B") && !strings.Contains(c.Address, "Subflow") && c.Action != "excluded" {
			t.Fatalf("Flow B must only be reported as excluded: %+v", c)
		}
		if c.Address == flowBAddr {
			found = true
			if c.Reason != "excluded; Git-managed copy kept" || c.Role != "excluded" {
				t.Fatalf("%+v", c)
			}
		}
	}
	if !found || res.Summary.Excluded != 1 {
		t.Fatalf("excluded: %+v %d", res.Changes, res.Summary.Excluded)
	}
	// The flow's Git blocks are byte-identical after the commit.
	after := h.gl.Files(h.branch)
	if blockOf(t, before["definitions/pingone_davinci_flow.tf"], flowBAddr) != blockOf(t, after["definitions/pingone_davinci_flow.tf"], flowBAddr) {
		t.Fatal("Flow B changed")
	}
	if !strings.Contains(after["definitions/pingone_davinci_flow.tf"], "Flow-0020-B-0020-Subflow") {
		t.Fatal("the rest of the environment is still brought in")
	}
}

func blockOf(t *testing.T, src, addr string) string {
	t.Helper()
	typ, label, _ := strings.Cut(addr, ".")
	start := strings.Index(src, "resource \""+typ+"\" \""+label+"\" {")
	if start < 0 {
		t.Fatalf("%s is not in the file", addr)
	}
	end := strings.Index(src[start:], "\n}\n")
	return src[start : start+end]
}

func TestExcludeAmbiguityListsCandidates(t *testing.T) {
	req := wholeRequest(func(m map[string]any) { m["exclude"] = []map[string]string{{"type": "flow", "name": "Flow"}} })
	h := newHarness(t, req)
	res := h.run(req)
	if res.Error == nil || res.Error.Code != ErrPreflight || res.Error.Type != "validation" || res.Error.Field != "exclude[0]" || len(res.Error.Candidates) != 4 {
		t.Fatalf("%+v", res.Error)
	}
	for _, c := range res.Error.Candidates {
		if c.ID == "" || c.Name == "" || c.Address == "" {
			t.Fatalf("candidate: %+v", c)
		}
	}
	// No match is a preflight error too.
	req = wholeRequest(func(m map[string]any) {
		m["exclude"] = []map[string]string{{"type": "connection", "name": "nothing like it"}}
	})
	if res = newHarness(t, req).run(req); res.Error == nil || res.Error.Code != ErrPreflight || len(res.Error.Candidates) != 0 {
		t.Fatalf("%+v", res.Error)
	}
	// Exact name wins over a substring, and an ID selects.
	req = wholeRequest(func(m map[string]any) {
		m["exclude"] = []map[string]string{{"type": "flow", "name": "Flow B"}, {"type": "connection", "id": "c0000000000000000000000000000002"}, {"type": "variable", "name": "apiKey"}}
	})
	h = newHarness(t, req)
	if res = h.run(req); res.Error != nil || res.Summary.Excluded != 3 {
		t.Fatalf("%+v %+v", res.Error, res.Summary.Counts)
	}
}

func TestIncludeAndExcludeConflictNamesBoth(t *testing.T) {
	// The request names the same flow two ways, so only the job can tell.
	req := requestJSON(func(m map[string]any) {
		m["dryRun"] = true
		m["flows"] = []map[string]string{{"id": "f0000000000000000000000000000002"}}
		m["exclude"] = []map[string]string{{"type": "flow", "name": "Flow B"}}
	})
	h := newHarness(t, req)
	res := h.run(req)
	if res.Error == nil || res.Error.Code != ErrPreflight || res.Error.Field != "exclude[0]" ||
		!strings.Contains(res.Error.Message, "exclude[0]") || !strings.Contains(res.Error.Message, "flows[0]") {
		t.Fatalf("%+v", res.Error)
	}
	// Excluding the selected item itself through the dependency choices.
	req = requestJSON(func(m map[string]any) {
		m["dryRun"] = true
		m["dependencyChoices"] = map[string]string{flowBAddr: "exclude"}
	})
	res = newHarness(t, req).run(req)
	if res.Error == nil || res.Error.Code != ErrPreflight || res.Error.Field != "dependencyChoices."+flowBAddr || !strings.Contains(res.Error.Message, "selected item") {
		t.Fatalf("%+v", res.Error)
	}
}

func TestExcludingManagedDependencyKeepsGitCopy(t *testing.T) {
	req := requestJSON(func(m map[string]any) {
		m["dryRun"] = true
		m["dependencyChoices"] = map[string]string{pingOneAddr: "exclude"}
	})
	h := newHarness(t, req)
	res := h.run(req)
	if res.Error != nil || res.Status != StatusPreviewed {
		t.Fatalf("%s %+v", res.Status, res.Error)
	}
	var got *ChangeOut
	for i, c := range res.Changes {
		if c.Address == pingOneAddr {
			got = &res.Changes[i]
		}
	}
	if got == nil || got.Action != "excluded" || got.Role != "dependency" || got.Reason != "excluded; Git-managed copy kept" {
		t.Fatalf("%+v", got)
	}
	if strings.Contains(res.Diff, "a/definitions/pingone_davinci_connector_instance.tf") {
		t.Fatalf("the connection must not change:\n%s", res.Diff)
	}
	argv := exportArgv(t, h)
	if !slices.Contains(argv, pingOneAddr) {
		t.Fatalf("the dependency must be passed to terraformer as an exclude: %q", argv)
	}
}

func TestExcludingUnmanagedDependencyNeedsIDsPerEnvironment(t *testing.T) {
	choices := map[string]string{subflowAddr: "exclude"}
	req := requestJSON(func(m map[string]any) { m["dryRun"] = true; m["dependencyChoices"] = choices })
	h := newHarness(t, req)
	res := h.run(req)
	if res.Status != StatusBlocked || res.Error == nil || res.Error.Code != ErrBlocked {
		t.Fatalf("%s %+v", res.Status, res.Error)
	}
	idVar := strings.NewReplacer("-", "_", ".", "_").Replace(subflowAddr) + "_id"
	for _, env := range []string{"qa", "stage", "prod"} {
		bs := res.Blockers[env]
		if len(bs) != 1 || bs[0].Code != "missing_value" || bs[0].Var != idVar || bs[0].Address != subflowAddr || !strings.Contains(bs[0].Message, "Subflow") {
			t.Fatalf("%s blockers: %+v", env, res.Blockers[env])
		}
	}
	if _, ok := res.Blockers["dev"]; ok {
		t.Fatalf("the source environment's value is filled from the export: %+v", res.Blockers["dev"])
	}
	var reason string
	for _, c := range res.Changes {
		if c.Address == subflowAddr {
			reason = c.Reason
			if c.Action != "excluded" || c.Role != "dependency" || c.Var != idVar || c.SourceID != "f0000000000000000000000000000003" {
				t.Fatalf("%+v", c)
			}
		}
	}
	if reason != "excluded; referenced by ID variable `"+idVar+"`" {
		t.Fatalf("reason %q", reason)
	}
	if !strings.Contains(res.Diff, "+"+idVar+" = \"f0000000000000000000000000000003\"") {
		t.Fatalf("the source overlay must carry the ID:\n%s", res.Diff)
	}
	// Supplying the IDs through envValues unblocks the request.
	vals := map[string]any{}
	for env, id := range map[string]string{"qa": "f2000000000000000000000000000003", "stage": "f3000000000000000000000000000003", "prod": "f4000000000000000000000000000003"} {
		vals[env] = map[string]any{idVar: id}
	}
	req = requestJSON(func(m map[string]any) { m["dryRun"] = true; m["dependencyChoices"] = choices; m["envValues"] = vals })
	res = newHarness(t, req).run(req)
	if res.Status != StatusPreviewed || res.Error != nil {
		t.Fatalf("%s %+v %+v", res.Status, res.Error, res.Blockers)
	}
}

func TestDiffTruncationHolds(t *testing.T) {
	diff := strings.Repeat("+line of a diff\n", 1<<16)
	cut, truncated := truncateDiff(diff, MaxDiffAll)
	if !truncated || len(cut) > MaxDiffAll || !strings.HasSuffix(cut, "\n") {
		t.Fatalf("cut %d truncated %v", len(cut), truncated)
	}
	if cut, truncated = truncateDiff("short", MaxDiffAll); truncated || cut != "short" {
		t.Fatal("a short diff is kept")
	}
	if MaxDiffAll >= MaxDiff {
		t.Fatal("a whole-environment diff is cut sooner")
	}
}

func TestLogLineStaysCompact(t *testing.T) {
	r := newResult()
	r.Scope = "all"
	r.Summary.ByType["pingone_davinci_flow"] = TypeCounts{Total: 3}
	l := r.LogLine()
	if strings.Contains(l, "byType") || !strings.Contains(l, `"scope":"all"`) || !strings.Contains(l, `"excluded":0`) {
		t.Fatal(l)
	}
}
