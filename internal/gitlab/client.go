package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

const maxResponse = 2 << 20

type Client struct {
	Config Config
	Token  string
	HTTP   *http.Client
}
type Project struct {
	ID            int64  `json:"id"`
	DefaultBranch string `json:"default_branch"`
	WebURL        string `json:"web_url"`
}
type MergeRequest struct {
	ID     int64  `json:"id"`
	IID    int64  `json:"iid"`
	WebURL string `json:"web_url"`
	Branch string `json:"-"`
}

// SubmissionPreflightError means Submit failed before it attempted any write.
// Callers may offer an explicit retry after the preflight problem is corrected.
type SubmissionPreflightError struct {
	Err error
}

func (e *SubmissionPreflightError) Error() string {
	return "GitLab submission preflight failed; no GitLab writes occurred: " + e.Err.Error()
}

func (e *SubmissionPreflightError) Unwrap() error { return e.Err }

func NewClient(config Config, token string) (*Client, error) {
	config = config.withDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("GitLab token is required")
	}
	return &Client{Config: config, Token: token, HTTP: &http.Client{Timeout: 45 * time.Second}}, nil
}

func parseBase(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("GitLab base URL is invalid")
	}
	if u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("GitLab base URL must be HTTPS without credentials, query, or fragment")
	}
	return u, nil
}
func urlPathUnescape(s string) (string, error) { return url.PathUnescape(s) }

func (c *Client) apiURL(rel string) (*url.URL, error) {
	if rel == "" || strings.HasPrefix(rel, "/") || strings.Contains(rel, "\\") || strings.ContainsAny(rel, "?#") {
		return nil, errors.New("invalid GitLab API path")
	}
	decoded, err := url.PathUnescape(rel)
	if err != nil || strings.Contains(decoded, "\\") {
		return nil, errors.New("invalid GitLab API path")
	}
	for _, part := range strings.Split(decoded, "/") {
		if part == "." || part == ".." {
			return nil, errors.New("invalid GitLab API path")
		}
	}
	base, err := parseBase(c.Config.BaseURL)
	if err != nil {
		return nil, err
	}
	prefix := strings.TrimRight(base.EscapedPath(), "/") + "/api/v4/"
	base.Path = strings.TrimRight(base.Path, "/") + "/api/v4/" + decoded
	base.RawPath = prefix + rel
	return base, nil
}

func (c *Client) Do(ctx context.Context, method, path string, body, out any) error {
	u, err := c.apiURL(path)
	if err != nil {
		return err
	}
	var reader io.Reader
	if body != nil {
		b, e := json.Marshal(body)
		if e != nil {
			return errors.New("could not encode GitLab request")
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return errors.New("could not create GitLab request")
	}
	req.Header.Set("PRIVATE-TOKEN", c.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	h := c.HTTP
	if h == nil {
		h = &http.Client{Timeout: 45 * time.Second}
	}
	clone := *h
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("GitLab redirects are not allowed") }
	res, err := clone.Do(req)
	if err != nil {
		return errors.New("GitLab request failed; verify the server URL and network")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("GitLab returned HTTP %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxResponse+1))
	if err != nil || len(data) > maxResponse {
		return errors.New("GitLab response was invalid or too large")
	}
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err = json.Unmarshal(data, out); err != nil {
		return errors.New("GitLab returned invalid JSON")
	}
	return nil
}

func (c *Client) VerifyProject(ctx context.Context) (Project, error) {
	var raw struct {
		ID            int64  `json:"id"`
		Visibility    string `json:"visibility"`
		Archived      bool   `json:"archived"`
		DefaultBranch string `json:"default_branch"`
		WebURL        string `json:"web_url"`
	}
	if err := c.Do(ctx, http.MethodGet, "projects/"+c.Config.ProjectID, nil, &raw); err != nil {
		return Project{}, fmt.Errorf("verify GitLab project: %w", err)
	}
	if raw.Visibility != "private" || raw.Archived || strings.TrimSpace(raw.DefaultBranch) == "" {
		return Project{}, errors.New("GitLab project must be private, active, and have a default branch")
	}
	expectedID, parseErr := strconv.ParseInt(c.Config.ProjectID, 10, 64)
	if parseErr != nil || raw.ID != expectedID {
		return Project{}, errors.New("GitLab returned a different project than configured")
	}
	base, _ := parseBase(c.Config.BaseURL)
	projectURL, urlErr := url.Parse(raw.WebURL)
	if urlErr != nil || !sameGitLabLocation(base, projectURL) {
		return Project{}, errors.New("GitLab returned a project URL outside the configured server path")
	}
	var branch struct {
		Name      string `json:"name"`
		Protected bool   `json:"protected"`
	}
	if err := c.Do(ctx, http.MethodGet, "projects/"+c.Config.ProjectID+"/repository/branches/"+url.PathEscape(raw.DefaultBranch), nil, &branch); err != nil {
		return Project{}, fmt.Errorf("verify GitLab default branch: %w", err)
	}
	if branch.Name != raw.DefaultBranch || !branch.Protected {
		return Project{}, errors.New("GitLab default branch must be protected")
	}
	return Project{ID: raw.ID, DefaultBranch: raw.DefaultBranch, WebURL: raw.WebURL}, nil
}

func (c *Client) Submit(ctx context.Context, id string, files map[string]string, title, description string) (MergeRequest, error) {
	if !validName(id) {
		return MergeRequest{}, errors.New("request ID is invalid")
	}
	if len(files) == 0 {
		return MergeRequest{}, errors.New("at least one request file is required")
	}
	for name := range files {
		if !validFileName(name) {
			return MergeRequest{}, fmt.Errorf("commit request files: unsafe filename %q", name)
		}
	}
	project, err := c.VerifyProject(ctx)
	if err != nil {
		return MergeRequest{}, &SubmissionPreflightError{Err: err}
	}
	branch := "promotion/" + id
	if err = c.Do(ctx, http.MethodPost, "projects/"+c.Config.ProjectID+"/repository/branches", map[string]string{"branch": branch, "ref": project.DefaultBranch}, nil); err != nil {
		return MergeRequest{}, fmt.Errorf("create promotion branch %q: %w", branch, err)
	}
	actions := make([]map[string]string, 0, len(files))
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		actions = append(actions, map[string]string{"action": "create", "file_path": "requests/" + id + "/" + name, "content": files[name]})
	}
	commit := map[string]any{"branch": branch, "start_branch": project.DefaultBranch, "commit_message": "Add promotion request " + id, "actions": actions}
	if err = c.Do(ctx, http.MethodPost, "projects/"+c.Config.ProjectID+"/repository/commits", commit, nil); err != nil {
		return MergeRequest{}, fmt.Errorf("commit promotion request on branch %q: %w", branch, err)
	}
	payload := map[string]any{"source_branch": branch, "target_branch": project.DefaultBranch, "title": title, "description": description, "remove_source_branch": true, "squash": false}
	var mr MergeRequest
	if err = c.Do(ctx, http.MethodPost, "projects/"+c.Config.ProjectID+"/merge_requests", payload, &mr); err != nil {
		return MergeRequest{}, fmt.Errorf("create merge request for branch %q: %w", branch, err)
	}
	if mr.ID < 1 || mr.IID < 1 {
		return MergeRequest{}, errors.New("GitLab returned an incomplete merge request identity")
	}
	base, _ := parseBase(c.Config.BaseURL)
	link, e := url.Parse(mr.WebURL)
	if e != nil || !sameGitLabLocation(base, link) {
		return MergeRequest{}, errors.New("GitLab returned a merge request URL outside the configured server path")
	}
	mr.Branch = branch
	return mr, nil
}
func validFileName(n string) bool {
	return n != "" && path.Base(n) == n && n != ".gitlab-ci.yml" && n != "." && n != ".." && !strings.ContainsAny(n, "\\\x00")
}
func sameGitLabLocation(base, u *url.URL) bool {
	if base == nil || u == nil || u.Scheme != "https" || !strings.EqualFold(u.Host, base.Host) || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	bp := strings.TrimRight(base.Path, "/")
	for _, part := range strings.Split(u.Path, "/") {
		if part == "." || part == ".." {
			return false
		}
	}
	return u.Path == bp || strings.HasPrefix(u.Path, bp+"/")
}
