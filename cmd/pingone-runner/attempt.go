package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"pingone-promote/internal/gitops"
	"pingone-promote/internal/strictjson"
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
			return errors.New("draft attempt sidecar must not alias inputs or outputs")
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
