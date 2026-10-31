package gitops

import (
	"context"
	"errors"
	"fmt"
	"pingone-promote/internal/pingone"
	"strings"
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

// ReconcileDraft reconstructs diagnostic evidence without creating or executing a draft.
func (e *Engine) ReconcileDraft(ctx context.Context, attempt DraftAttempt, request Request, origin Origin) (Artifact, error) {
	var a Artifact
	if err := attempt.ValidateRequest(request); err != nil {
		return a, err
	}
	if err := attempt.Validate(); err != nil {
		return a, err
	}
	if err := origin.Validate(); err != nil {
		return a, err
	}
	if !sameGitLabProject(attempt.Origin, origin) || attempt.RequestID != request.ID || attempt.RequestDigest != fingerprint(request) || attempt.Source != request.Source || attempt.Target != request.Target {
		return a, errors.New("draft attempt does not match request or trusted project")
	}
	if attempt.NativeID == "" {
		return a, errors.New("draft identity unavailable; blocked investigation required; do not guess candidates or retry draft creation")
	}
	if err := e.check(request); err != nil {
		return a, err
	}
	a = Artifact{Version: 1, Kind: attempt.Kind, NativeID: attempt.NativeID, OriginalNativeID: attempt.OriginalNativeID, CreatedAt: attempt.CreatedAt, Request: request, Origin: attempt.Origin, TargetSnapshotID: attempt.TargetSnapshotID}
	p, err := e.Client.GetPromotion(ctx, request.Source.ID, attempt.NativeID)
	if err != nil {
		return Artifact{}, err
	}
	if p.TargetSnapshot.ID != "" {
		if !safeID.MatchString(p.TargetSnapshot.ID) {
			return Artifact{}, errors.New("invalid native target snapshot identity")
		}
		a.TargetSnapshotID = p.TargetSnapshot.ID
	}
	identity := p
	identity.Errors = nil
	identity.PromotionErrors = nil
	if issues := nativeIssues(a, identity); len(issues) > 0 {
		return Artifact{}, fmt.Errorf("draft identity mismatch: %s", strings.Join(issues, "; "))
	}
	return e.refresh(ctx, a, false)
}

// ValidateRequest binds a retained attempt to the complete supplied request.
func (a DraftAttempt) ValidateRequest(r Request) error {
	if err := Validate(r); err != nil {
		return err
	}
	if err := a.Validate(); err != nil {
		return err
	}
	if a.RequestID != r.ID || a.RequestDigest != fingerprint(r) || a.Source != r.Source || a.Target != r.Target {
		return errors.New("draft attempt request binding mismatch")
	}
	return nil
}
