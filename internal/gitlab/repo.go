package gitlab

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// HTTPError is a non-2xx GitLab response. Its text is only the status, never
// response content. Conflict is set when GitLab reports that a file changed
// since last_commit_id, or that a file or branch already exists.
type HTTPError struct {
	Status   int
	Conflict bool
}

func (e *HTTPError) Error() string { return fmt.Sprintf("GitLab returned HTTP %d", e.Status) }

// IsNotFound reports whether err is a GitLab 404.
func IsNotFound(err error) bool {
	var he *HTTPError
	return errors.As(err, &he) && he.Status == http.StatusNotFound
}

func isConflict(status int, body []byte) bool {
	if status == http.StatusConflict {
		return true
	}
	if status != http.StatusBadRequest {
		return false
	}
	var e struct {
		Message any `json:"message"`
	}
	_ = json.Unmarshal(body, &e)
	msg := strings.ToLower(fmt.Sprint(e.Message))
	for _, marker := range []string{"has changed since", "already exists", "does not exist", "doesn't exist", "sorry, we cannot revert", "conflict"} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

func (c *Client) project() string { return "projects/" + c.Config.ProjectID }

// RepoFile is a file read from a ref.
type RepoFile struct {
	Path         string
	Content      []byte
	BlobID       string
	LastCommitID string
	Exists       bool
}

// maxFile bounds a decoded repository file. A whole environment's
// DaVinci flows file passes 20 MB; it is hclblocks.MaxFileSize.
const maxFile = MaxFileSize

// MaxFileSize is the largest file read from or committed to GitLab.
const MaxFileSize = 64 << 20

// GetFile reads path at ref. A missing file returns Exists == false and no
// error.
func (c *Client) GetFile(ctx context.Context, ref, path string) (RepoFile, error) {
	if !validRepoPath(path) || !validBranchName(ref) {
		return RepoFile{}, errors.New("invalid repository path or ref")
	}
	var raw struct {
		FilePath     string `json:"file_path"`
		Encoding     string `json:"encoding"`
		Content      string `json:"content"`
		BlobID       string `json:"blob_id"`
		LastCommitID string `json:"last_commit_id"`
	}
	err := c.doQuery(ctx, http.MethodGet, c.project()+"/repository/files/"+url.PathEscape(path), url.Values{"ref": {ref}}, nil, &raw, maxFile/3*4+(1<<20))
	if IsNotFound(err) {
		return RepoFile{Path: path}, nil
	}
	if err != nil {
		return RepoFile{}, fmt.Errorf("read %s: %w", path, err)
	}
	if raw.FilePath != path || raw.Encoding != "base64" {
		return RepoFile{}, errors.New("GitLab returned an unexpected file")
	}
	content, err := base64.StdEncoding.DecodeString(raw.Content)
	if err != nil || len(content) > maxFile {
		return RepoFile{}, errors.New("GitLab returned an invalid or oversized file")
	}
	return RepoFile{Path: path, Content: content, BlobID: raw.BlobID, LastCommitID: raw.LastCommitID, Exists: true}, nil
}

// TreeEntry is one repository tree item.
type TreeEntry struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Type string `json:"type"` // blob or tree
}

// ListTree lists one directory at ref (not recursive), following pages.
func (c *Client) ListTree(ctx context.Context, ref, dir string) ([]TreeEntry, error) {
	if dir != "" && !validRepoPath(dir) || !validBranchName(ref) {
		return nil, errors.New("invalid repository path or ref")
	}
	var all []TreeEntry
	for page := 1; page <= 50; page++ {
		q := url.Values{"ref": {ref}, "per_page": {"100"}, "page": {strconv.Itoa(page)}}
		if dir != "" {
			q.Set("path", dir)
		}
		var items []TreeEntry
		err := c.DoQuery(ctx, http.MethodGet, c.project()+"/repository/tree", q, nil, &items)
		if IsNotFound(err) && page == 1 {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", dir, err)
		}
		all = append(all, items...)
		if len(items) < 100 {
			return all, nil
		}
	}
	return nil, errors.New("repository directory has too many entries")
}

// Branch is a repository branch.
type Branch struct {
	Name      string `json:"name"`
	Protected bool   `json:"protected"`
	Commit    struct {
		ID      string `json:"id"`
		Message string `json:"message"`
	} `json:"commit"`
}

// GetBranch returns the branch, or ok == false when it does not exist.
func (c *Client) GetBranch(ctx context.Context, name string) (Branch, bool, error) {
	if !validBranchName(name) {
		return Branch{}, false, errors.New("branch name is invalid")
	}
	var b Branch
	err := c.Do(ctx, http.MethodGet, c.project()+"/repository/branches/"+url.PathEscape(name), nil, &b)
	if IsNotFound(err) {
		return Branch{}, false, nil
	}
	if err != nil {
		return Branch{}, false, fmt.Errorf("read branch %q: %w", name, err)
	}
	if b.Name != name {
		return Branch{}, false, errors.New("GitLab returned a different branch")
	}
	return b, true, nil
}

// OpenMergeRequest returns an open merge request from branch, if any.
func (c *Client) OpenMergeRequest(ctx context.Context, branch string) (*MergeRequest, error) {
	var mrs []MergeRequest
	q := url.Values{"source_branch": {branch}, "state": {"opened"}}
	if err := c.DoQuery(ctx, http.MethodGet, c.project()+"/merge_requests", q, nil, &mrs); err != nil {
		return nil, fmt.Errorf("look up merge requests: %w", err)
	}
	if len(mrs) == 0 {
		return nil, nil
	}
	mr := mrs[0]
	mr.Branch = branch
	return &mr, nil
}

// validRepoPath accepts a relative, normalized path without traversal,
// backslashes, control characters or a leading dot segment.
func validRepoPath(p string) bool {
	if p == "" || len(p) > 512 || strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") || strings.ContainsAny(p, "\\\x00") {
		return false
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." || strings.HasPrefix(part, ".") {
			return false
		}
	}
	return true
}
