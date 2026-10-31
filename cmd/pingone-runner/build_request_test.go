package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pingone-promote/internal/gitops"
	"pingone-promote/internal/pingone"
)

const intentID = "0123456789abcdef0123456789abcdef"

const intentDoc = `{"version":1,"id":"` + intentID + `","requestedBy":{"pingoneUserId":"user-1","username":"chase"},"source":{"id":"source","region":"us"},"target":{"id":"target","region":"us"},"selected":["flow"],"mappings":{"flow":"dest"},"description":"Promote the login flow"}`

func writeIntent(t *testing.T, dirName, doc string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "intents", dirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "intent.json")
	if err := os.WriteFile(p, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

type buildFixture struct {
	gate       *fakeGate
	inventoryN int
	deps       runnerDeps
}

func newBuildFixture() *buildFixture {
	f := &buildFixture{gate: &fakeGate{}}
	origin := "https://api.pingone.com"
	f.deps = runnerDeps{
		getenv: func(string) string { return "" },
		gate:   func() (runnerGate, error) { return f.gate, nil },
		client: func(r gitops.Request, _ func(string) string) (*pingone.Client, error) {
			if r.Source.ID != "source" || r.Source.Region != "us" {
				return nil, errors.New("client built for the wrong environment")
			}
			return &pingone.Client{}, nil
		},
		inventory: func(_ context.Context, _ *pingone.Client, env string) (pingone.Inventory, error) {
			f.inventoryN++
			switch env {
			case "source":
				return pingone.Inventory{APIOrigin: origin, EnvironmentID: "source", OrganizationID: "org", SnapshotID: "sourcepin", Resources: []pingone.Resource{{ID: "flow", URL: origin + "/v1/environments/source/flows/flow", Type: "flows", Configuration: map[string]any{"name": "Flow", "clientSecret": "secret-value"}}}}, nil
			case "target":
				return pingone.Inventory{APIOrigin: origin, EnvironmentID: "target", OrganizationID: "org", SnapshotID: "targetpin", Resources: []pingone.Resource{{ID: "dest", URL: origin + "/v1/environments/target/flows/dest", Type: "flows", Configuration: map[string]any{"name": "Old"}}}}, nil
			}
			return pingone.Inventory{}, errors.New("unexpected environment")
		},
	}
	return f
}

func TestBuildRequestWritesValidRedactedRequestAfterGateChecks(t *testing.T) {
	f := newBuildFixture()
	in := writeIntent(t, intentID, intentDoc)
	out := filepath.Join(filepath.Dir(in), "request.json")
	var stdout, stderr strings.Builder
	if err := runWith([]string{"build-request", "--intent", in, "--out", out}, &stdout, &stderr, f.deps); err != nil {
		t.Fatalf("build-request failed: %v", err)
	}
	if f.gate.targetChecked != 1 || f.gate.verified != 1 {
		t.Fatalf("gate not consulted: target=%d plan=%d", f.gate.targetChecked, f.gate.verified)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret-value") {
		t.Fatal("built request leaked a secret")
	}
	r, err := gitops.Decode(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("built request is not valid: %v", err)
	}
	if r.ID != intentID || !strings.Contains(r.Description, "chase") {
		t.Fatalf("request not bound to intent/requester: id=%s desc=%q", r.ID, r.Description)
	}
	// The built request must also pass the existing validate command.
	if err = runWith([]string{"validate", "--request", out}, &stdout, &stderr, f.deps); err != nil {
		t.Fatalf("validate rejected built request: %v", err)
	}
	// Never overwrite an existing output.
	if err = runWith([]string{"build-request", "--intent", in, "--out", out}, &stdout, &stderr, f.deps); err == nil {
		t.Fatal("overwrote an existing request")
	}
}

func TestBuildRequestTakesNoSnapshotOutsideProtectedJob(t *testing.T) {
	f := newBuildFixture()
	f.gate.targetErr = errors.New("request destination differs from protected runner target policy")
	in := writeIntent(t, intentID, intentDoc)
	var stdout, stderr strings.Builder
	err := runWith([]string{"build-request", "--intent", in, "--out", filepath.Join(filepath.Dir(in), "request.json")}, &stdout, &stderr, f.deps)
	if err == nil || f.inventoryN != 0 {
		t.Fatalf("snapshots taken without a verified job: err=%v inventories=%d", err, f.inventoryN)
	}
	// Default deps (no CI context) must fail closed too.
	if err = runWith([]string{"build-request", "--intent", in, "--out", filepath.Join(filepath.Dir(in), "request.json")}, &stdout, &stderr, runnerDeps{}); err == nil {
		t.Fatal("build-request ran without a CI verifier")
	}
}

func TestBuildRequestOutputMustMatchIntentPathBeforeGateOrInventory(t *testing.T) {
	f := newBuildFixture()
	in := writeIntent(t, intentID, intentDoc)
	wrongPaths := []string{
		filepath.Join(t.TempDir(), "request.json"),
		filepath.Join(filepath.Dir(in), "other.json"),
		filepath.Join(filepath.Dir(filepath.Dir(in)), "other-id", "request.json"),
	}
	for _, out := range wrongPaths {
		err := runWith([]string{"build-request", "--intent", in, "--out", out}, &strings.Builder{}, &strings.Builder{}, f.deps)
		if err == nil || !strings.Contains(err.Error(), "output must be intents/") {
			t.Errorf("output path %q was not rejected: %v", out, err)
		}
	}
	if f.gate.targetChecked != 0 || f.gate.verified != 0 || f.inventoryN != 0 {
		t.Fatalf("mismatched output reached protected work: target=%d plan=%d inventories=%d", f.gate.targetChecked, f.gate.verified, f.inventoryN)
	}
}

func TestIntentMustLiveInItsOwnIDDirectory(t *testing.T) {
	f := newBuildFixture()
	wrongDir := writeIntent(t, "fedcba9876543210fedcba9876543210", intentDoc)
	var stdout, stderr strings.Builder
	for _, args := range [][]string{
		{"validate", "--intent", wrongDir},
		{"build-request", "--intent", wrongDir, "--out", filepath.Join(filepath.Dir(wrongDir), "request.json")},
	} {
		if err := runWith(args, &stdout, &stderr, f.deps); err == nil || !strings.Contains(err.Error(), "location") {
			t.Fatalf("%v: accepted an intent outside its ID directory: %v", args[0], err)
		}
	}
	if f.inventoryN != 0 {
		t.Fatal("snapshots taken for a misplaced intent")
	}
	good := writeIntent(t, intentID, intentDoc)
	if err := runWith([]string{"validate", "--intent", good}, &stdout, &stderr, f.deps); err != nil {
		t.Fatalf("valid intent rejected: %v", err)
	}
	if err := runWith([]string{"validate", "--intent", good, "--request", good}, &stdout, &stderr, f.deps); err == nil {
		t.Fatal("validate accepted both --intent and --request")
	}
}
