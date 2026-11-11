package server

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"

	"pingone-promote/internal/gitlab"
	"pingone-promote/internal/promote"
)

type Snapshot struct {
	Head, DefaultBranch, ProjectURL, Workspace string
	Close                                      func() error
}
type RequestRecord struct{ ID, Branch, Commit, Title, State, URL string }
type RolloutRecord struct{ Environment, Job, Status, URL string }

const (
	repositoryPageSize    = 100
	repositoryMaxPages    = 10
	repositoryRecordLimit = repositoryPageSize * repositoryMaxPages
)

// Repository is the hosted service's only durable storage boundary.
type Repository interface {
	Snapshot(context.Context, string, promote.Layout) (Snapshot, error)
	Publish(context.Context, string, string, gitlab.BranchCommit) (gitlab.Result, error)
	Requests(context.Context) ([]RequestRecord, error)
	Rollout(context.Context, string) ([]RolloutRecord, error)
}
type gitLabRepository struct {
	client    *gitlab.Client
	cfg       Config
	terraform string
}

func (g *gitLabRepository) Snapshot(ctx context.Context, branch string, l promote.Layout) (Snapshot, error) {
	p, err := g.client.VerifyProject(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	if branch == "" {
		branch = p.DefaultBranch
	}
	dir, err := os.MkdirTemp("", "pingone-promote-snapshot-")
	if err != nil {
		return Snapshot{}, err
	}
	cleanup := func() error { return os.RemoveAll(dir) }
	dirs := []string{l.ModuleDir}
	overlayDir := path.Dir(l.OverlayPattern)
	if overlayDir != "." {
		dirs = append(dirs, overlayDir)
	}
	secretDir := path.Dir(l.CustomerSecretsFile)
	if secretDir != "." && secretDir != overlayDir {
		dirs = append(dirs, secretDir)
	}
	head, err := g.client.Snapshot(ctx, branch, dir, dirs)
	if err != nil {
		_ = cleanup()
		return Snapshot{}, err
	}
	return Snapshot{Head: head, Workspace: dir, DefaultBranch: p.DefaultBranch, ProjectURL: p.WebURL, Close: cleanup}, nil
}
func (g *gitLabRepository) Publish(ctx context.Context, head, branch string, bc gitlab.BranchCommit) (gitlab.Result, error) {
	if head == "" || bc.ExpectedHead != head || bc.Branch != branch {
		return gitlab.Result{}, errors.New("publication does not match preview")
	}
	p, err := g.client.VerifyProject(ctx)
	if err != nil {
		return gitlab.Result{}, err
	}
	h := gitlab.HostedCommitter{S: &gitlab.Submitter{Client: g.client, Formatter: g.terraform}, DefaultBranch: p.DefaultBranch}
	return h.CommitToBranch(ctx, bc)
}
func (g *gitLabRepository) project() string { return "projects/" + g.client.Config.ProjectID }
func (g *gitLabRepository) safeURL(raw string) string {
	u, e := url.Parse(raw)
	base, be := url.Parse(g.client.Config.BaseURL)
	if e != nil || be != nil || u.User != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, base.Host) || !strings.HasPrefix(u.Path, strings.TrimRight(base.Path, "/")+"/") {
		return ""
	}
	return u.String()
}
func (g *gitLabRepository) Requests(ctx context.Context) ([]RequestRecord, error) {
	out := []RequestRecord{}
	byBranch := map[string]bool{}
	prefix := g.cfg.BranchPrefix
	for page := 1; page <= repositoryMaxPages; page++ {
		var mrs []struct {
			IID             int64  `json:"iid"`
			Title           string `json:"title"`
			SourceBranch    string `json:"source_branch"`
			State           string `json:"state"`
			SHA             string `json:"sha"`
			MergeCommitSHA  string `json:"merge_commit_sha"`
			SquashCommitSHA string `json:"squash_commit_sha"`
			WebURL          string `json:"web_url"`
		}
		q := url.Values{"per_page": {strconv.Itoa(repositoryPageSize)}, "page": {strconv.Itoa(page)}, "order_by": {"updated_at"}, "sort": {"desc"}}
		if err := g.client.DoQuery(ctx, http.MethodGet, g.project()+"/merge_requests", q, nil, &mrs); err != nil {
			return nil, err
		}
		for _, m := range mrs {
			if !strings.HasPrefix(m.SourceBranch, prefix) {
				continue
			}
			id := strings.TrimPrefix(m.SourceBranch, prefix)
			if !validRequestID(id) {
				continue
			}
			commit := m.SHA
			if m.State == "merged" {
				commit = m.MergeCommitSHA
				if commit == "" {
					commit = m.SquashCommitSHA
				}
			}
			out = append(out, RequestRecord{ID: id, Branch: m.SourceBranch, Commit: commit, Title: m.Title, State: m.State, URL: g.safeURL(m.WebURL)})
			byBranch[m.SourceBranch] = true
		}
		if len(mrs) < repositoryPageSize {
			break
		}
	}
	for page := 1; page <= repositoryMaxPages; page++ {
		var branches []gitlab.Branch
		q := url.Values{"per_page": {strconv.Itoa(repositoryPageSize)}, "page": {strconv.Itoa(page)}, "search": {prefix}}
		if err := g.client.DoQuery(ctx, http.MethodGet, g.project()+"/repository/branches", q, nil, &branches); err != nil {
			return nil, err
		}
		for _, b := range branches {
			if !strings.HasPrefix(b.Name, prefix) || byBranch[b.Name] {
				continue
			}
			id := strings.TrimPrefix(b.Name, prefix)
			if !validRequestID(id) || !strings.Contains(b.Commit.Message, gitlab.RequestTrailer+id) {
				continue
			}
			out = append(out, RequestRecord{ID: id, Branch: b.Name, Commit: b.Commit.ID, Title: strings.SplitN(b.Commit.Message, "\n", 2)[0], State: "branch"})
		}
		if len(branches) < repositoryPageSize {
			return out, nil
		}
	}
	return out, nil
}
func (g *gitLabRepository) Rollout(ctx context.Context, commit string) ([]RolloutRecord, error) {
	out := []RolloutRecord{}
	for _, env := range g.cfg.Rollout {
		out = append(out, RolloutRecord{Environment: env, Job: strings.ReplaceAll(g.cfg.ApplyJobPattern, "{env}", env), Status: "unknown"})
	}
	if !commitSHA.MatchString(commit) {
		return out, nil
	}
	p, err := g.client.VerifyProject(ctx)
	if err != nil {
		return nil, err
	}
	var pipelines []struct {
		ID  int64  `json:"id"`
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	}
	q := url.Values{"sha": {commit}, "ref": {p.DefaultBranch}, "per_page": {strconv.Itoa(repositoryPageSize)}, "order_by": {"id"}, "sort": {"desc"}}
	if err := g.client.DoQuery(ctx, http.MethodGet, g.project()+"/pipelines", q, nil, &pipelines); err != nil {
		return nil, err
	}
	var id int64
	for _, v := range pipelines {
		if v.SHA == commit && v.Ref == p.DefaultBranch {
			id = v.ID
			break
		}
	}
	if id == 0 {
		return out, nil
	}
	for page := 1; page <= repositoryMaxPages; page++ {
		var jobs []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
			WebURL string `json:"web_url"`
		}
		q := url.Values{"per_page": {strconv.Itoa(repositoryPageSize)}, "page": {strconv.Itoa(page)}}
		if err := g.client.DoQuery(ctx, http.MethodGet, g.project()+"/pipelines/"+strconv.FormatInt(id, 10)+"/jobs", q, nil, &jobs); err != nil {
			return nil, err
		}
		for _, j := range jobs {
			for i := range out {
				if out[i].Job == j.Name && out[i].Status == "unknown" {
					out[i].Status = j.Status
					out[i].URL = g.safeURL(j.WebURL)
				}
			}
		}
		if len(jobs) < repositoryPageSize {
			return out, nil
		}
	}
	return out, nil
}
