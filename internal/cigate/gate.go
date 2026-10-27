// Package cigate verifies live GitLab job/deployment authorization. The real
// credential boundary remains protected runner secrets and PingOne roles.
package cigate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"pingone-promote/internal/gitops"
	"pingone-promote/internal/pingone"
)

type Config struct {
	BaseURL, ExpectedURL, ProjectID, ExpectedProjectID, PipelineID, JobID, CommitSHA string
	Branch, DefaultBranch, Environment, TargetEnvironmentID, TargetRegion            string
	JobToken, ReadToken, PipelineSource                                              string
	ProtectedRef                                                                     bool
}

var number = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
var commitSHA = regexp.MustCompile(`^[a-f0-9]{40}$`)
var identity = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,255}$`)

func FromEnv(getenv func(string) string) (Config, error) {
	c := Config{BaseURL: getenv("CI_SERVER_URL"), ExpectedURL: getenv("PROMOTION_GITLAB_URL"), ProjectID: getenv("CI_PROJECT_ID"), ExpectedProjectID: getenv("PROMOTION_GITLAB_PROJECT_ID"), PipelineID: getenv("CI_PIPELINE_ID"), JobID: getenv("CI_JOB_ID"), CommitSHA: getenv("CI_COMMIT_SHA"), Branch: getenv("CI_COMMIT_BRANCH"), DefaultBranch: getenv("CI_DEFAULT_BRANCH"), Environment: getenv("PROMOTION_ENVIRONMENT"), TargetEnvironmentID: getenv("PINGONE_TARGET_ENVIRONMENT_ID"), TargetRegion: getenv("PINGONE_TARGET_REGION"), JobToken: getenv("CI_JOB_TOKEN"), ReadToken: getenv("GITLAB_READ_API_TOKEN"), ProtectedRef: getenv("CI_COMMIT_REF_PROTECTED") == "true", PipelineSource: getenv("CI_PIPELINE_SOURCE")}
	if c.Environment == "" {
		c.Environment = "production"
	}
	if getenv("GITLAB_CI") != "true" {
		return c, errors.New("this command requires an authenticated GitLab runner job")
	}
	return c, c.validate()
}
func (c Config) validate() error {
	if _, err := baseURL(c.BaseURL); err != nil {
		return err
	}
	if _, err := baseURL(c.ExpectedURL); err != nil {
		return err
	}
	if strings.TrimRight(c.BaseURL, "/") != strings.TrimRight(c.ExpectedURL, "/") || !number.MatchString(c.ProjectID) || c.ProjectID != c.ExpectedProjectID || !number.MatchString(c.PipelineID) || !number.MatchString(c.JobID) || !commitSHA.MatchString(c.CommitSHA) {
		return errors.New("GitLab job identity differs from trusted server/project policy or has invalid identifiers")
	}
	if !c.ProtectedRef || c.Branch == "" || c.Branch != c.DefaultBranch || c.PipelineSource != "push" && c.PipelineSource != "web" {
		return errors.New("planning/execution requires the protected default branch in a push or manually started pipeline")
	}
	if c.JobToken == "" || c.ReadToken == "" || !identity.MatchString(c.TargetEnvironmentID) || pingone.RegionDomains[c.TargetRegion] == "" || !identity.MatchString(c.Environment) {
		return errors.New("protected GitLab tokens, environment name and trusted PingOne target ID/region are required")
	}
	return nil
}
func baseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || strings.Contains(u.Path, "//") {
		return nil, errors.New("trusted GitLab URL must be HTTPS without credentials, query, encoding or fragment")
	}
	for _, p := range strings.Split(u.Path, "/") {
		if p == "." || p == ".." {
			return nil, errors.New("invalid GitLab server path")
		}
	}
	return u, nil
}

type Verifier struct {
	Config Config
	HTTP   *http.Client
}

func (v *Verifier) Origin() gitops.Origin {
	return gitops.Origin{ServerURL: strings.TrimRight(v.Config.BaseURL, "/"), ProjectID: v.Config.ProjectID, PipelineID: v.Config.PipelineID, CommitSHA: v.Config.CommitSHA}
}
func (v *Verifier) get(ctx context.Context, path string, jobToken bool, out any) (http.Header, error) {
	base, err := baseURL(v.Config.BaseURL)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(path, "/") || strings.Contains(path, "://") {
		return nil, errors.New("invalid verifier API path")
	}
	endpoint := strings.TrimRight(base.String(), "/") + "/api/v4/" + path
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, errors.New("invalid verifier request")
	}
	if jobToken {
		req.Header.Set("JOB-TOKEN", v.Config.JobToken)
	} else {
		req.Header.Set("PRIVATE-TOKEN", v.Config.ReadToken)
	}
	req.Header.Set("Accept", "application/json")
	client := v.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("GitLab verifier redirects are forbidden")
	}
	response, err := copy.Do(req)
	if err != nil {
		return nil, errors.New("could not verify live GitLab authorization; execution is blocked")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("GitLab authorization API returned HTTP %d; execution is blocked", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if err != nil || len(raw) > 4<<20 {
		return nil, errors.New("GitLab authorization response unavailable or too large")
	}
	if json.Unmarshal(raw, out) != nil {
		return nil, errors.New("GitLab authorization API returned an unknown JSON shape")
	}
	return response.Header, nil
}

type job struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Ref    string `json:"ref"`
	Tag    bool   `json:"tag"`
	Commit struct {
		ID string `json:"id"`
	} `json:"commit"`
	Pipeline struct {
		ID        int64  `json:"id"`
		ProjectID int64  `json:"project_id"`
		SHA       string `json:"sha"`
		Ref       string `json:"ref"`
	} `json:"pipeline"`
}

func (v *Verifier) verify(ctx context.Context, r gitops.Request) (job, error) {
	var j job
	if err := v.Config.validate(); err != nil {
		return j, err
	}
	if err := gitops.Validate(r); err != nil {
		return j, err
	}
	c := v.Config
	if r.Target.ID != c.TargetEnvironmentID || r.Target.Region != c.TargetRegion {
		return j, errors.New("request destination differs from protected runner target policy")
	}
	if _, err := v.get(ctx, "job", true, &j); err != nil {
		return j, err
	}
	if strconv.FormatInt(j.ID, 10) != c.JobID || j.Status != "running" || j.Name == "" || j.Tag || j.Ref != c.Branch || j.Commit.ID != c.CommitSHA || strconv.FormatInt(j.Pipeline.ID, 10) != c.PipelineID || strconv.FormatInt(j.Pipeline.ProjectID, 10) != c.ProjectID || j.Pipeline.SHA != c.CommitSHA || j.Pipeline.Ref != c.Branch {
		return j, errors.New("live GitLab job token does not match the running protected-branch job")
	}
	var project struct {
		ID            int64  `json:"id"`
		Visibility    string `json:"visibility"`
		DefaultBranch string `json:"default_branch"`
		Archived      bool   `json:"archived"`
	}
	if _, err := v.get(ctx, "projects/"+c.ProjectID, false, &project); err != nil {
		return j, err
	}
	if strconv.FormatInt(project.ID, 10) != c.ProjectID || project.Visibility != "private" || project.Archived || project.DefaultBranch != c.DefaultBranch {
		return j, errors.New("live GitLab project is not the expected active private project")
	}
	var branch struct {
		Name      string `json:"name"`
		Protected bool   `json:"protected"`
	}
	if _, err := v.get(ctx, "projects/"+c.ProjectID+"/repository/branches/"+url.PathEscape(c.Branch), false, &branch); err != nil {
		return j, err
	}
	if branch.Name != c.DefaultBranch || !branch.Protected {
		return j, errors.New("the live GitLab default branch is not protected")
	}
	return j, nil
}
func (v *Verifier) VerifyPlan(ctx context.Context, r gitops.Request) error {
	_, err := v.verify(ctx, r)
	return err
}
func (v *Verifier) list(ctx context.Context, path string) ([]json.RawMessage, error) {
	var result []json.RawMessage
	page := "1"
	seen := map[string]bool{}
	expected := -1
	for page != "" {
		if !number.MatchString(page) || seen[page] || len(seen) >= 1000 {
			return nil, errors.New("invalid/cyclic GitLab authorization pagination")
		}
		seen[page] = true
		var rows []json.RawMessage
		h, err := v.get(ctx, path+"&per_page=100&page="+page, false, &rows)
		if err != nil {
			return nil, err
		}
		if rows == nil {
			return nil, errors.New("GitLab authorization collection is missing")
		}
		result = append(result, rows...)
		if total := h.Get("X-Total"); total != "" {
			n, e := strconv.Atoi(total)
			if e != nil || n < 0 {
				return nil, errors.New("invalid GitLab result count")
			}
			expected = max(expected, n)
		}
		page = h.Get("X-Next-Page")
	}
	if expected >= 0 && len(result) < expected {
		return nil, errors.New("incomplete GitLab authorization collection")
	}
	return result, nil
}
func (v *Verifier) Authorize(ctx context.Context, a gitops.Artifact) error {
	if err := a.Validate(); err != nil {
		return err
	}
	if a.Origin != v.Origin() {
		return errors.New("plan artifact belongs to another GitLab server, project, pipeline or commit")
	}
	j, err := v.verify(ctx, a.Request)
	if err != nil {
		return err
	}
	c := v.Config
	jobs, err := v.list(ctx, "projects/"+c.ProjectID+"/pipelines/"+c.PipelineID+"/jobs?include_retried=true")
	if err != nil {
		return err
	}
	found := false
	for _, raw := range jobs {
		var prior job
		if json.Unmarshal(raw, &prior) != nil {
			return errors.New("invalid pipeline job metadata")
		}
		if prior.ID == j.ID {
			found = true
		}
		if prior.Name == j.Name && prior.ID != j.ID {
			return errors.New("retry/replay of a deployment job is blocked; reconcile native status and prepare a new reviewed plan")
		}
	}
	if !found {
		return errors.New("current job is absent from the live pipeline history")
	}
	var protection struct {
		Name     string `json:"name"`
		Required int    `json:"required_approval_count"`
		Rules    []struct {
			Required int `json:"required_approvals"`
		} `json:"approval_rules"`
	}
	if _, err = v.get(ctx, "projects/"+c.ProjectID+"/protected_environments/"+url.PathEscape(c.Environment), false, &protection); err != nil {
		return err
	}
	ruleTotal := 0
	for _, rule := range protection.Rules {
		if rule.Required < 0 {
			return errors.New("invalid protected environment approval rule")
		}
		ruleTotal += rule.Required
	}
	required := max(protection.Required, ruleTotal)
	if protection.Name != c.Environment || required < 1 {
		return errors.New("protected deployment environment must require at least one approval")
	}
	deployments, err := v.list(ctx, "projects/"+c.ProjectID+"/deployments?environment="+url.QueryEscape(c.Environment)+"&status=running&order_by=id&sort=desc")
	if err != nil {
		return err
	}
	deploymentID := int64(0)
	for _, raw := range deployments {
		var d struct {
			ID         int64 `json:"id"`
			Deployable struct {
				ID int64 `json:"id"`
			} `json:"deployable"`
		}
		if json.Unmarshal(raw, &d) != nil {
			return errors.New("invalid deployment metadata")
		}
		if d.Deployable.ID == j.ID {
			if d.ID < 1 || deploymentID != 0 {
				return errors.New("ambiguous deployment for the running job")
			}
			deploymentID = d.ID
		}
	}
	if deploymentID == 0 {
		return errors.New("no running deployment is associated with this job; environment action:start is required")
	}
	var deployment struct {
		ID          int64  `json:"id"`
		Status      string `json:"status"`
		SHA         string `json:"sha"`
		Ref         string `json:"ref"`
		Environment struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"environment"`
		Deployable job `json:"deployable"`
		Summary    struct {
			Rules []struct {
				Required  int `json:"required_approvals"`
				Approvals []struct {
					Status string `json:"status"`
					User   struct {
						ID int64 `json:"id"`
					} `json:"user"`
				} `json:"deployment_approvals"`
			} `json:"rules"`
		} `json:"approval_summary"`
	}
	if _, err = v.get(ctx, "projects/"+c.ProjectID+"/deployments/"+strconv.FormatInt(deploymentID, 10), false, &deployment); err != nil {
		return err
	}
	if deployment.ID != deploymentID || deployment.Status != "running" || deployment.SHA != c.CommitSHA || deployment.Ref != c.Branch || deployment.Environment.ID < 1 || deployment.Environment.Name != c.Environment || deployment.Deployable.ID != j.ID || strconv.FormatInt(deployment.Deployable.Pipeline.ID, 10) != c.PipelineID || deployment.Deployable.Pipeline.SHA != c.CommitSHA {
		return errors.New("live deployment identity does not match the approved artifact/job/environment")
	}
	totalRequired := 0
	allApprovers := map[int64]bool{}
	for _, rule := range deployment.Summary.Rules {
		if rule.Required < 0 {
			return errors.New("invalid deployment approval rule")
		}
		approved := map[int64]bool{}
		for _, approval := range rule.Approvals {
			if approval.Status == "approved" && approval.User.ID > 0 {
				approved[approval.User.ID] = true
				allApprovers[approval.User.ID] = true
			}
		}
		if len(approved) < rule.Required {
			return errors.New("deployment approval rule is not satisfied")
		}
		totalRequired += rule.Required
	}
	if totalRequired < required || len(allApprovers) < totalRequired {
		return errors.New("deployment approval summary is missing or insufficient; configure approval rules supported by this GitLab instance")
	}
	return nil
}
