package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"pingone-promote/internal/gitops"
	"pingone-promote/internal/operation"
	"pingone-promote/internal/pingone"
	"pingone-promote/internal/promotion"
)

type fakeGate struct {
	verifyErr, authErr, originalVerifyErr error
	verified, originalVerified            int
	origin                                gitops.Origin
}

type fakeEngine struct {
	artifact      gitops.Artifact
	refreshed     gitops.Artifact
	refreshErr    error
	applyErr      error
	initial       pingone.Promotion
	statuses      []pingone.Promotion
	planCalls     int
	refreshCalls  int
	applyCalls    int
	statusCalls   int
	rollbackCalls int
	statusHook    func()
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
	return e.initial, e.applyErr
}
func (e *fakeEngine) Status(context.Context, gitops.Artifact) (pingone.Promotion, error) {
	e.statusCalls++
	if e.statusHook != nil {
		e.statusHook()
	}
	if len(e.statuses) == 0 {
		return pingone.Promotion{ID: "native-1", Status: "IN_PROGRESS"}, nil
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
			e := &fakeEngine{initial: pingone.Promotion{ID: "native-1", Status: "IN_PROGRESS"}, statuses: []pingone.Promotion{{ID: "native-1", Status: terminal}}}
			dd := deps(g)
			dd.client = func(gitops.Request, func(string) string) (*pingone.Client, error) { return &pingone.Client{}, nil }
			dd.engine = func(*pingone.Client) runnerEngine { return e }
			e.statusHook = func() {
				saved, readErr := readExecutionAttempt(ap + ".execute-intent")
				if readErr != nil || saved.Dispatch != operation.Identified || !saved.NativeObserved || saved.NativeStatus != "IN_PROGRESS" {
					t.Errorf("poll began before matching identity checkpoint: attempt=%+v err=%v", saved, readErr)
				}
			}
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
			if res.DispatchState != operation.Identified || !res.NativeObserved || res.EffectsVerified {
				t.Fatalf("result lifecycle evidence=%+v", res)
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

func TestApplyAuthorizationRefusalReturnsClassifiedPreDispatchFailure(t *testing.T) {
	d := t.TempDir()
	_, a := fixture(t)
	ap, outp := filepath.Join(d, "artifact.json"), filepath.Join(d, "result.json")
	putJSON(t, ap, a)
	g := &fakeGate{authErr: errors.New("private authorization detail token=private-token")}
	e := &fakeEngine{}
	dd := deps(g)
	dd.client = func(gitops.Request, func(string) string) (*pingone.Client, error) { return &pingone.Client{}, nil }
	dd.engine = func(*pingone.Client) runnerEngine { return e }
	var out, errOut strings.Builder
	err := runWith([]string{"apply", "--artifact", ap, "--approved-digest", a.Review.Digest, "--out", outp}, &out, &errOut, dd)
	if err == nil {
		t.Fatal("authorization refusal returned success")
	}
	var failure *operation.Failure
	if !errors.As(err, &failure) || failure.Code != "execution_pre_dispatch_refused" || failure.Phase != "preflight" || failure.RecoveryAction != "verify_review_and_authorization" || failure.CorrelationID == "" {
		t.Fatalf("authorization refusal was not classified: %v", err)
	}
	message := safeError(err, dd.getenv)
	if strings.Contains(message, "private authorization detail") || strings.Contains(message, "private-token") {
		t.Fatalf("CLI failure exposed authorization detail: %s", message)
	}
	if e.applyCalls != 1 {
		t.Fatalf("apply calls=%d want 1", e.applyCalls)
	}
	if _, err := os.Stat(ap + ".execute-intent"); !os.IsNotExist(err) {
		t.Fatalf("pre-dispatch refusal left an execute intent: %v", err)
	}
	if _, err := os.Stat(outp); !os.IsNotExist(err) {
		t.Fatalf("pre-dispatch refusal wrote a result: %v", err)
	}
}

func TestApplyReportsSanitizedPromotionErrorsAndRetainsIntent(t *testing.T) {
	// A terminal response using PingOne's promotionErrors string array must be
	// surfaced safely; ignoring that field loses the only useful failure signal.
	const response = `{"id":"native-1","status":"ERROR","errors":[{"code":"LEGACY_PRIVATE","message":"legacy private detail"}],"promotionErrors":["500 Internal Server Error: \"{\"id\":\"synthetic-id\",\"code\":\"UNEXPECTED_ERROR\",\"message\":\"private tenant message secret=synthetic-secret\"}\"","opaque token=synthetic-token code=PRIVATE_CODE"]}`
	var terminal pingone.Promotion
	if err := json.Unmarshal([]byte(response), &terminal); err != nil {
		t.Fatal(err)
	}
	terminal.ID = "native-1"
	d := t.TempDir()
	_, a := fixture(t)
	ap, outp := filepath.Join(d, "artifact.json"), filepath.Join(d, "result.json")
	putJSON(t, ap, a)
	e := &fakeEngine{initial: pingone.Promotion{ID: "native-1", Status: "IN_PROGRESS"}, statuses: []pingone.Promotion{terminal}}
	dd := deps(&fakeGate{})
	dd.client = func(gitops.Request, func(string) string) (*pingone.Client, error) { return &pingone.Client{}, nil }
	dd.engine = func(*pingone.Client) runnerEngine { return e }
	dd.pollInterval, dd.pollTimeout = time.Millisecond, time.Second
	var out, errOut strings.Builder
	err := runWith([]string{"apply", "--artifact", ap, "--approved-digest", a.Review.Digest, "--out", outp}, &out, &errOut, dd)
	if err == nil || !strings.Contains(err.Error(), "unexpected promotion error") || !strings.Contains(err.Error(), "inspect") {
		t.Fatalf("ERROR did not include safe actionable detail: %v", err)
	}
	b, readErr := os.ReadFile(outp)
	if readErr != nil {
		t.Fatal(readErr)
	}
	var res resultOutput
	if json.Unmarshal(b, &res) != nil || res.Status != "ERROR" || len(res.Errors) < 2 {
		t.Fatalf("sanitized promotion error missing from result: %s", b)
	}
	if !strings.Contains(strings.Join(res.Errors, " "), "promotion error; inspect") {
		t.Fatalf("legacy errors field was not combined safely: %#v", res.Errors)
	}
	for _, secret := range []string{"synthetic-id", "synthetic-secret", "synthetic-token", "private tenant message", "PRIVATE_CODE"} {
		if strings.Contains(string(b), secret) || strings.Contains(err.Error(), secret) {
			t.Fatalf("promotion error leaked %q", secret)
		}
	}
	if e.applyCalls != 1 || e.statusCalls != 1 {
		t.Fatalf("apply=%d status=%d; failed execution must not retry", e.applyCalls, e.statusCalls)
	}
	if _, statErr := os.Stat(ap + ".execute-intent"); statErr != nil {
		t.Fatalf("execute intent was not retained: %v", statErr)
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
	e := &fakeEngine{initial: pingone.Promotion{ID: "native-1", Status: "IN_PROGRESS"}, statuses: []pingone.Promotion{{ID: "native-1", Status: "IN_PROGRESS"}}}
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
	if !res.NativeObserved || !res.NeedsReconciliation || res.EffectsVerified {
		t.Fatalf("poll timeout did not retain observed status and require reconciliation: %+v", res)
	}
	if _, err = os.Stat(ap + ".execute-intent"); err != nil {
		t.Fatal("execute intent was not retained")
	}
}

func TestStatusProjectsFutureStatusAsObservedUnknownWithoutDispatchClaim(t *testing.T) {
	d := t.TempDir()
	_, a := fixture(t)
	ap := filepath.Join(d, "artifact.json")
	putJSON(t, ap, a)
	e := &fakeEngine{statuses: []pingone.Promotion{{ID: "native-1", Status: "FUTURE_PRIVATE_STATUS"}}}
	dd := deps(&fakeGate{})
	dd.client = func(gitops.Request, func(string) string) (*pingone.Client, error) { return &pingone.Client{}, nil }
	dd.engine = func(*pingone.Client) runnerEngine { return e }
	var out, errOut strings.Builder
	if err := runWith([]string{"status", "--artifact", ap}, &out, &errOut, dd); err != nil {
		t.Fatal(err)
	}
	var res resultOutput
	if err := json.Unmarshal([]byte(out.String()), &res); err != nil {
		t.Fatal(err)
	}
	if res.Status != "UNKNOWN" || !res.NativeObserved || res.EffectsVerified || !res.NeedsReconciliation || res.DispatchState != "" || strings.Contains(out.String(), "FUTURE_PRIVATE_STATUS") {
		t.Fatalf("status exposed unsafe or inferred evidence: %+v output=%s", res, out.String())
	}
	if e.statusCalls != 1 || e.applyCalls != 0 {
		t.Fatalf("status calls=%d apply calls=%d", e.statusCalls, e.applyCalls)
	}
}

func TestStatusRejectsMismatchedNativeIdentity(t *testing.T) {
	d := t.TempDir()
	_, a := fixture(t)
	ap := filepath.Join(d, "artifact.json")
	putJSON(t, ap, a)
	e := &fakeEngine{statuses: []pingone.Promotion{{ID: "different-native", Status: "COMPLETED"}}}
	dd := deps(&fakeGate{})
	dd.client = func(gitops.Request, func(string) string) (*pingone.Client, error) { return &pingone.Client{}, nil }
	dd.engine = func(*pingone.Client) runnerEngine { return e }
	var out, errOut strings.Builder
	err := runWith([]string{"status", "--artifact", ap}, &out, &errOut, dd)
	if err == nil || out.Len() != 0 || e.statusCalls != 1 || e.applyCalls != 0 {
		t.Fatalf("mismatched status identity was accepted: err=%v stdout=%s status=%d apply=%d", err, out.String(), e.statusCalls, e.applyCalls)
	}
}

func TestApplyErrorReportsUncertainDispatchInsteadOfInventedRunningStatus(t *testing.T) {
	d := t.TempDir()
	_, a := fixture(t)
	ap, outp := filepath.Join(d, "artifact.json"), filepath.Join(d, "result.json")
	putJSON(t, ap, a)
	e := &fakeEngine{applyErr: errors.New("connection lost after write")}
	dd := deps(&fakeGate{})
	dd.client = func(gitops.Request, func(string) string) (*pingone.Client, error) { return &pingone.Client{}, nil }
	dd.engine = func(*pingone.Client) runnerEngine { return e }
	var out, errOut strings.Builder
	err := runWith([]string{"apply", "--artifact", ap, "--approved-digest", a.Review.Digest, "--out", outp}, &out, &errOut, dd)
	if err == nil || e.applyCalls != 1 || e.statusCalls != 0 {
		t.Fatalf("apply error did not stop without polling: err=%v apply=%d status=%d", err, e.applyCalls, e.statusCalls)
	}
	b, readErr := os.ReadFile(outp)
	if readErr != nil {
		t.Fatal(readErr)
	}
	var res resultOutput
	if json.Unmarshal(b, &res) != nil || res.Status == "IN_PROGRESS" {
		t.Fatalf("apply error invented a native running observation: %+v raw=%s", res, b)
	}
	e2 := &fakeEngine{}
	dd.engine = func(*pingone.Client) runnerEngine { return e2 }
	err = runWith([]string{"apply", "--artifact", ap, "--approved-digest", a.Review.Digest, "--out", filepath.Join(d, "result-restart.json")}, &out, &errOut, dd)
	if err == nil || e2.applyCalls != 0 {
		t.Fatalf("restart replayed uncertain apply: err=%v apply=%d", err, e2.applyCalls)
	}
}

func TestApplyRejectsMismatchedNativeIdentityBeforePolling(t *testing.T) {
	d := t.TempDir()
	_, a := fixture(t)
	ap, outp := filepath.Join(d, "artifact.json"), filepath.Join(d, "result.json")
	putJSON(t, ap, a)
	e := &fakeEngine{initial: pingone.Promotion{ID: "different-native", Status: "IN_PROGRESS"}}
	dd := deps(&fakeGate{})
	dd.client = func(gitops.Request, func(string) string) (*pingone.Client, error) { return &pingone.Client{}, nil }
	dd.engine = func(*pingone.Client) runnerEngine { return e }
	dd.pollInterval, dd.pollTimeout = time.Millisecond, time.Millisecond
	var out, errOut strings.Builder
	err := runWith([]string{"apply", "--artifact", ap, "--approved-digest", a.Review.Digest, "--out", outp}, &out, &errOut, dd)
	if err == nil || e.statusCalls != 0 {
		t.Fatalf("mismatched native identity was treated as observed: err=%v status calls=%d", err, e.statusCalls)
	}
	attempt, readErr := readExecutionAttempt(ap + ".execute-intent")
	if readErr != nil || attempt.Dispatch != operation.Uncertain || attempt.NativeObserved {
		t.Fatalf("mismatched identity was checkpointed as trusted: %+v err=%v", attempt, readErr)
	}
}

func TestApplyFutureStatusKeepsIdentifiedDispatchWithSafeUnknownProjection(t *testing.T) {
	d := t.TempDir()
	_, a := fixture(t)
	ap, outp := filepath.Join(d, "artifact.json"), filepath.Join(d, "result.json")
	putJSON(t, ap, a)
	e := &fakeEngine{initial: pingone.Promotion{ID: "native-1", Status: "FUTURE_PRIVATE_STATUS"}, statuses: []pingone.Promotion{{ID: "native-1", Status: "COMPLETED"}}}
	dd := deps(&fakeGate{})
	dd.client = func(gitops.Request, func(string) string) (*pingone.Client, error) { return &pingone.Client{}, nil }
	dd.engine = func(*pingone.Client) runnerEngine { return e }
	e.statusHook = func() {
		saved, readErr := readExecutionAttempt(ap + ".execute-intent")
		decision := operation.Evaluate(operation.Observation{Status: saved.NativeStatus, Dispatch: saved.Dispatch, EffectsVerified: saved.EffectsVerified})
		if readErr != nil || saved.Dispatch != operation.Identified || !saved.NativeObserved || saved.NativeStatus != "UNKNOWN" || !decision.NeedsReconciliation {
			t.Errorf("future status identity checkpoint=%+v decision=%+v err=%v", saved, decision, readErr)
		}
	}
	var out, errOut strings.Builder
	err := runWith([]string{"apply", "--artifact", ap, "--approved-digest", a.Review.Digest, "--out", outp}, &out, &errOut, dd)
	if err != nil || e.statusCalls != 1 {
		t.Fatalf("future status apply err=%v status calls=%d", err, e.statusCalls)
	}
	b, readErr := os.ReadFile(outp)
	if readErr != nil || strings.Contains(string(b), "FUTURE_PRIVATE_STATUS") {
		t.Fatalf("raw future status leaked to result: read=%v body=%s", readErr, b)
	}
	var res resultOutput
	if err = json.Unmarshal(b, &res); err != nil || !res.NativeObserved || res.EffectsVerified {
		t.Fatalf("future status lifecycle result=%+v err=%v", res, err)
	}
}

func TestApplyIdentityCheckpointFailureRetainsIntentAndSkipsPoll(t *testing.T) {
	d := t.TempDir()
	_, a := fixture(t)
	ap, outp := filepath.Join(d, "artifact.json"), filepath.Join(d, "result.json")
	putJSON(t, ap, a)
	e := &fakeEngine{initial: pingone.Promotion{ID: "native-1", Status: "IN_PROGRESS"}}
	dd := deps(&fakeGate{})
	dd.client = func(gitops.Request, func(string) string) (*pingone.Client, error) { return &pingone.Client{}, nil }
	dd.engine = func(*pingone.Client) runnerEngine { return e }
	dd.executionAttempts = func(path string) executionAttemptStore {
		return failingCheckpointStore{executionAttemptStore: &executionAttemptFileStore{path: path}}
	}
	var out, errOut strings.Builder
	err := runWith([]string{"apply", "--artifact", ap, "--approved-digest", a.Review.Digest, "--out", outp}, &out, &errOut, dd)
	if err == nil || e.applyCalls != 1 || e.statusCalls != 0 {
		t.Fatalf("failed identity checkpoint did not stop before polling: err=%v apply=%d status=%d", err, e.applyCalls, e.statusCalls)
	}
	attempt, readErr := readExecutionAttempt(ap + ".execute-intent")
	if readErr != nil || attempt.Dispatch != operation.Uncertain || attempt.NativeObserved {
		t.Fatalf("failed checkpoint lost or advanced initial intent: %+v err=%v", attempt, readErr)
	}
	if _, readErr = os.Stat(outp); !os.IsNotExist(readErr) {
		t.Fatalf("identity checkpoint failure wrote result: %v", readErr)
	}
}

func TestApplyOutputFailureRetainsIntentAndRestartBlocksReplay(t *testing.T) {
	d := t.TempDir()
	_, a := fixture(t)
	ap, badOut := filepath.Join(d, "artifact.json"), filepath.Join(d, "missing", "result.json")
	putJSON(t, ap, a)
	e := &fakeEngine{initial: pingone.Promotion{ID: "native-1", Status: "COMPLETED"}}
	dd := deps(&fakeGate{})
	dd.client = func(gitops.Request, func(string) string) (*pingone.Client, error) { return &pingone.Client{}, nil }
	dd.engine = func(*pingone.Client) runnerEngine { return e }
	var out, errOut strings.Builder
	err := runWith([]string{"apply", "--artifact", ap, "--approved-digest", a.Review.Digest, "--out", badOut}, &out, &errOut, dd)
	if err == nil || e.applyCalls != 1 {
		t.Fatalf("result output failure was not reported: err=%v apply=%d", err, e.applyCalls)
	}
	if _, readErr := os.Stat(ap + ".execute-intent"); readErr != nil {
		t.Fatalf("output failure lost execute intent: %v", readErr)
	}
	e2 := &fakeEngine{}
	dd.engine = func(*pingone.Client) runnerEngine { return e2 }
	err = runWith([]string{"apply", "--artifact", ap, "--approved-digest", a.Review.Digest, "--out", filepath.Join(d, "restart.json")}, &out, &errOut, dd)
	if err == nil || e2.applyCalls != 0 {
		t.Fatalf("restart replayed after result output failure: err=%v apply=%d", err, e2.applyCalls)
	}
}

func TestMalformedAndLegacyExecuteIntentsBlockBeforeAuthorization(t *testing.T) {
	for name, content := range map[string]string{"malformed": "{", "legacy": "artifact=native-1\napprovedDigest=abc\n"} {
		t.Run(name, func(t *testing.T) {
			d := t.TempDir()
			_, a := fixture(t)
			ap := filepath.Join(d, "artifact.json")
			putJSON(t, ap, a)
			if err := os.WriteFile(ap+".execute-intent", []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			g, e := &fakeGate{}, &fakeEngine{}
			dd := deps(g)
			dd.engine = func(*pingone.Client) runnerEngine { return e }
			var out, errOut strings.Builder
			err := runWith([]string{"apply", "--artifact", ap, "--approved-digest", a.Review.Digest, "--out", filepath.Join(d, "result.json")}, &out, &errOut, dd)
			if err == nil || g.verified != 0 || e.applyCalls != 0 {
				t.Fatalf("existing %s intent did not block before authorization: err=%v verify=%d apply=%d", name, err, g.verified, e.applyCalls)
			}
		})
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
