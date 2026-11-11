package gitlab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// This file holds the read-only calls of the hosted server's Requests page:
// request branches, their merge requests and the rollout of a merged
// request (the default-branch pipeline jobs of its merge commit). Nothing
// here writes to GitLab.
//
// GitLab API assumptions (verify against the real instance; see
// docs/acceptance/hosted-server.md):
//   - GET /projects/:id/repository/branches?search=^PREFIX returns branches
//     whose name starts with PREFIX ("^" anchors the search).
//   - GET /projects/:id/merge_requests?state=all&labels=L returns merge
//     requests of every state with merge_commit_sha set once merged
//     (squash_commit_sha when squashed).
//   - GET /projects/:id/pipelines?sha=SHA&ref=BRANCH lists the pipelines of a
//     commit on a branch, newest first with order_by=id&sort=desc.
//   - GET /projects/:id/pipelines/:pipeline_id/jobs lists the latest try of
//     each job (retried jobs are left out by default).

// maxStatusPages bounds every paged listing below.
const maxStatusPages = 5

// RequestMR is a merge request as the Requests page shows it.
type RequestMR struct {
	IID             int64  `json:"iid"`
	Title           string `json:"title"`
	State           string `json:"state"` // opened, merged, closed, locked
	SourceBranch    string `json:"source_branch"`
	WebURL          string `json:"web_url"`
	MergeCommitSHA  string `json:"merge_commit_sha"`
	SquashCommitSHA string `json:"squash_commit_sha"`
	UpdatedAt       string `json:"updated_at"`
}

// MergedCommit is the commit a merged MR put on the default branch.
func (m RequestMR) MergedCommit() string {
	if m.SquashCommitSHA != "" {
		return m.SquashCommitSHA
	}
	return m.MergeCommitSHA
}

// Pipeline is a CI pipeline.
type Pipeline struct {
	ID     int64  `json:"id"`
	SHA    string `json:"sha"`
	Ref    string `json:"ref"`
	Status string `json:"status"`
	WebURL string `json:"web_url"`
}

// Job is one pipeline job.
type Job struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"` // created, pending, running, success, failed, canceled, skipped, manual, scheduled, ...
	WebURL string `json:"web_url"`
}

func (c *Client) inside(link string) bool {
	if link == "" {
		return true
	}
	base, err := parseBase(c.Config.BaseURL)
	if err != nil {
		return false
	}
	u, err := url.Parse(link)
	return err == nil && sameGitLabLocation(base, u)
}

// ListRequestBranches lists branches whose name starts with prefix.
func (c *Client) ListRequestBranches(ctx context.Context, prefix string) ([]Branch, error) {
	if !ValidRequestBranchPrefix(prefix) {
		return nil, errors.New("the request branch prefix is invalid")
	}
	var out []Branch
	for page := 1; page <= maxStatusPages; page++ {
		q := url.Values{"search": {"^" + prefix}, "per_page": {"100"}, "page": {strconv.Itoa(page)}}
		var items []Branch
		if err := c.DoQuery(ctx, http.MethodGet, c.project()+"/repository/branches", q, nil, &items); err != nil {
			return nil, fmt.Errorf("list request branches: %w", err)
		}
		for _, b := range items {
			if strings.HasPrefix(b.Name, prefix) && validBranchName(b.Name) {
				out = append(out, b)
			}
		}
		if len(items) < 100 {
			break
		}
	}
	return out, nil
}

// ListRequestMRs lists merge requests of every state that carry label and
// come from a branch starting with prefix, most recently updated first.
func (c *Client) ListRequestMRs(ctx context.Context, label, prefix string, limit int) ([]RequestMR, error) {
	if label == "" || strings.ContainsAny(label, ",\n") || !ValidRequestBranchPrefix(prefix) {
		return nil, errors.New("invalid merge request label or branch prefix")
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	q := url.Values{"state": {"all"}, "labels": {label}, "order_by": {"updated_at"}, "sort": {"desc"}, "per_page": {strconv.Itoa(limit)}}
	var items []RequestMR
	if err := c.DoQuery(ctx, http.MethodGet, c.project()+"/merge_requests", q, nil, &items); err != nil {
		return nil, fmt.Errorf("list merge requests: %w", err)
	}
	var out []RequestMR
	for _, m := range items {
		if !strings.HasPrefix(m.SourceBranch, prefix) || !c.inside(m.WebURL) {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

// CommitPipeline returns the newest pipeline of sha on ref, or nil.
func (c *Client) CommitPipeline(ctx context.Context, sha, ref string) (*Pipeline, error) {
	if !commitIDRE.MatchString(sha) || !validBranchName(ref) {
		return nil, errors.New("invalid commit or branch")
	}
	q := url.Values{"sha": {sha}, "ref": {ref}, "order_by": {"id"}, "sort": {"desc"}, "per_page": {"1"}}
	var items []Pipeline
	if err := c.DoQuery(ctx, http.MethodGet, c.project()+"/pipelines", q, nil, &items); err != nil {
		return nil, fmt.Errorf("list pipelines: %w", err)
	}
	if len(items) == 0 {
		return nil, nil
	}
	p := items[0]
	if p.SHA != sha || !c.inside(p.WebURL) {
		return nil, errors.New("GitLab returned an unexpected pipeline")
	}
	return &p, nil
}

// PipelineJobs lists the jobs of a pipeline.
func (c *Client) PipelineJobs(ctx context.Context, pipelineID int64) ([]Job, error) {
	if pipelineID < 1 {
		return nil, errors.New("invalid pipeline")
	}
	var out []Job
	for page := 1; page <= maxStatusPages; page++ {
		q := url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}}
		var items []Job
		if err := c.DoQuery(ctx, http.MethodGet, c.project()+"/pipelines/"+strconv.FormatInt(pipelineID, 10)+"/jobs", q, nil, &items); err != nil {
			return nil, fmt.Errorf("list pipeline jobs: %w", err)
		}
		for _, j := range items {
			if !c.inside(j.WebURL) {
				j.WebURL = ""
			}
			out = append(out, j)
		}
		if len(items) < 100 {
			break
		}
	}
	return out, nil
}
