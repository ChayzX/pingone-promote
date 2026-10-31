package gitops

import (
	"context"
	"errors"
	"fmt"
	"pingone-promote/internal/pingone"
	"time"
)

type AttemptStore interface {
	Begin(context.Context, DraftAttempt) error
	Checkpoint(context.Context, DraftAttempt) error
}

// DraftAttempt contains only recovery identities and a fingerprint, never request payloads.
type DraftAttempt struct {
	Version          int         `json:"version"`
	Kind             string      `json:"kind"`
	RequestID        string      `json:"requestId"`
	RequestDigest    string      `json:"requestDigest"`
	Origin           Origin      `json:"origin"`
	NativeID         string      `json:"nativeId,omitempty"`
	OriginalNativeID string      `json:"originalNativeId,omitempty"`
	Source           Environment `json:"source"`
	Target           Environment `json:"target"`
	TargetSnapshotID string      `json:"targetSnapshotId"`
	CreatedAt        string      `json:"createdAt"`
}

func (a DraftAttempt) Validate() error {
	if a.Version != 1 || !hexID.MatchString(a.RequestID) || !hashID.MatchString(a.RequestDigest) || !safeID.MatchString(a.TargetSnapshotID) {
		return errors.New("invalid draft attempt identity")
	}
	if err := a.Origin.Validate(); err != nil {
		return err
	}
	if _, err := time.Parse(time.RFC3339Nano, a.CreatedAt); err != nil {
		return errors.New("invalid draft attempt time")
	}
	for _, e := range []Environment{a.Source, a.Target} {
		if !safeID.MatchString(e.ID) || !safeID.MatchString(e.OrganizationID) || !safeID.MatchString(e.SnapshotID) || pingone.RegionDomains[e.Region] == "" {
			return errors.New("invalid draft attempt environment")
		}
	}
	if a.Source.ID == a.Target.ID || a.Source.OrganizationID != a.Target.OrganizationID {
		return errors.New("invalid draft attempt environment linkage")
	}
	if a.NativeID != "" && !safeID.MatchString(a.NativeID) {
		return errors.New("invalid draft native identity")
	}
	if (a.Kind != "promotion" && a.Kind != "rollback") || (a.Kind == "promotion" && a.OriginalNativeID != "") || (a.Kind == "rollback" && (!safeID.MatchString(a.OriginalNativeID) || a.NativeID == a.OriginalNativeID)) {
		return errors.New("invalid draft attempt kind or original linkage")
	}
	return nil
}
func newDraftAttempt(r Request, o Origin, kind, original, pin string) DraftAttempt {
	return DraftAttempt{Version: 1, Kind: kind, RequestID: r.ID, RequestDigest: fingerprint(r), Origin: o, OriginalNativeID: original, Source: r.Source, Target: r.Target, TargetSnapshotID: pin, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
}
func (e *Engine) beginDraft(ctx context.Context, a DraftAttempt) error {
	if err := a.Validate(); err != nil {
		return err
	}
	if e.DraftAttempts != nil {
		if err := e.DraftAttempts.Begin(ctx, a); err != nil {
			return errors.New("could not persist draft attempt; no draft dispatched")
		}
	}
	return nil
}
func (e *Engine) checkpointDraft(ctx context.Context, a DraftAttempt) error {
	if err := a.Validate(); err != nil {
		return err
	}
	if e.DraftAttempts != nil {
		if err := e.DraftAttempts.Checkpoint(ctx, a); err != nil {
			return fmt.Errorf("could not checkpoint accepted draft identity %s; retained attempt may contain intent only; preserve this returned identity and reconcile before retry", a.NativeID)
		}
	}
	return nil
}
