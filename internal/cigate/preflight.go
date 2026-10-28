package cigate

import (
	"context"
	"net/url"
	"strconv"

	"pingone-promote/internal/checks"
)

// Preflight audits the GitLab settings the promotion workflow relies on,
// read-only, using the same no-redirect, fail-closed API reader as Authorize.
// It never needs a request or plan, so it can run before the first promotion.
func (v *Verifier) Preflight(ctx context.Context) checks.Report {
	r := checks.Report{Title: "GitLab preflight"}
	c := v.Config
	if err := c.validate(); err != nil {
		r.Fail("job context", "%s", err.Error())
		return r
	}
	r.Pass("job context", "protected default branch %s, pipeline %s, project %s", c.Branch, c.PipelineID, c.ProjectID)

	var j job
	if _, err := v.get(ctx, "job", true, &j); err != nil {
		r.Fail("job identity", "%s", err.Error())
	} else if strconv.FormatInt(j.ID, 10) != c.JobID || j.Status != "running" || j.Pipeline.ProjectID != mustInt(c.ProjectID) {
		r.Fail("job identity", "job token does not identify this running job")
	} else {
		r.Pass("job identity", "job token identifies running job %s", c.JobID)
	}

	var project struct {
		ID                    int64   `json:"id"`
		Visibility            string  `json:"visibility"`
		DefaultBranch         string  `json:"default_branch"`
		Archived              bool    `json:"archived"`
		OverrideRole          *string `json:"ci_pipeline_variables_minimum_override_role"`
		RestrictUserVariables *bool   `json:"restrict_user_defined_variables"`
		TriggererCanApprove   *bool   `json:"allow_pipeline_trigger_approve_deployment"`
	}
	if _, err := v.get(ctx, "projects/"+c.ProjectID, false, &project); err != nil {
		r.Fail("project", "%s", err.Error())
	} else {
		if strconv.FormatInt(project.ID, 10) == c.ProjectID && project.Visibility == "private" && !project.Archived && project.DefaultBranch == c.DefaultBranch {
			r.Pass("project", "private, active, default branch %s", project.DefaultBranch)
		} else {
			r.Fail("project", "expected an active private project with default branch %s (visibility %q, archived %v, default %q)", c.DefaultBranch, project.Visibility, project.Archived, project.DefaultBranch)
		}
		switch {
		case project.OverrideRole == nil:
			r.Warn("variable overrides", "minimum role for pipeline variables is not visible to this token; confirm it is Maintainer in Settings > CI/CD")
		case *project.OverrideRole == "maintainer" || *project.OverrideRole == "owner":
			r.Pass("variable overrides", "only %s or higher can set pipeline variables", *project.OverrideRole)
		case *project.OverrideRole == "no_one_allowed":
			r.Warn("variable overrides", "no one can set pipeline variables, so rollback pipelines (PROMOTION_MODE, ORIGINAL_PLAN_JOB_ID) cannot be started; use maintainer")
		default:
			r.Fail("variable overrides", "%q can set pipeline variables and could override trusted policy; require maintainer", *project.OverrideRole)
		}
		if project.RestrictUserVariables != nil && !*project.RestrictUserVariables {
			r.Fail("variable overrides", "pipeline variable restriction is disabled")
		}
		switch {
		case project.TriggererCanApprove == nil:
			r.Warn("separation of duties", "pipeline-triggerer approval setting is not visible to this token")
		case *project.TriggererCanApprove:
			r.Warn("separation of duties", "the person who starts a pipeline may approve its own deployment; turn this off for production")
		default:
			r.Pass("separation of duties", "the pipeline triggerer cannot approve its own deployment")
		}
	}

	var branch struct {
		Name      string `json:"name"`
		Protected bool   `json:"protected"`
	}
	if _, err := v.get(ctx, "projects/"+c.ProjectID+"/repository/branches/"+url.PathEscape(c.DefaultBranch), false, &branch); err != nil {
		r.Fail("default branch", "%s", err.Error())
	} else if branch.Name == c.DefaultBranch && branch.Protected {
		r.Pass("default branch", "%s is protected", branch.Name)
	} else {
		r.Fail("default branch", "%s is not protected", c.DefaultBranch)
	}

	var protection struct {
		Name     string `json:"name"`
		Required int    `json:"required_approval_count"`
		Rules    []struct {
			Required int `json:"required_approvals"`
		} `json:"approval_rules"`
	}
	if _, err := v.get(ctx, "projects/"+c.ProjectID+"/protected_environments/"+url.PathEscape(c.Environment), false, &protection); err != nil {
		r.Fail("deployment approval", "protected environment %s unavailable: %s", c.Environment, err.Error())
	} else {
		total := 0
		for _, rule := range protection.Rules {
			total += rule.Required
		}
		if protection.Name == c.Environment && max(protection.Required, total) >= 1 {
			r.Pass("deployment approval", "%s is protected and requires %d approval(s)", c.Environment, max(protection.Required, total))
		} else {
			r.Fail("deployment approval", "%s must be a protected environment requiring at least one approval", c.Environment)
		}
		if protection.Required > 0 && len(protection.Rules) == 0 {
			r.Warn("deployment approval", "approvals are configured as a count without rules; confirm deployments expose approval_summary rules on this GitLab version")
		}
	}
	return r
}

func mustInt(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}
