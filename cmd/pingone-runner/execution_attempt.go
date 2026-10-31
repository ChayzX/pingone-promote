package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"pingone-promote/internal/gitops"
	"pingone-promote/internal/operation"
	"pingone-promote/internal/strictjson"
)

const maxExecutionAttemptBytes = 16 << 10

var (
	executionDigestPattern   = regexp.MustCompile(`^[a-f0-9]{64}$`)
	executionNativeIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,255}$`)
	executionStatusPattern   = regexp.MustCompile(`^[A-Za-z0-9_-]{0,64}$`)
)

// executionAttempt records only metadata needed to bind a possibly dispatched
// execute request to its reviewed native artifact and trusted CI origin.
type executionAttempt struct {
	Version            int                     `json:"version"`
	NativeArtifactID   string                  `json:"nativeArtifactId"`
	ApprovedDigest     string                  `json:"approvedDigest"`
	CreatedAt          string                  `json:"createdAt"`
	RequestFingerprint string                  `json:"requestFingerprint"`
	Origin             gitops.Origin           `json:"origin"`
	Dispatch           operation.DispatchState `json:"dispatchState"`
	NativeStatus       string                  `json:"nativeStatus,omitempty"`
	NativeObserved     bool                    `json:"nativeObserved"`
	EffectsVerified    bool                    `json:"effectsVerified"`
}

type executionAttemptStore interface {
	Begin(context.Context, executionAttempt) error
	Checkpoint(context.Context, executionAttempt) error
}

type executionAttemptFileStore struct{ path string }

func newExecutionAttempt(a gitops.Artifact, digest string) (executionAttempt, error) {
	var attempt executionAttempt
	if a.NativeID == "" || !executionNativeIDPattern.MatchString(a.NativeID) || !executionDigestPattern.MatchString(digest) {
		return attempt, errors.New("invalid execution intent binding")
	}
	if err := a.Origin.Validate(); err != nil {
		return attempt, errors.New("invalid execution trusted origin")
	}
	request, err := json.Marshal(a.Request)
	if err != nil {
		return attempt, errors.New("could not fingerprint execution request")
	}
	fingerprint := sha256.Sum256(request)
	attempt = executionAttempt{
		Version: 1, NativeArtifactID: a.NativeID, ApprovedDigest: digest,
		CreatedAt:          time.Now().UTC().Format(time.RFC3339Nano),
		RequestFingerprint: hex.EncodeToString(fingerprint[:]), Origin: a.Origin,
		Dispatch: operation.Uncertain, NativeObserved: false, EffectsVerified: false,
	}
	return attempt, attempt.Validate()
}

func executionStatusProjection(status string) string {
	if operation.Classify(status) == operation.Unknown {
		return "UNKNOWN"
	}
	return status
}

func (a executionAttempt) Validate() error {
	if a.Version != 1 || !executionNativeIDPattern.MatchString(a.NativeArtifactID) || !executionDigestPattern.MatchString(a.ApprovedDigest) || !executionDigestPattern.MatchString(a.RequestFingerprint) {
		return errors.New("invalid execution attempt metadata")
	}
	if _, err := time.Parse(time.RFC3339Nano, a.CreatedAt); err != nil {
		return errors.New("invalid execution attempt timestamp")
	}
	if err := a.Origin.Validate(); err != nil {
		return errors.New("invalid execution attempt origin")
	}
	if a.EffectsVerified {
		return errors.New("execution attempt cannot claim effects verification")
	}
	switch a.Dispatch {
	case operation.Uncertain:
		if a.NativeObserved || a.NativeStatus != "" {
			return errors.New("uncertain execution attempt cannot contain native observation")
		}
	case operation.Identified:
		if !a.NativeObserved || !executionStatusPattern.MatchString(a.NativeStatus) || (operation.Classify(a.NativeStatus) == operation.Unknown && a.NativeStatus != "UNKNOWN") {
			return errors.New("identified execution attempt requires a bounded native observation")
		}
	default:
		return errors.New("invalid execution dispatch state")
	}
	return nil
}

func (s *executionAttemptFileStore) Begin(ctx context.Context, attempt executionAttempt) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if attempt.Dispatch != operation.Uncertain || attempt.NativeObserved || attempt.NativeStatus != "" {
		return errors.New("execution intent must begin in uncertain state")
	}
	b, err := encodeExecutionAttempt(attempt)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("could not create execute intent: %w", err)
	}
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	return err
}

func (s *executionAttemptFileStore) Checkpoint(ctx context.Context, attempt executionAttempt) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if attempt.Dispatch != operation.Identified || !attempt.NativeObserved {
		return errors.New("execution checkpoint requires a matching native observation")
	}
	b, err := encodeExecutionAttempt(attempt)
	if err != nil {
		return err
	}
	before, err := os.Lstat(s.path)
	if err != nil || !before.Mode().IsRegular() {
		return errors.New("execute intent is missing or not a regular file")
	}
	current, err := readExecutionAttempt(s.path)
	if err != nil || !sameExecutionBinding(current, attempt) {
		return errors.New("execute intent binding changed; reconciliation is required")
	}
	if current.Dispatch == operation.Identified && attempt.Dispatch != operation.Identified {
		return errors.New("execution checkpoint cannot reduce dispatch certainty")
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".execute-intent-*")
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
		return errors.New("execute intent changed during checkpoint")
	}
	return os.Rename(tmp.Name(), s.path)
}

func sameExecutionBinding(a, b executionAttempt) bool {
	return a.Version == b.Version && a.NativeArtifactID == b.NativeArtifactID && a.ApprovedDigest == b.ApprovedDigest && a.CreatedAt == b.CreatedAt && a.RequestFingerprint == b.RequestFingerprint && a.Origin == b.Origin
}

func encodeExecutionAttempt(attempt executionAttempt) ([]byte, error) {
	if err := attempt.Validate(); err != nil {
		return nil, err
	}
	b, err := json.Marshal(attempt)
	if err != nil || len(b) > maxExecutionAttemptBytes {
		return nil, errors.New("invalid or oversized execution attempt")
	}
	return append(b, '\n'), nil
}

func readExecutionAttempt(path string) (executionAttempt, error) {
	var attempt executionAttempt
	info, err := os.Lstat(path)
	if err != nil {
		return attempt, err
	}
	if !info.Mode().IsRegular() {
		return attempt, errors.New("execution attempt is not a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return attempt, err
	}
	b, readErr := io.ReadAll(io.LimitReader(f, maxExecutionAttemptBytes+1))
	closeErr := f.Close()
	if readErr == nil {
		readErr = closeErr
	}
	if readErr != nil {
		return attempt, readErr
	}
	if len(b) > maxExecutionAttemptBytes || strictjson.Unmarshal(b, &attempt) != nil {
		return attempt, errors.New("invalid execution attempt JSON")
	}
	return attempt, attempt.Validate()
}
