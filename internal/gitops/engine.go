package gitops

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path"
	"reflect"
	"regexp"
	"strings"
	"time"

	"pingone-promote/internal/pingone"
	"pingone-promote/internal/promotion"
)

type Origin struct {
	ServerURL  string `json:"serverUrl"`
	ProjectID  string `json:"projectId"`
	PipelineID string `json:"pipelineId"`
	CommitSHA  string `json:"commitSha"`
}
type Artifact struct {
	Version          int              `json:"version"`
	Kind             string           `json:"kind"`
	NativeID         string           `json:"nativeId"`
	OriginalNativeID string           `json:"originalNativeId,omitempty"`
	CreatedAt        string           `json:"createdAt"`
	Request          Request          `json:"request"`
	Origin           Origin           `json:"origin"`
	TargetSnapshotID string           `json:"targetSnapshotId"`
	Review           promotion.Review `json:"review"`
}
type Engine struct{ Client *pingone.Client }

var numericID = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
var commitID = regexp.MustCompile(`^[a-f0-9]{40}$`)

func (o Origin) Validate() error {
	u, err := url.Parse(o.ServerURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !cleanGitLabPath(o.ServerURL, u) || !numericID.MatchString(o.ProjectID) || !numericID.MatchString(o.PipelineID) || !commitID.MatchString(o.CommitSHA) {
		return errors.New("artifact requires a trusted HTTPS GitLab origin, numeric project/pipeline identities and full commit SHA")
	}
	return nil
}

// cleanGitLabPath permits enterprise installations under a stable path prefix.
func cleanGitLabPath(raw string, u *url.URL) bool {
	if u.RawPath != "" || u.ForceQuery || strings.ContainsAny(raw, "%#\\ \t\r\n") || strings.Contains(u.Path, "//") {
		return false
	}
	if u.Path == "" || u.Path == "/" {
		return true
	}
	return path.Clean(u.Path) == strings.TrimSuffix(u.Path, "/")
}
func (a Artifact) Validate() error      { return validateArtifact(a, true) }
func ValidateArtifact(a Artifact) error { return a.Validate() }
func validateArtifact(a Artifact, review bool) error {
	if err := Validate(a.Request); err != nil {
		return err
	}
	if err := a.Origin.Validate(); err != nil {
		return err
	}
	if a.Version != 1 || !safeID.MatchString(a.NativeID) || !safeID.MatchString(a.TargetSnapshotID) || (a.Kind != "promotion" && a.Kind != "rollback") {
		return errors.New("invalid artifact version, native identity, kind or target snapshot")
	}
	if (a.Kind == "rollback" && (!safeID.MatchString(a.OriginalNativeID) || a.NativeID == a.OriginalNativeID)) || (a.Kind == "promotion" && a.OriginalNativeID != "") {
		return errors.New("invalid original promotion linkage")
	}
	if _, err := time.Parse(time.RFC3339Nano, a.CreatedAt); err != nil {
		return errors.New("invalid artifact creation time")
	}
	if review {
		if !hashID.MatchString(a.Review.Digest) {
			return errors.New("artifact has no valid review digest")
		}
		raw, err := json.Marshal(a.Review)
		if err != nil {
			return errors.New("invalid artifact review")
		}
		var value any
		if json.Unmarshal(raw, &value) != nil || !reflect.DeepEqual(value, promotion.Redact(value)) {
			return errors.New("artifact review contains unredacted sensitive fields")
		}
	}
	return nil
}
func (e *Engine) check(r Request) error {
	if err := Validate(r); err != nil {
		return err
	}
	if e == nil || e.Client == nil {
		return errors.New("PingOne client is required")
	}
	u, err := url.Parse(e.Client.BaseURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("PingOne client requires a trusted HTTPS API origin")
	}
	ip := net.ParseIP(u.Hostname())
	if ip != nil && ip.IsLoopback() {
		return nil
	}
	for region, domain := range pingone.RegionDomains {
		if u.Host == "api."+domain {
			if r.Source.Region != region || r.Target.Region != region {
				return errors.New("request regions differ from trusted PingOne API origin")
			}
			return nil
		}
	}
	return errors.New("PingOne API origin is not a supported regional API")
}
func (e *Engine) inventories(ctx context.Context, r Request, targetPin string) (pingone.Inventory, pingone.Inventory, error) {
	var empty pingone.Inventory
	if err := e.check(r); err != nil {
		return empty, empty, err
	}
	source, err := e.Client.InventoryVersion(ctx, r.Source.ID, r.Source.SnapshotID)
	if err != nil {
		return empty, empty, err
	}
	if source.OrganizationID != r.Source.OrganizationID {
		return empty, empty, errors.New("pinned source organization differs from request")
	}
	resources := map[string]pingone.Resource{}
	for _, res := range source.Resources {
		resources[res.ID] = res
	}
	for _, id := range r.Selected {
		res, ok := resources[id]
		if !ok || fingerprint(promotion.Redact(res.Configuration)) != r.Fingerprints[id] {
			return empty, empty, errors.New("pinned selected source configuration differs from the reviewed request")
		}
	}
	var target pingone.Inventory
	if targetPin == "" {
		target, err = e.Client.Inventory(ctx, r.Target.ID)
	} else {
		target, err = e.Client.InventoryVersion(ctx, r.Target.ID, targetPin)
	}
	if err != nil {
		return empty, empty, err
	}
	if target.OrganizationID != r.Target.OrganizationID {
		return empty, empty, errors.New("target organization differs from request")
	}
	if _, err = promotion.BuildRequest(source, target, r.Selected, r.Mappings, r.Description); err != nil {
		return empty, empty, err
	}
	return source, target, nil
}
func (e *Engine) Plan(ctx context.Context, r Request, origin Origin) (Artifact, error) {
	var a Artifact
	if err := origin.Validate(); err != nil {
		return a, err
	}
	source, target, err := e.inventories(ctx, r, "")
	if err != nil {
		return a, err
	}
	body, err := promotion.BuildRequest(source, target, r.Selected, r.Mappings, r.Description)
	if err != nil {
		return a, err
	}
	var native pingone.Promotion
	if err = e.Client.Do(ctx, "POST", pingone.EnvPath(r.Source.ID)+"/promotions", "", body, &native); err != nil {
		return a, err
	}
	if !safeID.MatchString(native.ID) {
		return a, errors.New("draft response omitted safe native promotion identity; inspect native status before creating another draft")
	}
	a = Artifact{Version: 1, Kind: "promotion", NativeID: native.ID, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Request: r, Origin: origin, TargetSnapshotID: target.SnapshotID}
	// The native service may pin a fresher target snapshot while preparing its plan.
	native, err = e.wait(ctx, r.Source.ID, native.ID)
	if err != nil {
		return a, err
	}
	if native.TargetSnapshot.ID != "" {
		if !safeID.MatchString(native.TargetSnapshot.ID) {
			return a, errors.New("invalid native target snapshot identity")
		}
		a.TargetSnapshotID = native.TargetSnapshot.ID
	}
	return e.refresh(ctx, a, false)
}
func pending(status string) bool {
	switch status {
	case "NEW", "PREPARING", "ADDITIONAL_SNAPSHOTS_REQUESTED", "ROLLBACK_NEW", "ROLLBACK_PREPARING":
		return true
	}
	return false
}
func (e *Engine) wait(ctx context.Context, env, id string) (pingone.Promotion, error) {
	var last pingone.Promotion
	if err := ctx.Err(); err != nil {
		return last, err
	}
	pollingCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	for {
		p, err := e.Client.GetPromotion(pollingCtx, env, id)
		if err != nil {
			if ctx.Err() != nil {
				return last, ctx.Err()
			}
			if pollingCtx.Err() != nil && last.ID != "" {
				return last, nil
			}
			return p, err
		}
		last = p
		if !pending(p.Status) {
			return p, nil
		}
		select {
		case <-pollingCtx.Done():
			if ctx.Err() != nil {
				return last, ctx.Err()
			}
			// The caller receives a blocked pending review when preparation needs longer.
			return last, nil
		case <-time.After(time.Second):
		}
	}
}
func (e *Engine) Refresh(ctx context.Context, a Artifact) (Artifact, error) {
	return e.refresh(ctx, a, true)
}
func (e *Engine) refresh(ctx context.Context, a Artifact, existing bool) (Artifact, error) {
	if err := validateArtifact(a, existing); err != nil {
		return a, err
	}
	source, target, err := e.inventories(ctx, a.Request, a.TargetSnapshotID)
	if err != nil {
		return a, err
	}
	p, err := e.Client.GetPromotion(ctx, a.Request.Source.ID, a.NativeID)
	if err != nil {
		return a, err
	}
	steps := []pingone.Step{}
	if !pending(p.Status) {
		steps, err = e.Client.Steps(ctx, a.Request.Source.ID, a.NativeID)
		if err != nil {
			return a, err
		}
	}
	for i := range steps {
		if bytes.Equal(bytes.TrimSpace(steps[i].Payload), []byte("null")) {
			steps[i].Payload = nil
		}
	}
	a.Review = promotion.Inspect(source, target, a.Request.Selected, a.Request.Mappings, steps, a.Kind == "rollback")
	for _, issue := range nativeIssues(a, p) {
		a.Review.Issues = append(a.Review.Issues, issue)
	}
	if p.Status != "READY" {
		a.Review.Issues = append(a.Review.Issues, "native promotion is not READY; inspect status before any execution")
	}
	// Bind full unredacted native step digest to all execution identities. Only the
	// redacted review and resulting digest cross the artifact boundary.
	a.Review.Digest = fingerprint(struct {
		StepDigest                                         string
		Kind, NativeID, OriginalNativeID, TargetSnapshotID string
		Origin                                             Origin
		Request                                            Request
	}{a.Review.Digest, a.Kind, a.NativeID, a.OriginalNativeID, a.TargetSnapshotID, a.Origin, a.Request})
	return a, nil
}
func nativeIssues(a Artifact, p pingone.Promotion) []string {
	issues := []string{}
	if p.ID != a.NativeID || p.Source.ID != a.Request.Source.ID || p.Target.ID != a.Request.Target.ID {
		issues = append(issues, "native promotion identity or source/target differs from the approved request")
	}
	if p.SourceSnapshot.ID != a.Request.Source.SnapshotID {
		issues = append(issues, "native source snapshot differs from the pinned request")
	}
	if p.TargetSnapshot.ID != "" && p.TargetSnapshot.ID != a.TargetSnapshotID {
		issues = append(issues, "native target snapshot differs from the reviewed target")
	}
	if !sameMappings(p.Mapping, a.Request.Mappings) {
		issues = append(issues, "native resource mappings differ from the approved request")
	}
	if a.Kind == "rollback" && p.Previous.ID != a.OriginalNativeID {
		issues = append(issues, "native rollback is not linked to the approved original promotion")
	}
	if nativeErrors(p.Errors) {
		issues = append(issues, "native promotion reports errors; inspect PingOne before proceeding")
	}
	return issues
}
func sameMappings(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
func nativeErrors(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return true
	}
	switch x := v.(type) {
	case nil:
		return false
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	case string:
		return x != ""
	}
	return true
}
func (e *Engine) Apply(ctx context.Context, a Artifact, approvedDigest string, authorize func(context.Context, Artifact) error) (pingone.Promotion, error) {
	var result pingone.Promotion
	if authorize == nil {
		return result, errors.New("live protected deployment authorization is required")
	}
	if !hashID.MatchString(approvedDigest) || subtle.ConstantTimeCompare([]byte(approvedDigest), []byte(a.Review.Digest)) != 1 {
		return result, errors.New("approved digest does not match the artifact review")
	}
	fresh, err := e.Refresh(ctx, a)
	if err != nil {
		return result, err
	}
	if len(fresh.Review.Issues) > 0 {
		return result, errors.New("execution blocked by native status, identity or review issues")
	}
	if subtle.ConstantTimeCompare([]byte(approvedDigest), []byte(fresh.Review.Digest)) != 1 {
		return result, errors.New("native plan changed after approval; review a fresh artifact")
	}
	if err = authorize(ctx, fresh); err != nil {
		return result, err
	}
	if err = e.Client.Do(ctx, "POST", pingone.EnvPath(a.Request.Source.ID)+"/promotions/"+a.NativeID, pingone.ExecuteType, nil, &result); err != nil {
		return result, err
	}
	if result.ID != a.NativeID {
		return result, errors.New("execute response identity differs; reconcile native status before any retry")
	}
	return result, nil
}
func (e *Engine) RollbackPlan(ctx context.Context, original Artifact, origin Origin) (Artifact, error) {
	var a Artifact
	if err := original.Validate(); err != nil {
		return a, err
	}
	if err := origin.Validate(); err != nil {
		return a, err
	}
	if original.Kind != "promotion" {
		return a, errors.New("only a forward promotion may produce a rollback plan")
	}
	p, err := e.Status(ctx, original)
	if err != nil {
		return a, err
	}
	if p.Status != "COMPLETED" && p.Status != "ERROR" {
		return a, errors.New("only a completed or failed promotion can produce a rollback plan")
	}
	var native pingone.Promotion
	if err = e.Client.Do(ctx, "POST", pingone.EnvPath(original.Request.Source.ID)+"/promotions/"+original.NativeID, pingone.RollbackType, nil, &native); err != nil {
		return a, err
	}
	if !safeID.MatchString(native.ID) || native.ID == original.NativeID || native.Previous.ID != original.NativeID {
		return a, errors.New("rollback did not create a distinct linked plan; reconcile native status before retrying")
	}
	a = original
	a.Kind = "rollback"
	a.NativeID = native.ID
	a.OriginalNativeID = original.NativeID
	a.Origin = origin
	a.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	native, err = e.wait(ctx, a.Request.Source.ID, a.NativeID)
	if err != nil {
		return a, err
	}
	if native.TargetSnapshot.ID != "" {
		a.TargetSnapshotID = native.TargetSnapshot.ID
	}
	return e.refresh(ctx, a, false)
}
func (e *Engine) Status(ctx context.Context, a Artifact) (pingone.Promotion, error) {
	var p pingone.Promotion
	if err := a.Validate(); err != nil {
		return p, err
	}
	if err := e.check(a.Request); err != nil {
		return p, err
	}
	p, err := e.Client.GetPromotion(ctx, a.Request.Source.ID, a.NativeID)
	if err != nil {
		return p, err
	}
	identity := p
	identity.Errors = nil // Failed native executions must remain observable and rollback eligible.
	if issues := nativeIssues(a, identity); len(issues) > 0 {
		return p, fmt.Errorf("native status identity check failed: %s", strings.Join(issues, "; "))
	}
	return p, nil
}
