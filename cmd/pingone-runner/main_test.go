package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"pingone-promote/internal/gitops"
	"pingone-promote/internal/pingone"
	"pingone-promote/internal/promotion"
)

type fakeGate struct {
	verifyErr, authErr, originalVerifyErr, targetErr error
	verified, originalVerified, targetChecked        int
	origin                                           gitops.Origin
}

type fakeEngine struct {
	artifact      gitops.Artifact
	refreshed     gitops.Artifact
	refreshErr    error
	initial       pingone.Promotion
	statuses      []pingone.Promotion
	planCalls     int
	refreshCalls  int
	applyCalls    int
	statusCalls   int
	rollbackCalls int
}

func (e *fakeEngine) Plan(context.Context, gitops.Request, gitops.Origin) (gitops.Artifact, error) {
	e.planCalls++
	return e.artifact, nil
}
func (e *fakeEngine) Refresh(_ context.Context, a gitops.Artifact) (gitops.Artifact, error) {
	e.refreshCalls++
	if e.refreshErr != nil {
		return gitops.Artifact{}, e.refreshErr
	}
	if e.refreshed.NativeID != "" {
		return e.refreshed, nil
	}
	return a, nil
}
func (e *fakeEngine) Apply(ctx context.Context, a gitops.Artifact, digest string, authorize func(context.Context, gitops.Artifact) error) (pingone.Promotion, error) {
	e.applyCalls++
	if err := authorize(ctx, a); err != nil {
		return pingone.Promotion{}, err
	}
	return e.initial, nil
}
func (e *fakeEngine) Status(context.Context, gitops.Artifact) (pingone.Promotion, error) {
	e.statusCalls++
	if len(e.statuses) == 0 {
		return pingone.Promotion{Status: "IN_PROGRESS"}, nil
	}
	p := e.statuses[0]
	e.statuses = e.statuses[1:]
	return p, nil
}
func (e *fakeEngine) RollbackPlan(context.Context, gitops.Artifact, gitops.Origin) (gitops.Artifact, error) {
	e.rollbackCalls++
	return e.artifact, nil
}

func (g *fakeGate) Origin() gitops.Origin { return g.origin }
func (g *fakeGate) VerifyTarget(context.Context, string, string) error {
	g.targetChecked++
	return g.targetErr
}
func (g *fakeGate) VerifyPlan(context.Context, gitops.Request) error {
	g.verified++
	return g.verifyErr
}
func (g *fakeGate) VerifyOriginalPlan(_ context.Context, a gitops.Artifact, jobID string) error {
	g.originalVerified++
	if g.originalVerifyErr != nil {
		return g.originalVerifyErr
	}
	if jobID == "" || a.Origin.ServerURL != g.origin.ServerURL || a.Origin.ProjectID != g.origin.ProjectID {
		return errors.New("source artifact origin is not bound to the trusted project")
	}
	return nil
}
func (g *fakeGate) Authorize(context.Context, gitops.Artifact) error { return g.authErr }

func fixture(t *testing.T) (gitops.Request, gitops.Artifact) {
	t.Helper()
	cfg := map[string]any{"name": "flow"}
	raw, _ := json.Marshal(cfg)
	h := sha256.Sum256(raw)
	r := gitops.Request{Version: 1, ID: strings.Repeat("a", 32), CreatedAt: "2026-01-01T00:00:00Z", Description: "review", Source: gitops.Environment{ID: "source", Region: "us", OrganizationID: "org", SnapshotID: "srcpin"}, Target: gitops.Environment{ID: "target", Region: "us", OrganizationID: "org", SnapshotID: "dstpin"}, Selected: []string{"flow"}, Mappings: map[string]string{}, Configurations: map[string]any{"flow": cfg}, Fingerprints: map[string]string{"flow": hex.EncodeToString(h[:])}}
	a := gitops.Artifact{Version: 1, Kind: "promotion", NativeID: "native-1", CreatedAt: "2026-01-01T00:00:00Z", Request: r, Origin: gitops.Origin{ServerURL: "https://gitlab.example", ProjectID: "10", PipelineID: "20", CommitSHA: strings.Repeat("b", 40)}, TargetSnapshotID: "dstpin"}
	a.Review.Digest = strings.Repeat("c", 64)
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	return r, a
}
func deps(g *fakeGate) runnerDeps {
	return runnerDeps{getenv: func(string) string { return "" }, gate: func() (runnerGate, error) { return g, nil }, client: func(gitops.Request, func(string) string) (*pingone.Client, error) {
		return nil, errors.New("native client unexpectedly constructed")
	}}
}
func putJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
}

func TestVersionPrintsWithoutCIOrCredentials(t *testing.T) {
	var out, errOut strings.Builder
	err := runWith([]string{"--version"}, &out, &errOut, runnerDeps{
		getenv: func(string) string { return "" },
		gate: func() (runnerGate, error) {
			t.Fatal("version command initialized the CI verifier")
			return nil, nil
		},
	})
	if err != nil || out.String() != "pingone-runner dev\n" {
		t.Fatalf("version output=%q err=%v", out.String(), err)
	}
}

func TestVersionRejectsExtraArguments(t *testing.T) {
	var out, errOut strings.Builder
	err := runWith([]string{"--version", "apply"}, &out, &errOut, runnerDeps{})
	if err == nil {
		t.Fatal("version command accepted extra runner arguments")
	}
	if got := out.String(); got != "" {
		t.Fatalf("printed version despite extra arguments: %q", got)
	}
}

func TestRemovedLegacyCheckCommandsStayUnavailable(t *testing.T) {
	for _, command := range [][]string{{"preflight"}, {"plan-check"}, {"evidence", "compare"}} {
		if err := runWith(command, io.Discard, io.Discard, runnerDeps{}); err == nil || !strings.Contains(err.Error(), "unknown command") {
			t.Errorf("legacy command %q was not rejected as unavailable: %v", command[0], err)
		}
	}
}

func TestValidateAndDigestOffline(t *testing.T) {
	d := t.TempDir()
	r, a := fixture(t)
	rp, ap := filepath.Join(d, "requests", r.ID, "request.json"), filepath.Join(d, "artifact.json")
	putJSON(t, rp, r)
	putJSON(t, ap, a)
	var out, errOut strings.Builder
	if err := runWith([]string{"validate", "--request", rp}, &out, &errOut, runnerDeps{}); err != nil || out.String() != "valid\n" {
		t.Fatalf("validate: %q %v", out.String(), err)
	}
	out.Reset()
	if err := runWith([]string{"digest", "--artifact", ap}, &out, &errOut, runnerDeps{}); err != nil || out.String() != a.Review.Digest+"\n" {
		t.Fatalf("digest: %q %v", out.String(), err)
	}
}
func TestStrictArtifactAndFlags(t *testing.T) {
	d := t.TempDir()
	_, a := fixture(t)
	p := filepath.Join(d, "artifact.json")
	putJSON(t, p, a)
	b, _ := os.ReadFile(p)
	bad := strings.TrimSuffix(string(b), "}") + `,"extra":true}`
	_ = os.WriteFile(p, []byte(bad), 0600)
	if _, err := readArtifact(p); err == nil {
		t.Fatal("unknown artifact field accepted")
	}
	for _, args := range [][]string{{"validate"}, {"digest", "--artifact", p, "trailing"}, {"inspect", "--artifact", p}, {"inspect", "--artifact", p, "--out", filepath.Join(d, "diagnostic"), "--approved-digest", strings.Repeat("c", 64)}, {"apply", "--artifact", p, "--out", filepath.Join(d, "result"), "--approved-digest", "bad"}, {"rollback-plan", "--artifact", p, "--out", filepath.Join(d, "rollback")}} {
		var out, errOut strings.Builder
		if err := runWith(args, &out, &errOut, runnerDeps{}); err == nil {
			t.Fatalf("accepted args %v", args)
		}
	}
}
func TestPlanGateRunsBeforeNativeClient(t *testing.T) {
	d := t.TempDir()
	r, _ := fixture(t)
	rp := filepath.Join(d, "requests", r.ID, "request.json")
	putJSON(t, rp, r)
	g := &fakeGate{verifyErr: errors.New("not trusted")}
	var out, errOut strings.Builder
	err := runWith([]string{"plan", "--request", rp, "--out", filepath.Join(d, "artifact.json")}, &out, &errOut, deps(g))
	if err == nil || g.verified != 1 {
		t.Fatalf("gate not enforced: %v, calls=%d", err, g.verified)
	}
}

func TestInspectRefreshesReadOnlyAndWritesRedactedArtifact(t *testing.T) {
	d := t.TempDir()
	_, a := fixture(t)
	a.Review.Steps = []promotion.StepReview{{Action: "POST", After: map[string]any{"clientSecret": "[REDACTED]", "name": "flow"}}}
	ap, outp := filepath.Join(d, "artifact.json"), filepath.Join(d, "inspection.json")
	putJSON(t, ap, a)
	g := &fakeGate{origin: gitops.Origin{ServerURL: a.Origin.ServerURL, ProjectID: a.Origin.ProjectID, PipelineID: "99", CommitSHA: strings.Repeat("d", 40)}}
	e := &fakeEngine{refreshed: a}
	dd := deps(g)
	clientCalls := 0
	dd.client = func(gitops.Request, func(string) string) (*pingone.Client, error) {
		clientCalls++
		return &pingone.Client{}, nil
	}
	dd.engine = func(*pingone.Client) runnerEngine { return e }
	var out, errOut strings.Builder
	if err := runWith([]string{"inspect", "--artifact", ap, "--out", outp}, &out, &errOut, dd); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "inspect did not approve, authorize, or execute") || !strings.Contains(out.String(), "subject to all normal apply gates") || g.verified != 1 || clientCalls != 1 || e.refreshCalls != 1 || e.planCalls != 0 || e.applyCalls != 0 || e.rollbackCalls != 0 {
		t.Fatalf("inspect was not read-only and trust-gated: out=%q verify=%d client=%d refresh=%d plan=%d apply=%d rollback=%d", out.String(), g.verified, clientCalls, e.refreshCalls, e.planCalls, e.applyCalls, e.rollbackCalls)
	}
	refreshed, err := readArtifact(outp)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Origin != a.Origin || refreshed.NativeID != a.NativeID || !reflect.DeepEqual(refreshed.Request, a.Request) || refreshed.TargetSnapshotID != a.TargetSnapshotID {
		t.Fatal("inspection output changed original artifact identities or provenance")
	}
	raw, err := os.ReadFile(outp)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "[REDACTED]") == false || strings.Contains(string(raw), "clientSecret\": \"secret") {
		t.Fatalf("inspection output did not preserve a redacted artifact: %s", raw)
	}
	if _, err := os.Stat(ap + ".execute-intent"); !os.IsNotExist(err) {
		t.Fatalf("inspect created execution intent: %v", err)
	}
}

func TestInspectWritesBlockedSafeDiagnosticWithOriginalOrigin(t *testing.T) {
	d := t.TempDir()
	_, a := fixture(t)
	a.Review.Issues = []string{"selected flow/version create lifecycle is unsupported or mismatched: root-step-source-ref-metadata"}
	ap, outp := filepath.Join(d, "artifact.json"), filepath.Join(d, "diagnostic.json")
	putJSON(t, ap, a)
	g := &fakeGate{origin: gitops.Origin{ServerURL: a.Origin.ServerURL, ProjectID: a.Origin.ProjectID, PipelineID: "99", CommitSHA: strings.Repeat("d", 40)}}
	e := &fakeEngine{refreshed: a}
	dd := deps(g)
	dd.client = func(gitops.Request, func(string) string) (*pingone.Client, error) { return &pingone.Client{}, nil }
	dd.engine = func(*pingone.Client) runnerEngine { return e }
	var out, errOut strings.Builder
	err := runWith([]string{"inspect", "--artifact", ap, "--out", outp}, &out, &errOut, dd)
	if err == nil || !strings.Contains(err.Error(), "inspect did not approve, authorize, or execute") || !strings.Contains(err.Error(), "subject to all normal apply gates") {
		t.Fatalf("blocked diagnostic did not clearly state its boundary: %v", err)
	}
	refreshed, readErr := readArtifact(outp)
	if readErr != nil {
		t.Fatalf("blocked diagnostic was not written: %v", readErr)
	}
	if len(refreshed.Review.Issues) != 1 || refreshed.Review.Issues[0] != a.Review.Issues[0] || refreshed.Origin != a.Origin || g.verified != 1 || e.refreshCalls != 1 || e.planCalls != 0 || e.applyCalls != 0 || e.rollbackCalls != 0 {
		t.Fatalf("blocked diagnostic lost safe reason/origin or used mutation paths: artifact=%+v verify=%d refresh=%d plan=%d apply=%d rollback=%d", refreshed, g.verified, e.refreshCalls, e.planCalls, e.applyCalls, e.rollbackCalls)
	}
}

func TestInspectRejectsForeignArtifactServerOrProjectBeforeRefresh(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*gitops.Artifact)
	}{
		{name: "server", mutate: func(a *gitops.Artifact) { a.Origin.ServerURL = "https://other.example" }},
		{name: "project", mutate: func(a *gitops.Artifact) { a.Origin.ProjectID = "11" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := t.TempDir()
			_, a := fixture(t)
			tc.mutate(&a)
			ap, outp := filepath.Join(d, "artifact.json"), filepath.Join(d, "inspection.json")
			putJSON(t, ap, a)
			g := &fakeGate{origin: gitops.Origin{ServerURL: "https://gitlab.example", ProjectID: "10"}}
			e := &fakeEngine{}
			dd := deps(g)
			clientCalls := 0
			dd.client = func(gitops.Request, func(string) string) (*pingone.Client, error) {
				clientCalls++
				return &pingone.Client{}, nil
			}
			dd.engine = func(*pingone.Client) runnerEngine { return e }
			var out, errOut strings.Builder
			if err := runWith([]string{"inspect", "--artifact", ap, "--out", outp}, &out, &errOut, dd); err == nil {
				t.Fatal("foreign artifact origin accepted")
			}
			if g.verified != 0 || clientCalls != 0 || e.refreshCalls != 0 {
				t.Fatalf("foreign artifact reached verifier/client/refresh: verify=%d client=%d refresh=%d", g.verified, clientCalls, e.refreshCalls)
			}
		})
	}
}

func TestInspectRequiresSuccessfulGateVerificationBeforeRefresh(t *testing.T) {
	d := t.TempDir()
	_, a := fixture(t)
	ap, outp := filepath.Join(d, "artifact.json"), filepath.Join(d, "inspection.json")
	putJSON(t, ap, a)
	g := &fakeGate{origin: gitops.Origin{ServerURL: a.Origin.ServerURL, ProjectID: a.Origin.ProjectID}, verifyErr: errors.New("job is not trusted")}
	e := &fakeEngine{}
	dd := deps(g)
	clientCalls := 0
	dd.client = func(gitops.Request, func(string) string) (*pingone.Client, error) {
		clientCalls++
		return &pingone.Client{}, nil
	}
	dd.engine = func(*pingone.Client) runnerEngine { return e }
	var out, errOut strings.Builder
	if err := runWith([]string{"inspect", "--artifact", ap, "--out", outp}, &out, &errOut, dd); err == nil {
		t.Fatal("inspection continued despite failed GitLab job verification")
	}
	if g.verified != 1 || clientCalls != 0 || e.refreshCalls != 0 {
		t.Fatalf("failed gate reached client/refresh: verify=%d client=%d refresh=%d", g.verified, clientCalls, e.refreshCalls)
	}
}

func TestInspectProtectsInputAndExistingOutput(t *testing.T) {
	d := t.TempDir()
	_, a := fixture(t)
	ap, existing := filepath.Join(d, "artifact.json"), filepath.Join(d, "existing.json")
	putJSON(t, ap, a)
	putJSON(t, existing, map[string]string{"keep": "me"})
	g := &fakeGate{}
	e := &fakeEngine{}
	dd := deps(g)
	dd.client = func(gitops.Request, func(string) string) (*pingone.Client, error) {
		t.Fatal("unexpected client")
		return nil, nil
	}
	dd.engine = func(*pingone.Client) runnerEngine { return e }
	for _, outp := range []string{ap, existing} {
		var out, errOut strings.Builder
		if err := runWith([]string{"inspect", "--artifact", ap, "--out", outp}, &out, &errOut, dd); err == nil {
			t.Fatalf("accepted protected inspect output path %q", outp)
		}
	}
	got, err := readArtifact(ap)
	if err != nil || got.NativeID != a.NativeID || e.refreshCalls != 0 || g.verified != 0 {
		t.Fatalf("input artifact changed or inspection continued: read=%v refresh=%d verify=%d", err, e.refreshCalls, g.verified)
	}
	contents, err := os.ReadFile(existing)
	if err != nil || !strings.Contains(string(contents), "keep") {
		t.Fatalf("existing output was changed: %s (%v)", contents, err)
	}
}

func TestInspectRejectsRefreshIdentityRebinding(t *testing.T) {
	d := t.TempDir()
	_, a := fixture(t)
	ap, outp := filepath.Join(d, "artifact.json"), filepath.Join(d, "inspection.json")
	putJSON(t, ap, a)
	changed := a
	changed.NativeID = "native-2"
	g := &fakeGate{origin: gitops.Origin{ServerURL: a.Origin.ServerURL, ProjectID: a.Origin.ProjectID}}
	e := &fakeEngine{refreshed: changed}
	dd := deps(g)
	dd.client = func(gitops.Request, func(string) string) (*pingone.Client, error) { return &pingone.Client{}, nil }
	dd.engine = func(*pingone.Client) runnerEngine { return e }
	var out, errOut strings.Builder
	if err := runWith([]string{"inspect", "--artifact", ap, "--out", outp}, &out, &errOut, dd); err == nil || !strings.Contains(err.Error(), "identity or provenance") {
		t.Fatalf("refresh identity rebinding accepted: %v", err)
	}
	if e.refreshCalls != 1 {
		t.Fatalf("refresh calls=%d, want one", e.refreshCalls)
	}
	if _, err := os.Stat(outp); !os.IsNotExist(err) {
		t.Fatalf("identity-rebound output was written: %v", err)
	}
}

// A rollback artifact from another project must not reach native rollback draft creation.
func TestRollbackPlanRejectsForeignArtifactOriginBeforeNativeDraft(t *testing.T) {
	d := t.TempDir()
	_, a := fixture(t)
	a.Origin.ProjectID = "11"
	ap, outp := filepath.Join(d, "artifact.json"), filepath.Join(d, "rollback.json")
	putJSON(t, ap, a)
	g := &fakeGate{origin: gitops.Origin{ServerURL: "https://gitlab.example", ProjectID: "10", PipelineID: "30", CommitSHA: strings.Repeat("d", 40)}}
	e := &fakeEngine{}
	dd := deps(g)
	dd.client = func(gitops.Request, func(string) string) (*pingone.Client, error) { return &pingone.Client{}, nil }
	dd.engine = func(*pingone.Client) runnerEngine { return e }
	var out, errOut strings.Builder
	err := runWith([]string{"rollback-plan", "--artifact", ap, "--original-job-id", "41", "--out", outp}, &out, &errOut, dd)
	if err == nil || e.rollbackCalls != 0 || g.originalVerified != 1 {
		t.Fatalf("foreign artifact reached native rollback draft: err=%v rollback calls=%d source verifications=%d", err, e.rollbackCalls, g.originalVerified)
	}
}

func TestPlanWritesBlockedArtifactThenReturnsError(t *testing.T) {
	d := t.TempDir()
	r, a := fixture(t)
	a.Review.Issues = []string{"native promotion is not READY"}
	rp, outp := filepath.Join(d, "requests", r.ID, "request.json"), filepath.Join(d, "artifact.json")
	putJSON(t, rp, r)
	g := &fakeGate{}
	e := &fakeEngine{artifact: a}
	dd := deps(g)
	dd.client = func(gitops.Request, func(string) string) (*pingone.Client, error) { return &pingone.Client{}, nil }
	dd.engine = func(*pingone.Client) runnerEngine { return e }
	var out, errOut strings.Builder
	err := runWith([]string{"plan", "--request", rp, "--out", outp}, &out, &errOut, dd)
	if err == nil {
		t.Fatal("blocked plan returned success")
	}
	if _, err := readArtifact(outp); err != nil {
		t.Fatalf("blocked artifact was not preserved: %v", err)
	}
}

func TestApplyPollsAcceptedExecutionToCompletedOrError(t *testing.T) {
	for _, terminal := range []string{"COMPLETED", "ERROR"} {
		t.Run(terminal, func(t *testing.T) {
			d := t.TempDir()
			_, a := fixture(t)
			ap, outp := filepath.Join(d, "artifact.json"), filepath.Join(d, "result.json")
			putJSON(t, ap, a)
			g := &fakeGate{}
			e := &fakeEngine{initial: pingone.Promotion{Status: "IN_PROGRESS"}, statuses: []pingone.Promotion{{Status: terminal}}}
			dd := deps(g)
			dd.client = func(gitops.Request, func(string) string) (*pingone.Client, error) { return &pingone.Client{}, nil }
			dd.engine = func(*pingone.Client) runnerEngine { return e }
			dd.pollInterval = time.Millisecond
			dd.pollTimeout = time.Second
			var out, errOut strings.Builder
			err := runWith([]string{"apply", "--artifact", ap, "--approved-digest", a.Review.Digest, "--out", outp}, &out, &errOut, dd)
			if terminal == "COMPLETED" && err != nil {
				t.Fatalf("completed apply failed: %v", err)
			}
			if terminal == "ERROR" && err == nil {
				t.Fatal("ERROR execution returned success")
			}
			b, e2 := os.ReadFile(outp)
			if e2 != nil {
				t.Fatal(e2)
			}
			var res resultOutput
			if json.Unmarshal(b, &res) != nil || res.Status != terminal {
				t.Fatalf("result status=%s raw=%s", res.Status, b)
			}
			if e.applyCalls != 1 || e.statusCalls != 1 {
				t.Fatalf("apply=%d status=%d", e.applyCalls, e.statusCalls)
			}
			if _, e2 = os.Stat(ap + ".execute-intent"); e2 != nil {
				t.Fatal("execute intent was not retained")
			}
		})
	}
}

func TestExistingApplyOutputStopsBeforeAuthorizationOrExecute(t *testing.T) {
	d := t.TempDir()
	_, a := fixture(t)
	ap, outp := filepath.Join(d, "artifact.json"), filepath.Join(d, "result.json")
	putJSON(t, ap, a)
	putJSON(t, outp, map[string]string{"existing": "keep"})
	g := &fakeGate{}
	e := &fakeEngine{}
	dd := deps(g)
	dd.client = func(gitops.Request, func(string) string) (*pingone.Client, error) { return &pingone.Client{}, nil }
	dd.engine = func(*pingone.Client) runnerEngine { return e }
	var out, errOut strings.Builder
	err := runWith([]string{"apply", "--artifact", ap, "--approved-digest", a.Review.Digest, "--out", outp}, &out, &errOut, dd)
	if err == nil || g.verified != 0 || e.applyCalls != 0 {
		t.Fatalf("existing result was not rejected early: err=%v verify=%d apply=%d", err, g.verified, e.applyCalls)
	}
}

func TestApplyTimeoutWritesInProgressAndRetainsIntent(t *testing.T) {
	d := t.TempDir()
	_, a := fixture(t)
	ap, outp := filepath.Join(d, "artifact.json"), filepath.Join(d, "result.json")
	putJSON(t, ap, a)
	g := &fakeGate{}
	e := &fakeEngine{initial: pingone.Promotion{Status: "ACCEPTED"}, statuses: []pingone.Promotion{{Status: "IN_PROGRESS"}}}
	dd := deps(g)
	dd.client = func(gitops.Request, func(string) string) (*pingone.Client, error) { return &pingone.Client{}, nil }
	dd.engine = func(*pingone.Client) runnerEngine { return e }
	dd.pollInterval = time.Millisecond
	dd.pollTimeout = 3 * time.Millisecond
	var out, errOut strings.Builder
	err := runWith([]string{"apply", "--artifact", ap, "--approved-digest", a.Review.Digest, "--out", outp}, &out, &errOut, dd)
	if err == nil {
		t.Fatal("timed out execution returned success")
	}
	b, err := os.ReadFile(outp)
	if err != nil {
		t.Fatal(err)
	}
	var res resultOutput
	if json.Unmarshal(b, &res) != nil || res.Status != "IN_PROGRESS" {
		t.Fatalf("result status=%s raw=%s", res.Status, b)
	}
	if _, err = os.Stat(ap + ".execute-intent"); err != nil {
		t.Fatal("execute intent was not retained")
	}
}

func TestMissingCIRejectsPlanAndApplyBeforeNativeClient(t *testing.T) {
	d := t.TempDir()
	r, a := fixture(t)
	rp, ap := filepath.Join(d, "requests", r.ID, "request.json"), filepath.Join(d, "artifact.json")
	putJSON(t, rp, r)
	putJSON(t, ap, a)
	for _, key := range []string{"GITLAB_CI", "CI_SERVER_URL", "CI_PROJECT_ID", "CI_PIPELINE_ID", "CI_JOB_ID", "CI_COMMIT_SHA", "CI_COMMIT_BRANCH", "CI_DEFAULT_BRANCH", "CI_COMMIT_REF_PROTECTED", "CI_JOB_TOKEN", "GITLAB_READ_API_TOKEN", "PROMOTION_GITLAB_URL", "PROMOTION_GITLAB_PROJECT_ID", "PROMOTION_ENVIRONMENT", "PINGONE_TARGET_ENVIRONMENT_ID", "PINGONE_TARGET_REGION", "PINGONE_AUTH_ENVIRONMENT_ID", "PINGONE_CLIENT_ID", "PINGONE_CLIENT_SECRET", "PINGONE_SECRET_MODE"} {
		t.Setenv(key, "")
	}
	for _, args := range [][]string{
		{"plan", "--request", rp, "--out", filepath.Join(d, "planned.json")},
		{"apply", "--artifact", ap, "--approved-digest", a.Review.Digest, "--out", filepath.Join(d, "result.json")},
	} {
		var out, errOut strings.Builder
		if err := run(args, &out, &errOut); err == nil {
			t.Fatalf("accepted operation outside GitLab CI: %v", args)
		}
		if _, err := os.Stat(filepath.Join(d, "planned.json")); err == nil {
			t.Fatal("plan reached native operations outside CI")
		}
	}
}
func TestPrivateAtomicWriteAndIntentReplayBlock(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "out.json")
	if err := writePrivateJSON(p, map[string]string{"ok": "yes"}); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0600 {
		t.Fatalf("mode %o", st.Mode().Perm())
	}
	if err := writePrivateJSON(p, map[string]string{"ok": "no"}); err == nil {
		t.Fatal("overwrote existing output")
	}
	_, a := fixture(t)
	marker := p + ".execute-intent"
	if err := createIntent(marker, a, a.Review.Digest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600); err == nil {
		t.Fatal("replay marker creation succeeded")
	}
	artifactPath := filepath.Join(d, "artifact.json")
	putJSON(t, artifactPath, a)
	if err := os.Rename(marker, artifactPath+".execute-intent"); err != nil {
		t.Fatal(err)
	}
	g := &fakeGate{}
	var out, errOut strings.Builder
	err = runWith([]string{"apply", "--artifact", artifactPath, "--approved-digest", a.Review.Digest, "--out", filepath.Join(d, "result.json")}, &out, &errOut, deps(g))
	if err == nil || g.verified != 0 {
		t.Fatalf("replay was not blocked before CI/native calls: %v", err)
	}
}
func TestPlanWritesMarkdownSummaryWithBlockedArtifact(t *testing.T) {
	d := t.TempDir()
	r, a := fixture(t)
	rp, outp, mdp := filepath.Join(d, "requests", r.ID, "request.json"), filepath.Join(d, "artifact.json"), filepath.Join(d, "summary.md")
	a.Review.Steps = []promotion.StepReview{{Operation: "change", Action: "PUT", URL: "https://api.pingone.com/v1/environments/target/flows/f", Name: "Flow", Before: map[string]any{"clientSecret": "[REDACTED]"}, After: map[string]any{"name": "new"}}}
	a.Review.Issues = []string{"native promotion is not READY"}
	putJSON(t, rp, r)
	dd := deps(&fakeGate{})
	dd.client = func(gitops.Request, func(string) string) (*pingone.Client, error) { return &pingone.Client{}, nil }
	dd.engine = func(*pingone.Client) runnerEngine { return &fakeEngine{artifact: a} }
	var out, errOut strings.Builder
	err := runWith([]string{"plan", "--request", rp, "--out", outp, "--summary-out", mdp}, &out, &errOut, dd)
	if err == nil {
		t.Fatal("blocked plan returned success")
	}
	md, readErr := os.ReadFile(mdp)
	if readErr != nil || !strings.Contains(string(md), "1 change") || strings.Contains(string(md), "secret-value") {
		t.Fatalf("summary=%q read=%v plan=%v stderr=%s", md, readErr, err, errOut.String())
	}
}
