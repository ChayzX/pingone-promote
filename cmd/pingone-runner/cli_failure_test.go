package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pingone-promote/internal/gitops"
	"pingone-promote/internal/operation"
	"pingone-promote/internal/pingone"
)

const privateCLICause = "synthetic-private-cause tenant=tenant-secret token=sentinel-token"

type attemptThenFailEngine struct {
	fakeEngine
	attemptPath string
	attempt     *gitops.DraftAttempt
	cause       error
}

type statusFailureEngine struct {
	fakeEngine
	cause error
}

func (e *statusFailureEngine) Status(context.Context, gitops.Artifact) (pingone.Promotion, error) {
	e.statusCalls++
	return pingone.Promotion{}, e.cause
}

func (e *attemptThenFailEngine) Plan(context.Context, gitops.Request, gitops.Origin) (gitops.Artifact, error) {
	e.planCalls++
	if err := os.WriteFile(e.attemptPath, []byte("attempt"), 0600); err != nil {
		return gitops.Artifact{}, err
	}
	if e.attempt != nil {
		if err := putDraftAttempt(e.attemptPath, *e.attempt); err != nil {
			return gitops.Artifact{}, err
		}
	}
	return gitops.Artifact{}, e.cause
}

func putDraftAttempt(path string, attempt gitops.DraftAttempt) error {
	data, err := json.Marshal(attempt)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

type rollbackAttemptThenFailEngine struct {
	fakeEngine
	attemptPath string
	cause       error
}

type outputRaceEngine struct {
	fakeEngine
	t           *testing.T
	attemptPath string
	outputPath  string
	attempt     gitops.DraftAttempt
}

func (e *outputRaceEngine) Plan(context.Context, gitops.Request, gitops.Origin) (gitops.Artifact, error) {
	e.planCalls++
	putJSON(e.t, e.attemptPath, e.attempt)
	if err := os.WriteFile(e.outputPath, []byte("concurrent output"), 0600); err != nil {
		e.t.Fatal(err)
	}
	return e.artifact, nil
}

func (e *rollbackAttemptThenFailEngine) RollbackPlan(context.Context, gitops.Artifact, gitops.Origin) (gitops.Artifact, error) {
	e.rollbackCalls++
	if err := os.WriteFile(e.attemptPath, []byte("attempt"), 0600); err != nil {
		return gitops.Artifact{}, err
	}
	return gitops.Artifact{}, e.cause
}

func TestCLIPlanFailureContractDistinguishesRefusalAndPossibleDraft(t *testing.T) {
	for _, tc := range []struct {
		name, code, phase, recovery string
		engineFailure               bool
	}{
		{name: "refused before dispatch", code: "draft_pre_dispatch_refused", phase: "preflight", recovery: "verify_request_and_gitlab_authorization"},
		{name: "attempt marker after dispatch stage", code: "draft_outcome_uncertain", phase: "draft", recovery: "reconcile_plan_or_investigate_unknown_identity", engineFailure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := t.TempDir()
			r, _ := fixture(t)
			rp, out := filepath.Join(d, "requests", r.ID, "request.json"), filepath.Join(d, "artifact.json")
			putJSON(t, rp, r)
			g := &fakeGate{verifyErr: errors.New(privateCLICause)}
			dd := deps(g)
			engine := &fakeEngine{}
			if tc.engineFailure {
				g.verifyErr = nil
				engineWithAttempt := &attemptThenFailEngine{attemptPath: out + ".draft-attempt", cause: errors.New(privateCLICause)}
				dd.engine = func(*pingone.Client) runnerEngine { return engineWithAttempt }
			} else {
				dd.engine = func(*pingone.Client) runnerEngine { return engine }
			}
			dd.client = func(gitops.Request, func(string) string) (*pingone.Client, error) { return &pingone.Client{}, nil }
			var stdout, stderr strings.Builder
			err := runWith([]string{"plan", "--request", rp, "--out", out}, &stdout, &stderr, dd)
			if err == nil {
				t.Fatal("plan failure returned success")
			}
			var failure *operation.Failure
			if !errors.As(err, &failure) || failure.Code != tc.code || failure.Phase != tc.phase || failure.RecoveryAction != tc.recovery || failure.CorrelationID == "" {
				t.Fatalf("plan failure contract=%+v err=%v", failure, err)
			}
			message := cliFailure(err, dd.getenv)
			if strings.Contains(message, "synthetic-private-cause") || strings.Contains(message, "tenant-secret") || strings.Contains(message, "sentinel-token") {
				t.Fatalf("CLI leaked dependency cause: %s", message)
			}
			if tc.engineFailure {
				if _, statErr := os.Stat(out + ".draft-attempt"); statErr != nil {
					t.Fatalf("uncertain draft did not retain attempt marker: %v", statErr)
				}
				if engine.planCalls != 0 {
					t.Fatalf("unexpected alternate engine calls=%d", engine.planCalls)
				}
			} else if engine.planCalls != 0 {
				t.Fatalf("pre-dispatch refusal reached draft creation: calls=%d", engine.planCalls)
			}
		})
	}
}

func TestCLIPlanFailureUsesParsedOutputForDashAliasAndLastDuplicate(t *testing.T) {
	for _, tc := range []struct {
		name string
		args func(requestPath, first, last string) []string
	}{
		{name: "single dash alias", args: func(r, _, last string) []string { return []string{"plan", "-request", r, "-out", last} }},
		{name: "last duplicate output flag", args: func(r, first, last string) []string {
			return []string{"plan", "--request", r, "--out", first, "-out", last}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := t.TempDir()
			r, _ := fixture(t)
			rp := filepath.Join(d, "requests", r.ID, "request.json")
			first, last := filepath.Join(d, "first.json"), filepath.Join(d, "last.json")
			putJSON(t, rp, r)
			e := &attemptThenFailEngine{attemptPath: last + ".draft-attempt", cause: errors.New(privateCLICause)}
			dd := deps(&fakeGate{})
			dd.client = func(gitops.Request, func(string) string) (*pingone.Client, error) { return &pingone.Client{}, nil }
			dd.engine = func(*pingone.Client) runnerEngine { return e }
			var stdout, stderr strings.Builder
			err := runWith(tc.args(rp, first, last), &stdout, &stderr, dd)
			var failure *operation.Failure
			if !errors.As(err, &failure) || failure.Code != "draft_outcome_uncertain" || failure.Phase != "draft" || failure.RecoveryAction != "reconcile_plan_or_investigate_unknown_identity" {
				t.Fatalf("parsed output did not classify uncertain draft: failure=%+v err=%v", failure, err)
			}
			if e.planCalls != 1 {
				t.Fatalf("draft calls=%d, want one possible dispatch", e.planCalls)
			}
			if _, statErr := os.Stat(last + ".draft-attempt"); statErr != nil {
				t.Fatalf("parsed output sidecar missing: %v", statErr)
			}
			restarted := &fakeEngine{}
			dd.engine = func(*pingone.Client) runnerEngine { return restarted }
			err = runWith(tc.args(rp, first, last), &stdout, &stderr, dd)
			if err == nil || restarted.planCalls != 0 {
				t.Fatalf("retained parsed-output attempt allowed replay: err=%v plan calls=%d", err, restarted.planCalls)
			}
		})
	}
}

func TestCLIDependencyErrorCannotImpersonateSafeArgumentMessage(t *testing.T) {
	d := t.TempDir()
	r, _ := fixture(t)
	rp, out := filepath.Join(d, "requests", r.ID, "request.json"), filepath.Join(d, "artifact.json")
	putJSON(t, rp, r)
	cause := errors.New("plan requires --request and --out secret=private-vendor-detail")
	g := &fakeGate{verifyErr: cause}
	dd := deps(g)
	var stdout, stderr strings.Builder
	err := runWith([]string{"plan", "--request", rp, "--out", out}, &stdout, &stderr, dd)
	var failure *operation.Failure
	if !errors.As(err, &failure) || failure.Code != "draft_pre_dispatch_refused" || failure.Phase != "preflight" || failure.RecoveryAction != "verify_request_and_gitlab_authorization" || failure.CorrelationID == "" {
		t.Fatalf("dependency error impersonated safe validation text: failure=%+v err=%v", failure, err)
	}
	message := cliFailure(err, dd.getenv)
	if strings.Contains(message, "private-vendor-detail") || strings.Contains(message, "secret=") || strings.Contains(message, "plan requires --request") {
		t.Fatalf("CLI echoed a dependency error: %s", message)
	}
	if g.verified != 1 {
		t.Fatalf("gate verification calls=%d want 1", g.verified)
	}
}

func TestCLIKnownDraftIdentityWithEngineFailureRequiresReconciliation(t *testing.T) {
	d := t.TempDir()
	r, a := fixture(t)
	rp, out := filepath.Join(d, "requests", r.ID, "request.json"), filepath.Join(d, "artifact.json")
	putJSON(t, rp, r)
	attempt := attemptFixture(t)
	attempt.NativeID = a.NativeID
	e := &attemptThenFailEngine{attemptPath: out + ".draft-attempt", attempt: &attempt, cause: errors.New(privateCLICause)}
	dd := deps(&fakeGate{})
	dd.client = func(gitops.Request, func(string) string) (*pingone.Client, error) { return &pingone.Client{}, nil }
	dd.engine = func(*pingone.Client) runnerEngine { return e }
	var stdout, stderr strings.Builder
	err := runWith([]string{"plan", "--request", rp, "--out", out}, &stdout, &stderr, dd)
	var failure *operation.Failure
	if !errors.As(err, &failure) || failure.Code != "draft_reconciliation_required" || failure.Phase != "reconciliation" || failure.RecoveryAction != "reconcile_plan_from_retained_attempt" {
		t.Fatalf("known identity with planning failure was mislabeled: failure=%+v err=%v", failure, err)
	}
	if strings.Contains(cliFailure(err, dd.getenv), "private-cause") || e.planCalls != 1 {
		t.Fatalf("planning failure leaked cause or repeated draft: error=%s plan calls=%d", cliFailure(err, dd.getenv), e.planCalls)
	}
	if _, statErr := os.Stat(out + ".draft-attempt"); statErr != nil {
		t.Fatalf("checkpointed identity was not retained: %v", statErr)
	}
}

func TestCLIUnhandledFailureFallbackIsStableAndSanitized(t *testing.T) {
	cause := errors.New("secret=private-vendor-detail arbitrary cause")
	message := cliFailure(cause, func(string) string { return "" })
	for _, required := range []string{"code=cli_failure_unclassified", "phase=operation", "recovery=review_command_and_correlation", "correlation="} {
		if !strings.Contains(message, required) {
			t.Fatalf("untyped fallback omitted %q: %s", required, message)
		}
	}
	if strings.Contains(message, "private-vendor-detail") || strings.Contains(message, "arbitrary cause") {
		t.Fatalf("untyped cliFailure exposed cause: %s", message)
	}
	var stdout, stderr strings.Builder
	err := runWith([]string{"not-a-command", "secret=arbitrary-input"}, &stdout, &stderr, runnerDeps{})
	var failure *operation.Failure
	if !errors.As(err, &failure) || failure.Code != "cli_command_invalid" || failure.Phase != "validation" || failure.RecoveryAction != "check_runner_usage" || strings.Contains(cliFailure(err, nil), "arbitrary-input") {
		t.Fatalf("unknown command was not safely classified: failure=%+v output=%s", failure, cliFailure(err, nil))
	}
}

func TestCLITrustedArgumentMessagesRemainAvailable(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		args       []string
	}{
		{name: "validate", args: []string{"validate"}, want: "validate requires --request"},
		{name: "digest", args: []string{"digest"}, want: "digest requires --artifact"},
		{name: "apply", args: []string{"apply"}, want: "apply requires --artifact and --out"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr strings.Builder
			err := runWith(tc.args, &stdout, &stderr, runnerDeps{})
			var failure *operation.Failure
			if !errors.As(err, &failure) || failure.Code != "cli_arguments_invalid" || failure.Phase != "validation" || failure.Message != tc.want || failure.RecoveryAction != "correct_command_arguments" {
				t.Fatalf("trusted argument message was not retained: failure=%+v err=%v", failure, err)
			}
		})
	}
}

func TestCLIReadOnlyStatusFailureHasFixedRecoveryAndNoMutation(t *testing.T) {
	d := t.TempDir()
	_, a := fixture(t)
	ap := filepath.Join(d, "artifact.json")
	putJSON(t, ap, a)
	e := &statusFailureEngine{cause: errors.New(privateCLICause)}
	dd := deps(&fakeGate{})
	dd.client = func(gitops.Request, func(string) string) (*pingone.Client, error) { return &pingone.Client{}, nil }
	dd.engine = func(*pingone.Client) runnerEngine { return e }
	var stdout, stderr strings.Builder
	err := runWith([]string{"status", "--artifact", ap}, &stdout, &stderr, dd)
	if err == nil {
		t.Fatal("status failure returned success")
	}
	var failure *operation.Failure
	if !errors.As(err, &failure) || failure.Code != "read_only_status_failed" || failure.Phase != "reconciliation" || failure.RecoveryAction != "retry_read_only_status_or_investigate" || failure.CorrelationID == "" {
		t.Fatalf("status failure contract=%+v err=%v", failure, err)
	}
	message := cliFailure(err, dd.getenv)
	if strings.Contains(message, "synthetic-private-cause") || strings.Contains(message, "tenant-secret") || strings.Contains(message, "sentinel-token") {
		t.Fatalf("CLI leaked dependency cause: %s", message)
	}
	if stdout.Len() != 0 || e.statusCalls != 1 || e.applyCalls != 0 || e.planCalls != 0 || e.rollbackCalls != 0 {
		t.Fatalf("status crossed read-only boundary: stdout=%q status=%d apply=%d plan=%d rollback=%d", stdout.String(), e.statusCalls, e.applyCalls, e.planCalls, e.rollbackCalls)
	}
}

func TestCLIOperationalFailureCategoriesStaySanitizedAndBounded(t *testing.T) {
	t.Run("accepted identity survives artifact output failure", func(t *testing.T) {
		d := t.TempDir()
		r, a := fixture(t)
		rp, out := filepath.Join(d, "requests", r.ID, "request.json"), filepath.Join(d, "artifact.json")
		putJSON(t, rp, r)
		attempt := attemptFixture(t)
		attempt.NativeID = a.NativeID
		e := &outputRaceEngine{t: t, attemptPath: out + ".draft-attempt", outputPath: out, attempt: attempt, fakeEngine: fakeEngine{artifact: a}}
		dd := deps(&fakeGate{})
		dd.client = func(gitops.Request, func(string) string) (*pingone.Client, error) { return &pingone.Client{}, nil }
		dd.engine = func(*pingone.Client) runnerEngine { return e }
		var stdout, stderr strings.Builder
		err := runWith([]string{"plan", "--request", rp, "--out", out}, &stdout, &stderr, dd)
		var failure *operation.Failure
		if !errors.As(err, &failure) || failure.Code != "draft_output_persistence_failed" || failure.Phase != "persistence" || failure.RecoveryAction != "reconcile_plan_from_retained_attempt" {
			t.Fatalf("output failure contract=%+v err=%v", failure, err)
		}
		retained, readErr := readDraftAttempt(out + ".draft-attempt")
		contents, outputErr := os.ReadFile(out)
		if readErr != nil || retained.NativeID != a.NativeID || outputErr != nil || string(contents) != "concurrent output" || e.planCalls != 1 {
			t.Fatalf("output failure lost evidence or overwrote output: attempt=%+v read=%v output=%q err=%v plan=%d", retained, readErr, contents, outputErr, e.planCalls)
		}
	})

	t.Run("rollback draft may have dispatched", func(t *testing.T) {
		d := t.TempDir()
		_, a := fixture(t)
		ap, out := filepath.Join(d, "artifact.json"), filepath.Join(d, "rollback.json")
		putJSON(t, ap, a)
		g := &fakeGate{origin: a.Origin}
		e := &rollbackAttemptThenFailEngine{attemptPath: out + ".draft-attempt", cause: errors.New(privateCLICause)}
		dd := deps(g)
		dd.client = func(gitops.Request, func(string) string) (*pingone.Client, error) { return &pingone.Client{}, nil }
		dd.engine = func(*pingone.Client) runnerEngine { return e }
		var stdout, stderr strings.Builder
		err := runWith([]string{"rollback-plan", "--artifact", ap, "--original-job-id", "41", "--out", out}, &stdout, &stderr, dd)
		var failure *operation.Failure
		if !errors.As(err, &failure) || failure.Code != "draft_outcome_uncertain" || failure.Phase != "draft" || failure.RecoveryAction != "reconcile_plan_or_investigate_unknown_identity" {
			t.Fatalf("rollback failure contract=%+v err=%v", failure, err)
		}
		if strings.Contains(cliFailure(err, dd.getenv), "synthetic-private-cause") || e.rollbackCalls != 1 || g.verified != 1 || g.originalVerified != 1 {
			t.Fatalf("rollback failure leaked or crossed boundaries: err=%s rollback=%d verify=%d original=%d", cliFailure(err, dd.getenv), e.rollbackCalls, g.verified, g.originalVerified)
		}
		if _, statErr := os.Stat(out + ".draft-attempt"); statErr != nil {
			t.Fatalf("rollback attempt marker lost: %v", statErr)
		}
	})

	t.Run("inspect remains read-only", func(t *testing.T) {
		d := t.TempDir()
		_, a := fixture(t)
		ap, out := filepath.Join(d, "artifact.json"), filepath.Join(d, "inspection.json")
		putJSON(t, ap, a)
		g := &fakeGate{origin: a.Origin}
		e := &fakeEngine{refreshErr: errors.New(privateCLICause)}
		dd := deps(g)
		clientCalls := 0
		dd.client = func(gitops.Request, func(string) string) (*pingone.Client, error) {
			clientCalls++
			return &pingone.Client{}, nil
		}
		dd.engine = func(*pingone.Client) runnerEngine { return e }
		var stdout, stderr strings.Builder
		err := runWith([]string{"inspect", "--artifact", ap, "--out", out}, &stdout, &stderr, dd)
		var failure *operation.Failure
		if !errors.As(err, &failure) || failure.Code != "read_only_inspection_failed" || failure.Phase != "reconciliation" || failure.RecoveryAction != "retry_read_only_inspection_or_investigate" {
			t.Fatalf("inspect failure contract=%+v err=%v", failure, err)
		}
		if strings.Contains(cliFailure(err, dd.getenv), "synthetic-private-cause") || e.refreshCalls != 1 || e.planCalls != 0 || e.applyCalls != 0 || e.rollbackCalls != 0 || clientCalls != 1 || g.verified != 1 || stdout.Len() != 0 {
			t.Fatalf("inspection leaked or crossed boundaries: err=%s refresh=%d plan=%d apply=%d rollback=%d client=%d verify=%d stdout=%q", cliFailure(err, dd.getenv), e.refreshCalls, e.planCalls, e.applyCalls, e.rollbackCalls, clientCalls, g.verified, stdout.String())
		}
	})

	t.Run("reconcile remains read-only", func(t *testing.T) {
		d := t.TempDir()
		r, a := fixture(t)
		ap, rp, out := filepath.Join(d, "attempt.json"), filepath.Join(d, "requests", r.ID, "request.json"), filepath.Join(d, "reconciled.json")
		attempt := attemptFixture(t)
		attempt.NativeID = a.NativeID
		requestBytes, _ := json.Marshal(r)
		requestDigest := sha256.Sum256(requestBytes)
		attempt.RequestDigest = hex.EncodeToString(requestDigest[:])
		putJSON(t, ap, attempt)
		putJSON(t, rp, r)
		g := &fakeGate{origin: a.Origin, verifyErr: errors.New(privateCLICause)}
		dd := deps(g)
		clientCalls := 0
		dd.client = func(gitops.Request, func(string) string) (*pingone.Client, error) {
			clientCalls++
			return &pingone.Client{}, nil
		}
		var stdout, stderr strings.Builder
		err := runWith([]string{"reconcile-plan", "--attempt", ap, "--attempt-job-id", "40", "--request", rp, "--out", out}, &stdout, &stderr, dd)
		var failure *operation.Failure
		if !errors.As(err, &failure) || failure.Code != "read_only_reconciliation_failed" || failure.Phase != "reconciliation" || failure.RecoveryAction != "retry_reconcile_plan_or_investigate" {
			t.Fatalf("reconcile failure contract=%+v err=%v", failure, err)
		}
		if strings.Contains(cliFailure(err, dd.getenv), "synthetic-private-cause") || g.verified != 1 || clientCalls != 0 || stdout.Len() != 0 || g.originalVerified != 0 {
			t.Fatalf("reconciliation leaked or crossed boundaries: err=%s verify=%d client=%d stdout=%q original=%d", cliFailure(err, dd.getenv), g.verified, clientCalls, stdout.String(), g.originalVerified)
		}
		if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
			t.Fatalf("reconciliation wrote output on failed gate: %v", statErr)
		}
	})
}
