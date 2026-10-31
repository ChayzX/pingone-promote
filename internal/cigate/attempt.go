package cigate

import (
	"context"
	"encoding/json"
	"errors"
	"pingone-promote/internal/gitops"
	"pingone-promote/internal/strictjson"
	"strconv"
	"strings"
)

// VerifyDraftAttempt authenticates the exact retained checkpoint, not editable metadata.
// VerifyPlan must separately authenticate the current running context.
func (v *Verifier) VerifyDraftAttempt(ctx context.Context, a gitops.DraftAttempt, jobID string) error {
	if err := a.Validate(); err != nil {
		return err
	}
	if err := v.Config.validate(); err != nil {
		return err
	}
	c := v.Config
	if !number.MatchString(jobID) || a.Origin.ServerURL != strings.TrimRight(c.BaseURL, "/") || a.Origin.ProjectID != c.ProjectID {
		return errors.New("draft attempt job or trusted origin mismatch")
	}
	name, file := "plan_promotion", "plan.json.draft-attempt"
	if a.Kind == "rollback" {
		name, file = "rollback_plan", "rollback.json.draft-attempt"
	}
	var source job
	base := "projects/" + c.ProjectID + "/jobs/" + jobID
	if _, err := v.get(ctx, base, false, &source); err != nil {
		return err
	}
	terminal := source.Status == "success" || source.Status == "failed" || source.Status == "canceled"
	allowedSource := source.Pipeline.Source == "web" || (a.Kind == "promotion" && source.Pipeline.Source == "push")
	if strconv.FormatInt(source.ID, 10) != jobID || source.Name != name || !terminal || !allowedSource || source.Tag == nil || *source.Tag || source.Ref != c.DefaultBranch || source.Commit.ID != a.Origin.CommitSHA || strconv.FormatInt(source.Pipeline.ID, 10) != a.Origin.PipelineID || strconv.FormatInt(source.Pipeline.ProjectID, 10) != c.ProjectID || source.Pipeline.SHA != a.Origin.CommitSHA || source.Pipeline.Ref != c.DefaultBranch {
		return errors.New("draft attempt does not match a terminal protected-default-branch planning job")
	}
	var raw json.RawMessage
	if _, err := v.get(ctx, base+"/artifacts/"+file, false, &raw); err != nil {
		return errors.New("retained draft attempt unavailable or redirected; blocked investigation required; preserve evidence without retrying draft creation")
	}
	var retained gitops.DraftAttempt
	if len(raw) > 16<<10 || strictjson.Unmarshal(raw, &retained) != nil || retained != a {
		return errors.New("retained draft attempt differs or has invalid JSON")
	}
	return nil
}
