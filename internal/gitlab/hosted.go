package gitlab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// This file holds the calls the hosted server needs and a CI job does not:
// a job starts with the repository checked out on an existing request
// branch, the server starts with neither.

// CreateBranch creates name from ref (a branch name).
func (c *Client) CreateBranch(ctx context.Context, name, ref string) error {
	if !validBranchName(name) || !validBranchName(ref) {
		return errors.New("branch name is invalid")
	}
	if err := c.Do(ctx, http.MethodPost, c.project()+"/repository/branches", map[string]string{"branch": name, "ref": ref}, nil); err != nil {
		return fmt.Errorf("create branch %q: %w", name, err)
	}
	return nil
}

// DeleteBranch deletes name. A branch that is already gone is not an error.
func (c *Client) DeleteBranch(ctx context.Context, name string) error {
	if !validBranchName(name) {
		return errors.New("branch name is invalid")
	}
	err := c.Do(ctx, http.MethodDelete, c.project()+"/repository/branches/"+url.PathEscape(name), nil, nil)
	if err != nil && !IsNotFound(err) {
		return fmt.Errorf("delete branch %q: %w", name, err)
	}
	return nil
}

// snapshotMax bounds a snapshot: Terraform roots are small.
const (
	snapshotMaxFiles = 2000
	snapshotMaxBytes = 64 << 20
)

// Snapshot copies the Terraform files of branch into an existing, empty,
// private caller-owned directory and returns their immutable commit. It
// never removes the supplied directory. The caller owns its cleanup.
// It reads the repository root and the
// directories in dirs (not recursive), and only *.tf, *.tfvars, *.yml and
// *.yaml files: everything the request engine reads. All reads use the
// captured commit ID, so branch movement cannot mix repository versions.
func (c *Client) Snapshot(ctx context.Context, branch, dir string, dirs []string) (head string, err error) {
	fi, err := os.Lstat(dir)
	if err != nil || !filepath.IsAbs(dir) || !fi.IsDir() || fi.Mode().Perm()&0o077 != 0 {
		return "", errors.New("snapshot destination must be an existing private directory")
	}
	contents, err := os.ReadDir(dir)
	if err != nil || len(contents) != 0 {
		return "", errors.New("snapshot destination must be empty")
	}
	readDirs := []string{""}
	seenDirs := map[string]bool{"": true}
	for _, d := range dirs {
		if d != "" && !validRepoPath(d) {
			return "", errors.New("snapshot directory path is invalid")
		}
		if !seenDirs[d] {
			seenDirs[d] = true
			readDirs = append(readDirs, d)
		}
	}
	want := func(name string) bool {
		// Repository CI and dot files are deliberately outside the engine's
		// snapshot. Validate paths only after these known omissions, so a
		// normal .gitlab-ci.yml does not make the repository unreadable.
		if strings.HasPrefix(name, ".") || isCIFile(name) || strings.EqualFold(name, "gitlab-ci.yml") {
			return false
		}
		for _, ext := range []string{".tf", ".tfvars", ".yml", ".yaml"} {
			if strings.HasSuffix(name, ext) {
				return true
			}
		}
		return false
	}
	b, ok, err := c.GetBranch(ctx, branch)
	if err != nil {
		return "", err
	}
	if !ok || !fullCommitIDRE.MatchString(b.Commit.ID) {
		return "", errors.New("snapshot branch has no valid commit")
	}
	files, size := 0, 0
	for _, d := range readDirs {
		wantDir := d
		if wantDir == "" {
			wantDir = "."
		}
		entries, err := c.ListTree(ctx, b.Commit.ID, d)
		if err != nil {
			return "", err
		}
		for _, e := range entries {
			if e.Type != "blob" || !want(e.Name) {
				continue
			}
			if !validRepoPath(e.Path) || filepath.ToSlash(filepath.Dir(e.Path)) != wantDir {
				return "", errors.New("GitLab returned an unexpected snapshot path")
			}
			f, err := c.GetFile(ctx, b.Commit.ID, e.Path)
			if err != nil {
				return "", err
			}
			if !f.Exists {
				return "", errors.New("a file is missing from the immutable snapshot")
			}
			files++
			size += len(f.Content)
			if files > snapshotMaxFiles || size > snapshotMaxBytes {
				return "", errors.New("the repository has too many or too large Terraform files to read")
			}
			full := filepath.Join(dir, filepath.FromSlash(e.Path))
			if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
				return "", err
			}
			out, err := os.OpenFile(full, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return "", err
			}
			_, writeErr := out.Write(f.Content)
			closeErr := out.Close()
			if writeErr != nil {
				return "", writeErr
			}
			if closeErr != nil {
				return "", closeErr
			}
		}
	}
	return b.Commit.ID, nil
}

var fullCommitIDRE = regexp.MustCompile(`^[0-9a-f]{40}$`)

// HostedCommitter commits a request for the hosted server. The request
// branch must not exist yet. Its creation and the reviewed files are sent
// in one commit request rooted at ExpectedHead. Uncertain writes are never
// cleaned up destructively; a retry reconciles the request trailer.
type HostedCommitter struct {
	S             *Submitter
	DefaultBranch string
}

// CommitToBranch implements the request engine's Committer.
func (h HostedCommitter) CommitToBranch(ctx context.Context, bc BranchCommit) (Result, error) {
	if h.S == nil || h.S.Client == nil {
		return Result{}, &PreflightError{Err: errors.New("GitLab committer is not configured")}
	}
	if bc.DeleteRequestFile || !fullCommitIDRE.MatchString(bc.ExpectedHead) {
		return Result{}, &PreflightError{Err: errors.New("hosted requests require a full snapshot commit and no CI request file")}
	}
	if err := h.S.prepareBranchCommit(ctx, &bc); err != nil {
		return Result{}, &PreflightError{Err: err}
	}
	c := h.S.Client
	project, err := c.VerifyProject(ctx)
	if err != nil {
		return Result{}, &PreflightError{Err: err}
	}
	if h.DefaultBranch != "" && h.DefaultBranch != project.DefaultBranch || bc.Branch == project.DefaultBranch {
		return Result{}, &PreflightError{Err: errors.New("the configured default branch changed or is a request branch")}
	}
	b, exists, err := c.GetBranch(ctx, bc.Branch)
	if err != nil {
		return Result{}, &PreflightError{Err: err}
	}
	if exists {
		if carriesRequest(b.Commit.Message, bc.RequestID) {
			mr, err := c.OpenMergeRequest(ctx, bc.Branch)
			if err != nil {
				return Result{}, &PartialError{Branch: bc.Branch, CommitID: b.Commit.ID, Stage: "merge_request_unconfirmed", Resumable: true}
			}
			res := Result{Branch: bc.Branch, CommitID: b.Commit.ID, MR: mr, Resumed: true}
			if bc.Mode == ModeMR && mr == nil {
				res.MR, err = h.S.openMR(ctx, project, ChangeSet{Branch: bc.Branch, Title: bc.Title, Description: bc.Description, Labels: bc.Labels})
				if err != nil {
					return Result{}, &PartialError{Branch: bc.Branch, CommitID: b.Commit.ID, Stage: "merge_request_failed", Resumable: true}
				}
			}
			return Result{}, &AlreadyCommittedError{Result: res}
		}
		return Result{}, &ConflictError{Paths: []string{bc.Branch}, Reason: "the request branch already exists without this request's commit"}
	}
	actions := make([]map[string]string, 0, len(bc.Files))
	for _, f := range bc.Files {
		cur, err := c.GetFile(ctx, bc.ExpectedHead, f.Path)
		if err != nil {
			return Result{}, &PreflightError{Err: err}
		}
		a := map[string]string{"action": "create", "file_path": f.Path, "content": string(f.Content)}
		if cur.Exists {
			a["action"], a["last_commit_id"] = "update", cur.LastCommitID
		}
		actions = append(actions, a)
	}
	base, ok, err := c.GetBranch(ctx, project.DefaultBranch)
	if err != nil {
		return Result{}, &PreflightError{Err: err}
	}
	if !ok || base.Commit.ID != bc.ExpectedHead {
		return Result{}, &ConflictError{Paths: []string{project.DefaultBranch}, Reason: "the default branch changed after this request was previewed; preview again"}
	}
	// GitLab does not atomically compare the default branch here. A later
	// move cannot change the new branch's immutable reviewed parent.
	payload := map[string]any{"branch": bc.Branch, "start_sha": bc.ExpectedHead, "force": false,
		"commit_message": withTrailer(bc.CommitMessage, RequestTrailer+bc.RequestID), "actions": actions}
	var created struct {
		ID string `json:"id"`
	}
	if err := c.Do(ctx, http.MethodPost, c.project()+"/repository/commits", payload, &created); err != nil {
		var he *HTTPError
		if errors.As(err, &he) && he.Conflict {
			return Result{}, &ConflictError{Paths: []string{bc.Branch}, Reason: "GitLab rejected the new request branch because it already exists or a file changed"}
		}
		return Result{}, &PartialError{Branch: bc.Branch, Stage: "commit_unconfirmed", Resumable: true}
	}
	if !fullCommitIDRE.MatchString(created.ID) {
		return Result{}, &PartialError{Branch: bc.Branch, Stage: "commit_unconfirmed", Resumable: true}
	}
	res := Result{Branch: bc.Branch, CommitID: created.ID}
	if bc.Mode == ModeMR {
		res.MR, err = h.S.openMR(ctx, project, ChangeSet{Branch: bc.Branch, Title: bc.Title, Description: bc.Description, Labels: bc.Labels})
		if err != nil {
			return Result{}, &PartialError{Branch: bc.Branch, CommitID: created.ID, Stage: "merge_request_failed", Resumable: true}
		}
	}
	return res, nil
}

// OpenMergeRequest implements the request engine's Committer.
func (h HostedCommitter) OpenMergeRequest(ctx context.Context, branch string) (*MergeRequest, error) {
	return h.S.Client.OpenMergeRequest(ctx, branch)
}
