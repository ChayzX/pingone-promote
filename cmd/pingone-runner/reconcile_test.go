package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"pingone-promote/internal/gitops"
	"pingone-promote/internal/pingone"
	"testing"
)

type recoveryGate struct {
	*fakeGate
	denied bool
}

func (g recoveryGate) VerifyDraftAttempt(context.Context, gitops.DraftAttempt, string) error {
	if g.denied {
		return errors.New("retained evidence missing")
	}
	return nil
}

type recoveryEngine struct{ *fakeEngine }

func (e recoveryEngine) ReconcileDraft(_ context.Context, a gitops.DraftAttempt, r gitops.Request, _ gitops.Origin) (gitops.Artifact, error) {
	out := e.artifact
	out.Origin = a.Origin
	out.CreatedAt = a.CreatedAt
	return out, nil
}
func TestReconcileRunnerEvidenceAndOutput(t *testing.T) {
	for _, mode := range []string{"valid", "denied", "missing-capability", "changed-request", "alias", "exists", "empty", "rollback-no-original", "rollback"} {
		t.Run(mode, func(t *testing.T) {
			r, artifact := fixture(t)
			raw, _ := json.Marshal(r)
			sum := sha256.Sum256(raw)
			a := gitops.DraftAttempt{Version: 1, Kind: "promotion", RequestID: r.ID, RequestDigest: hex.EncodeToString(sum[:]), Origin: artifact.Origin, NativeID: artifact.NativeID, Source: r.Source, Target: r.Target, TargetSnapshotID: artifact.TargetSnapshotID, CreatedAt: artifact.CreatedAt}
			dir := t.TempDir()
			ap := filepath.Join(dir, "attempt")
			rp := filepath.Join(dir, "requests", r.ID, "request.json")
			out := filepath.Join(dir, "out")
			if mode == "empty" {
				a.NativeID = ""
			}
			if mode == "rollback-no-original" || mode == "rollback" {
				a.Kind = "rollback"
				a.OriginalNativeID = "original"
				artifact.Kind = "rollback"
				artifact.OriginalNativeID = "original"
			}
			putJSON(t, ap, a)
			if mode == "changed-request" {
				r.Description = "changed"
			}
			putJSON(t, rp, r)
			if mode == "alias" {
				out = ap
			}
			if mode == "exists" {
				putJSON(t, out, artifact)
			}
			before, _ := os.ReadFile(ap)
			g := &fakeGate{origin: a.Origin}
			g.origin.PipelineID = "99"
			d := deps(g)
			d.gate = func() (runnerGate, error) {
				if mode == "missing-capability" {
					return g, nil
				}
				return recoveryGate{g, mode == "denied"}, nil
			}
			calls := 0
			d.client = func(gitops.Request, func(string) string) (*pingone.Client, error) { calls++; return nil, nil }
			d.engine = func(*pingone.Client) runnerEngine { return recoveryEngine{&fakeEngine{artifact: artifact}} }
			args := []string{"reconcile-plan", "--attempt", ap, "--attempt-job-id", "41", "--request", rp, "--out", out}
			if mode == "rollback" {
				original := artifact
				original.Kind = "promotion"
				original.NativeID = "original"
				original.OriginalNativeID = ""
				op := filepath.Join(dir, "original")
				putJSON(t, op, original)
				args = append(args, "--original-artifact", op, "--original-job-id", "42")
			}
			var stdout, stderr bytes.Buffer
			err := runWith(args, &stdout, &stderr, d)
			valid := mode == "valid" || mode == "rollback"
			if (err == nil) != valid {
				t.Fatalf("err=%v", err)
			}
			if !valid && calls != 0 {
				t.Fatal("constructed client on rejection")
			}
			after, _ := os.ReadFile(ap)
			if !bytes.Equal(before, after) {
				t.Fatal("attempt overwritten")
			}
			if valid {
				got, err := readArtifact(out)
				if err != nil || got.Origin != a.Origin {
					t.Fatal("lost original provenance")
				}
				if mode == "rollback" && g.originalVerified != 1 {
					t.Fatal("original provenance not verified")
				}
			}
		})
	}
}
