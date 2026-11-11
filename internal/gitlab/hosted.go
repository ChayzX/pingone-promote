package gitlab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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

// Snapshot copies the Terraform files of branch into dir and returns the
// branch head they were read at. It reads the repository root and the
// directories in dirs (not recursive), and only *.tf, *.tfvars, *.yml and
// *.yaml files: everything the request engine reads. The head is checked
// before and after, so a push during the read is retried, not mixed in.
func (c *Client) Snapshot(ctx context.Context, branch, dir string, dirs []string) (head string, err error) {
	want := func(name string) bool {
		for _, ext := range []string{".tf", ".tfvars", ".yml", ".yaml"} {
			if strings.HasSuffix(name, ext) {
				return true
			}
		}
		return false
	}
	for attempt := 0; attempt < 3; attempt++ {
		b, ok, err := c.GetBranch(ctx, branch)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("branch %q does not exist", branch)
		}
		if err := os.RemoveAll(dir); err != nil {
			return "", err
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", err
		}
		files, size := 0, 0
		for _, d := range append([]string{""}, dirs...) {
			entries, err := c.ListTree(ctx, branch, d)
			if err != nil {
				return "", err
			}
			for _, e := range entries {
				if e.Type != "blob" || !want(e.Name) || !validRepoPath(e.Path) {
					continue
				}
				f, err := c.GetFile(ctx, branch, e.Path)
				if err != nil {
					return "", err
				}
				if !f.Exists {
					continue
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
				if err := os.WriteFile(full, f.Content, 0o600); err != nil {
					return "", err
				}
			}
		}
		after, ok, err := c.GetBranch(ctx, branch)
		if err != nil {
			return "", err
		}
		if ok && after.Commit.ID == b.Commit.ID {
			return b.Commit.ID, nil
		}
	}
	return "", errors.New("the branch kept changing while it was read; try again")
}

// HostedCommitter commits a request for the hosted server. The request
// branch does not exist yet, so it is created from the default branch
// first. If the default branch has moved since the files were read
// (ExpectedHead), nothing is committed and the new branch is removed.
type HostedCommitter struct {
	S             *Submitter
	DefaultBranch string
}

// CommitToBranch implements the request engine's Committer.
func (h HostedCommitter) CommitToBranch(ctx context.Context, bc BranchCommit) (Result, error) {
	c := h.S.Client
	b, exists, err := c.GetBranch(ctx, bc.Branch)
	if err != nil {
		return Result{}, &PreflightError{Err: err}
	}
	created := false
	if !exists {
		if err := c.CreateBranch(ctx, bc.Branch, h.DefaultBranch); err != nil {
			return Result{}, &PreflightError{Err: err}
		}
		created = true
		if b, exists, err = c.GetBranch(ctx, bc.Branch); err != nil || !exists {
			return Result{}, &PreflightError{Err: errors.New("the new request branch could not be read back")}
		}
	}
	if b.Commit.ID != bc.ExpectedHead && !carriesRequest(b.Commit.Message, bc.RequestID) {
		if created {
			_ = c.DeleteBranch(ctx, bc.Branch)
		}
		return Result{}, &ConflictError{Paths: []string{h.DefaultBranch}, Reason: "the default branch changed after this request was previewed; preview again"}
	}
	res, err := h.S.CommitToBranch(ctx, bc)
	if err != nil && created {
		var partial *PartialError
		var done *AlreadyCommittedError
		if !errors.As(err, &partial) && !errors.As(err, &done) {
			// Nothing was committed: do not leave an empty branch behind.
			_ = c.DeleteBranch(ctx, bc.Branch)
		}
	}
	return res, err
}

// OpenMergeRequest implements the request engine's Committer.
func (h HostedCommitter) OpenMergeRequest(ctx context.Context, branch string) (*MergeRequest, error) {
	return h.S.Client.OpenMergeRequest(ctx, branch)
}
