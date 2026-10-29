package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pingone-promote/internal/checks"
	"pingone-promote/internal/pingone"
	"pingone-promote/internal/promotion"
)

func checkDeps(env map[string]string, gitlab checks.Report) runnerDeps {
	return runnerDeps{
		getenv: func(k string) string { return env[k] },
		environ: func() []string {
			out := []string{}
			for k, v := range env {
				out = append(out, k+"="+v)
			}
			return out
		},
		gitlabPreflight: func(context.Context) checks.Report { return gitlab },
		targetClient: func(func(string) string) (*pingone.Client, error) {
			return nil, errors.New("no PingOne in unit tests")
		},
	}
}

func fullEnv() map[string]string {
	env := map[string]string{}
	for _, n := range checks.RequiredVariables {
		env[n] = "value-" + strings.ToLower(n)
	}
	return env
}

func passingGitLab() checks.Report {
	r := checks.Report{Title: "GitLab preflight"}
	r.Pass("project", "ok")
	return r
}

func TestPreflightCommandReportsAndFailsClosed(t *testing.T) {
	d := t.TempDir()
	var out, errOut strings.Builder
	report := filepath.Join(d, "preflight.md")
	err := runWith([]string{"preflight", "--skip-pingone", "--report", report}, &out, &errOut, checkDeps(fullEnv(), passingGitLab()))
	if err != nil {
		t.Fatalf("clean preflight failed: %v\n%s", err, out.String())
	}
	if b, _ := os.ReadFile(report); !strings.Contains(string(b), "| PASS | project |") {
		t.Fatalf("report not written: %s", b)
	}
	if err = runWith([]string{"preflight", "--skip-pingone", "--report", report}, &out, &errOut, checkDeps(fullEnv(), passingGitLab())); err == nil {
		t.Fatal("existing report overwritten")
	}

	// PingOne client unavailable must fail, not silently skip.
	out.Reset()
	if err = runWith([]string{"preflight"}, &out, &errOut, checkDeps(fullEnv(), passingGitLab())); err == nil || !strings.Contains(out.String(), "[FAIL] PingOne client") {
		t.Fatalf("missing PingOne client accepted: %v\n%s", err, out.String())
	}

	// Truncated secret name from the trial run.
	env := fullEnv()
	delete(env, "PINGONE_CLIENT_SECRET")
	env["PINGONE_CLIENT_SECRE"] = "x"
	out.Reset()
	if err = runWith([]string{"preflight", "--skip-pingone"}, &out, &errOut, checkDeps(env, passingGitLab())); err == nil || !strings.Contains(out.String(), "misspelling of PINGONE_CLIENT_SECRET") {
		t.Fatalf("misspelled variable not reported: %v\n%s", err, out.String())
	}
	for _, v := range env {
		if strings.Contains(out.String(), v) && len(v) > 6 {
			t.Fatalf("variable value printed: %s", out.String())
		}
	}

	// Default deps without a GitLab context must fail closed.
	out.Reset()
	if err = runWith([]string{"preflight", "--skip-pingone"}, &out, &errOut, runnerDeps{environ: checkDeps(fullEnv(), passingGitLab()).environ}); err == nil {
		t.Fatal("preflight passed without a CI verifier")
	}
}

func planFixture(t *testing.T, stepName string) (string, string) {
	t.Helper()
	_, a := fixture(t)
	a.Review.Issues = []string{}
	a.Review.Steps = []promotion.StepReview{{Action: "PUT", URL: "https://api.pingone.com/v1/environments/target/flows/flow-target", SourceID: "flow", TargetID: "flow-target", Name: stepName}}
	d := t.TempDir()
	ap, dp := filepath.Join(d, "plan.json"), filepath.Join(d, "plan.sha256")
	putJSON(t, ap, a)
	_ = os.WriteFile(dp, []byte(a.Review.Digest+"\n"), 0600)
	return ap, dp
}

func TestPlanCheckCommand(t *testing.T) {
	ap, dp := planFixture(t, "Login")
	summary := filepath.Join(filepath.Dir(ap), "plan-review.md")
	env := map[string]string{"PINGONE_CLIENT_SECRET": "worker-secret-value"}
	var out, errOut strings.Builder
	err := runWith([]string{"plan-check", "--artifact", ap, "--digest-file", dp, "--expect-selected", "flow", "--forbid-type", "templates", "--summary-out", summary}, &out, &errOut, checkDeps(env, passingGitLab()))
	if err != nil {
		t.Fatalf("matching plan failed: %v\n%s", err, out.String())
	}
	if b, _ := os.ReadFile(summary); !strings.Contains(string(b), "Promotion review: native-1") || !strings.Contains(string(b), "flows/flow-target") {
		t.Fatalf("summary not written: %s", b)
	}

	for name, args := range map[string][]string{
		"wrong selection": {"--expect-selected", "flow,template"},
		"forbidden type":  {"--forbid-type", "flows"},
	} {
		out.Reset()
		if err = runWith(append([]string{"plan-check", "--artifact", ap}, args...), &out, &errOut, checkDeps(env, passingGitLab())); err == nil {
			t.Fatalf("%s accepted:\n%s", name, out.String())
		}
	}

	// A known secret inside the artifact must fail the check.
	leaky, _ := planFixture(t, "worker-secret-value")
	out.Reset()
	if err = runWith([]string{"plan-check", "--artifact", leaky}, &out, &errOut, checkDeps(env, passingGitLab())); err == nil || !strings.Contains(out.String(), "[FAIL] secrets") {
		t.Fatalf("leaked secret accepted: %v\n%s", err, out.String())
	}
	if err = runWith([]string{"plan-check"}, &out, &errOut, checkDeps(env, passingGitLab())); err == nil {
		t.Fatal("missing --artifact accepted")
	}
}

func TestEvidenceCompareCommand(t *testing.T) {
	d := t.TempDir()
	key := []byte("evidence-key-0123456789")
	inv := func(v int) pingone.Inventory {
		return pingone.Inventory{EnvironmentID: "target", OrganizationID: "org", SnapshotID: "s", Resources: []pingone.Resource{
			{ID: "flow", Type: "flows", Name: "Login", Configuration: map[string]any{"v": v}},
			{ID: "tpl", Type: "templates", Name: "Email", Configuration: map[string]any{"v": 1}},
		}}
	}
	write := func(name string, i pingone.Inventory) string {
		e, err := checks.NewEvidence(i, key, time.Unix(0, 0))
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(d, name)
		b, _ := json.Marshal(e)
		_ = os.WriteFile(p, b, 0600)
		return p
	}
	before, after := write("before.json", inv(1)), write("after.json", inv(2))
	var out, errOut strings.Builder
	report := filepath.Join(d, "compare.md")
	if err := runWith([]string{"evidence", "compare", "--before", before, "--after", after, "--expect-changed", "flow", "--report", report}, &out, &errOut, runnerDeps{}); err != nil {
		t.Fatalf("intended change rejected: %v\n%s", err, out.String())
	}
	if _, err := os.Stat(report); err != nil {
		t.Fatal("compare report not written")
	}
	out.Reset()
	if err := runWith([]string{"evidence", "compare", "--before", before, "--after", after}, &out, &errOut, runnerDeps{}); err == nil || !strings.Contains(out.String(), "[FAIL] unexpected change") {
		t.Fatalf("unexpected change accepted: %v\n%s", err, out.String())
	}
}

func TestEvidenceCaptureRequiresKeyAndFreshOutput(t *testing.T) {
	d := t.TempDir()
	var out, errOut strings.Builder
	err := runWith([]string{"evidence", "capture", "--env", "target", "--out", filepath.Join(d, "e.json")}, &out, &errOut, checkDeps(map[string]string{"PINGONE_CLIENT_SECRET": "short"}, passingGitLab()))
	if err == nil || !strings.Contains(err.Error(), "EVIDENCE_HMAC_KEY") {
		t.Fatalf("weak key accepted: %v", err)
	}
	existing := filepath.Join(d, "exists.json")
	_ = os.WriteFile(existing, []byte("{}"), 0600)
	if err = runWith([]string{"evidence", "capture", "--env", "target", "--out", existing}, &out, &errOut, checkDeps(map[string]string{"EVIDENCE_HMAC_KEY": "evidence-key-0123456789"}, passingGitLab())); err == nil {
		t.Fatal("existing evidence overwritten")
	}
	for _, args := range [][]string{{"evidence"}, {"evidence", "bogus"}, {"evidence", "capture"}, {"evidence", "compare"}} {
		if err = runWith(args, &out, &errOut, runnerDeps{}); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
