package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"pingone-promote/internal/gitops"
	"pingone-promote/internal/strictjson"
	"reflect"
)

const maxDraftAttemptBytes = 16 << 10

type draftAttemptStore struct{ path string }

func encodeDraftAttempt(a gitops.DraftAttempt) ([]byte, error) {
	if err := a.Validate(); err != nil {
		return nil, err
	}
	b, err := json.Marshal(a)
	if err != nil || len(b) > maxDraftAttemptBytes {
		return nil, errors.New("invalid or oversized draft attempt")
	}
	return append(b, '\n'), nil
}
func (s *draftAttemptStore) Begin(ctx context.Context, a gitops.DraftAttempt) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.NativeID != "" {
		return errors.New("draft intent already has native identity")
	}
	b, err := encodeDraftAttempt(a)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	// Retain even an incomplete record: its existence prevents dispatch replay.
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	return err
}
func (s *draftAttemptStore) Checkpoint(ctx context.Context, a gitops.DraftAttempt) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.NativeID == "" {
		return errors.New("checkpoint requires native identity")
	}
	b, err := encodeDraftAttempt(a)
	if err != nil {
		return err
	}
	original, err := readDraftAttempt(s.path)
	if err != nil {
		return err
	}
	binding := a
	binding.NativeID = ""
	initial := original
	initial.NativeID = ""
	if binding != initial || (original.NativeID != "" && original.NativeID != a.NativeID) {
		return errors.New("draft checkpoint binding changed")
	}
	before, err := os.Lstat(s.path)
	if err != nil || !before.Mode().IsRegular() {
		return errors.New("draft intent is missing or not regular")
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".draft-attempt-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(b)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	after, err := os.Lstat(s.path)
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return errors.New("draft intent changed during checkpoint")
	}
	return os.Rename(tmp.Name(), s.path)
}
func readDraftAttempt(path string) (gitops.DraftAttempt, error) {
	var a gitops.DraftAttempt
	info, err := os.Lstat(path)
	if err != nil {
		return a, err
	}
	if !info.Mode().IsRegular() {
		return a, errors.New("draft attempt is not a regular file")
	}
	b, err := readSmallFile(path, maxDraftAttemptBytes)
	if err != nil {
		return a, err
	}
	if err = strictjson.Unmarshal(b, &a); err != nil {
		return a, errors.New("invalid draft attempt JSON")
	}
	return a, a.Validate()
}
func precheckDraftAttempt(out string, paths ...string) error {
	sidecar := out + ".draft-attempt"
	for _, p := range paths {
		if p != "" && pathsAlias(sidecar, p) {
			return safeRunnerValidation("draft attempt sidecar must not alias inputs or outputs")
		}
	}
	if err := requireAbsent(sidecar); err != nil {
		return fmt.Errorf("draft attempt exists or cannot be inspected; reconcile before retry: %w", err)
	}
	return nil
}
func pathsAlias(a, b string) bool {
	if samePath(a, b) {
		return true
	}
	ai, ae := os.Stat(a)
	bi, be := os.Stat(b)
	if ae == nil && be == nil && os.SameFile(ai, bi) {
		return true
	}
	// Resolve existing parent symlinks even when the final output does not exist.
	ap, ae := filepath.EvalSymlinks(filepath.Dir(a))
	bp, be := filepath.EvalSymlinks(filepath.Dir(b))
	return ae == nil && be == nil && samePath(filepath.Join(ap, filepath.Base(a)), filepath.Join(bp, filepath.Base(b)))
}
func bindDraftAttempts(engine runnerEngine, out string) runnerEngine {
	if e, ok := engine.(*gitops.Engine); ok {
		e.DraftAttempts = &draftAttemptStore{path: out + ".draft-attempt"}
	}
	return engine
}

type draftAttemptVerifier interface {
	VerifyDraftAttempt(context.Context, gitops.DraftAttempt, string) error
}
type draftReconciler interface {
	ReconcileDraft(context.Context, gitops.DraftAttempt, gitops.Request, gitops.Origin) (gitops.Artifact, error)
}

func reconcilePlan(ctx context.Context, args []string, stdout, stderr io.Writer, deps runnerDeps) error {
	fs := flagSet("reconcile-plan", stderr)
	attemptPath := fs.String("attempt", "", "retained attempt JSON")
	jobID := fs.String("attempt-job-id", "", "originating planning job")
	requestPath := fs.String("request", "", "request JSON")
	out := fs.String("out", "", "new diagnostic artifact")
	originalPath := fs.String("original-artifact", "", "rollback original artifact")
	originalJob := fs.String("original-job-id", "", "rollback original plan job")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *attemptPath == "" || *jobID == "" || *requestPath == "" || *out == "" {
		return safeRunnerValidation("reconcile-plan requires --attempt, --attempt-job-id, --request and --out")
	}
	for _, p := range []string{*attemptPath, *requestPath, *originalPath} {
		if p != "" && pathsAlias(*out, p) {
			return safeRunnerValidation("diagnostic output must not alias inputs")
		}
	}
	if err := requireAbsent(*out); err != nil {
		return err
	}
	a, err := readDraftAttempt(*attemptPath)
	if err != nil {
		return err
	}
	if a.NativeID == "" {
		return safeRunnerDiagnostic("draft identity unavailable; blocked investigation required; do not guess candidates or retry draft creation")
	}
	r, err := readRequest(*requestPath)
	if err != nil {
		return err
	}
	if err = a.ValidateRequest(r); err != nil {
		return err
	}
	var original gitops.Artifact
	if a.Kind == "rollback" {
		if *originalPath == "" || *originalJob == "" {
			return safeRunnerValidation("rollback reconciliation requires --original-artifact and --original-job-id")
		}
		original, err = readArtifact(*originalPath)
		if err != nil {
			return err
		}
		if original.Kind != "promotion" || original.NativeID != a.OriginalNativeID || !reflect.DeepEqual(original.Request, r) || !sameTrustedProject(original.Origin, a.Origin) || original.TargetSnapshotID != a.TargetSnapshotID {
			return errors.New("rollback original artifact linkage mismatch")
		}
	} else if *originalPath != "" || *originalJob != "" {
		return safeRunnerValidation("forward reconciliation rejects rollback-only flags")
	}
	g, err := deps.gate()
	if err != nil {
		return err
	}
	if !sameTrustedProject(a.Origin, g.Origin()) {
		return errors.New("draft attempt trusted project mismatch")
	}
	if err = g.VerifyPlan(ctx, r); err != nil {
		return err
	}
	verifier, ok := g.(draftAttemptVerifier)
	if !ok {
		return errors.New("draft provenance verification unavailable; reconciliation blocked")
	}
	if err = verifier.VerifyDraftAttempt(ctx, a, *jobID); err != nil {
		return err
	}
	if a.Kind == "rollback" {
		if err = g.VerifyOriginalPlan(ctx, original, *originalJob); err != nil {
			return err
		}
	}
	client, err := deps.client(r, deps.getenv)
	if err != nil {
		return err
	}
	engine, ok := deps.engine(client).(draftReconciler)
	if !ok {
		return errors.New("read-only draft reconciliation unavailable")
	}
	fresh, err := engine.ReconcileDraft(ctx, a, r, g.Origin())
	if err != nil {
		return err
	}
	if fresh.Origin != a.Origin || fresh.CreatedAt != a.CreatedAt || fresh.NativeID != a.NativeID || fresh.Kind != a.Kind || fresh.OriginalNativeID != a.OriginalNativeID || !reflect.DeepEqual(fresh.Request, r) {
		return errors.New("reconciliation changed identity or provenance")
	}
	if err = fresh.Validate(); err != nil {
		return err
	}
	if err = writePrivateJSON(*out, fresh); err != nil {
		return err
	}
	if len(fresh.Review.Issues) > 0 {
		return errors.New("diagnostic artifact written with review issues; reconciliation did not approve, authorize, or execute; output remains subject to all normal apply gates")
	}
	_, err = io.WriteString(stdout, "diagnostic reconciliation complete; reconciliation did not approve, authorize, or execute; output remains subject to all normal apply gates\n")
	return err
}
