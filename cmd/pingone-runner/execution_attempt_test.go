package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pingone-promote/internal/operation"
)

type failingCheckpointStore struct{ executionAttemptStore }

func (f failingCheckpointStore) Checkpoint(context.Context, executionAttempt) error {
	return errors.New("synthetic checkpoint failure")
}

func TestExecutionAttemptStoreBeginsExclusiveMetadataOnlyIntent(t *testing.T) {
	d := t.TempDir()
	_, artifact := fixture(t)
	attempt, err := newExecutionAttempt(artifact, artifact.Review.Digest)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(d, "artifact.execute-intent")
	store := &executionAttemptFileStore{path: path}
	if err := store.Begin(context.Background(), attempt); err != nil {
		t.Fatal(err)
	}
	saved, err := readExecutionAttempt(path)
	if err != nil || saved.Dispatch != operation.Uncertain || saved.NativeObserved || saved.NativeStatus != "" || saved.EffectsVerified {
		t.Fatalf("initial intent=%+v err=%v", saved, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private intent mode=%v err=%v", info, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"flow", "review", "configurations", "secret-value"} {
		if strings.Contains(string(raw), private) {
			t.Fatalf("intent contains request payload field %q: %s", private, raw)
		}
	}
	if err = store.Begin(context.Background(), attempt); err == nil {
		t.Fatal("second begin replaced an existing intent")
	}
}

func TestExecutionAttemptCheckpointRequiresMatchingIdentityAndSafeStatus(t *testing.T) {
	d := t.TempDir()
	_, artifact := fixture(t)
	attempt, err := newExecutionAttempt(artifact, artifact.Review.Digest)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(d, "artifact.execute-intent")
	store := &executionAttemptFileStore{path: path}
	if err := store.Begin(context.Background(), attempt); err != nil {
		t.Fatal(err)
	}
	identified := attempt
	identified.Dispatch = operation.Identified
	identified.NativeObserved = true
	identified.NativeStatus = "COMPLETED"
	identified.NativeArtifactID = "wrong-id"
	if err = store.Checkpoint(context.Background(), identified); err == nil {
		t.Fatal("checkpoint accepted a different native identity")
	}
	identified.NativeArtifactID = artifact.NativeID
	identified.NativeStatus = "FUTURE_STATUS"
	if err = store.Checkpoint(context.Background(), identified); err == nil {
		t.Fatal("checkpoint accepted an unprojected unknown native status")
	}
	saved, err := readExecutionAttempt(path)
	if err != nil || saved.Dispatch != operation.Uncertain || saved.NativeObserved {
		t.Fatalf("failed checkpoint did not retain uncertain intent: %+v err=%v", saved, err)
	}
	identified.NativeStatus = "UNKNOWN"
	if err = store.Checkpoint(context.Background(), identified); err != nil {
		t.Fatal(err)
	}
	saved, err = readExecutionAttempt(path)
	if err != nil || saved.Dispatch != operation.Identified || !saved.NativeObserved || saved.NativeStatus != "UNKNOWN" || saved.EffectsVerified {
		t.Fatalf("identified checkpoint=%+v err=%v", saved, err)
	}
}

func TestExecutionAttemptReaderRejectsMalformedAndLegacyRecords(t *testing.T) {
	d := t.TempDir()
	for name, content := range map[string]string{
		"malformed": "{",
		"legacy":    "artifact=native-1\napprovedDigest=abc\ncreatedAt=2026-01-01T00:00:00Z\n",
	} {
		path := filepath.Join(d, name)
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readExecutionAttempt(path); err == nil {
			t.Fatalf("read accepted %s execute intent", name)
		}
	}
}
