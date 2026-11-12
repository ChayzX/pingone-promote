package gitlab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// This file holds the calls the hosted server needs and a CI job does not:
// a job starts with the repository checked out on an existing request
// branch, the server starts with neither.

// CreateBranch creates name from ref (a branch name or a commit SHA).
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

// snapshotLimits bound a snapshot: Terraform roots are small.
type snapshotLimits struct {
	Files int // files written
	Bytes int // total decoded bytes
}

var defaultSnapshotLimits = snapshotLimits{Files: 2000, Bytes: 64 << 20}

// snapshotExts are the files the request engine reads.
var snapshotExts = []string{".tf", ".tfvars", ".yml", ".yaml"}

// Snapshot copies the Terraform files of branch, as of ONE commit, into a
// new private directory and returns that directory and the commit SHA.
//
// Snapshot creates the directory itself (os.MkdirTemp, mode 0700, under
// the system temporary directory); it never writes into or deletes a
// directory the caller names. The caller owns the returned directory and
// removes it when done. On error nothing is left behind and dir is "".
//
// The branch head is resolved first; the tree and every file are then read
// with ref=<that SHA>, so a push during the read cannot mix versions.
// Only the repository root and the directories in dirs (not recursive) are
// read, only *.tf, *.tfvars, *.yml and *.yaml files directly inside them,
// and never a dot path. The number of files and their total size are
// bounded.
func (c *Client) Snapshot(ctx context.Context, branch string, dirs []string) (dir, head string, err error) {
	return c.snapshot(ctx, branch, dirs, defaultSnapshotLimits)
}

func (c *Client) snapshot(ctx context.Context, branch string, dirs []string, lim snapshotLimits) (dir, head string, err error) {
	want := map[string]bool{"": true}
	order := []string{""}
	for _, d := range dirs {
		if !validRepoPath(d) || isCIFile(d) {
			return "", "", fmt.Errorf("snapshot directory %q is not allowed", d)
		}
		if !want[d] {
			want[d] = true
			order = append(order, d)
		}
	}
	b, ok, err := c.GetBranch(ctx, branch)
	if err != nil {
		return "", "", err
	}
	if !ok {
		return "", "", fmt.Errorf("branch %q does not exist", branch)
	}
	head = b.Commit.ID
	if !commitIDRE.MatchString(head) || len(head) < 40 {
		return "", "", errors.New("GitLab returned an invalid branch head")
	}
	own, mkErr := os.MkdirTemp("", "pingone-promote-snapshot-")
	if mkErr != nil {
		return "", "", errors.New("cannot create a private snapshot directory")
	}
	defer func() {
		if err != nil {
			os.RemoveAll(own) // our own directory, created above
			dir, head = "", ""
		}
	}()
	if err := os.Chmod(own, 0o700); err != nil {
		return "", "", err
	}
	files, size := 0, 0
	for _, d := range order {
		entries, err := c.ListTree(ctx, head, d)
		if err != nil {
			return "", "", err
		}
		for _, e := range entries {
			if e.Type != "blob" || !hasSnapshotExt(e.Name) || strings.HasPrefix(e.Name, ".") || isCIFile(e.Name) {
				continue // not read: other files, dot files, CI files
			}
			// The entry must be directly inside the directory listed.
			if strings.Contains(e.Name, "/") || e.Path != path.Join(d, e.Name) || !validRepoPath(e.Path) || isCIFile(e.Path) {
				return "", "", fmt.Errorf("GitLab returned an unexpected tree entry in %q", d)
			}
			if files++; files > lim.Files {
				return "", "", errors.New("the repository has too many Terraform files to read")
			}
			f, err := c.GetFile(ctx, head, e.Path)
			if err != nil {
				return "", "", err
			}
			if !f.Exists {
				return "", "", fmt.Errorf("%s disappeared from commit %s while it was read", e.Path, short(head))
			}
			if size += len(f.Content); size > lim.Bytes {
				return "", "", errors.New("the repository's Terraform files are too large to read")
			}
			full := filepath.Join(own, filepath.FromSlash(e.Path))
			if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
				return "", "", err
			}
			out, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return "", "", err
			}
			_, werr := out.Write(f.Content)
			if cerr := out.Close(); werr == nil {
				werr = cerr
			}
			if werr != nil {
				return "", "", werr
			}
		}
	}
	return own, head, nil
}

func hasSnapshotExt(name string) bool {
	for _, ext := range snapshotExts {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}
	return false
}

// HostedCommitter commits a request for the hosted server.
//
// The request branch is created from ExpectedHead, the default-branch SHA
// the request was previewed at, never from the moving default branch name,
// so the commit is rooted at the reviewed SHA. If the default branch has
// moved since then, nothing is committed and a *ConflictError asks for
// another preview. The GitLab API cannot make this atomic: a push to the
// default branch after the check still leaves the request rooted at the
// reviewed SHA, and the merge request shows the divergence.
//
// A branch is deleted only when this call created it, the failure is
// certain to have committed nothing, and the branch head is still the start
// commit. A timeout, a transport failure or a 5xx on the commit call is
// uncertain: the branch is kept and a *PartialError (stage
// "commit_uncertain") names it for reconciliation. A branch that existed
// before is never deleted.
type HostedCommitter struct {
	S             *Submitter
	DefaultBranch string
	// BranchPrefix is the configured request namespace (empty means
	// RequestBranchPrefix). Branches are always BranchPrefix + request ID.
	BranchPrefix string
	// BranchPattern, when set, is the rule for the branch name the person
	// types instead of BranchPrefix + request ID. Such a branch must not
	// exist yet, unless it already carries this request's commit.
	BranchPattern *regexp.Regexp
}

func (h HostedCommitter) prefix() string {
	if h.BranchPrefix != "" {
		return h.BranchPrefix
	}
	return RequestBranchPrefix
}

// CommitToBranch implements the request engine's Committer.
func (h HostedCommitter) CommitToBranch(ctx context.Context, bc BranchCommit) (Result, error) {
	c := h.S.Client
	prefix := h.prefix()
	switch {
	case h.BranchPattern != nil:
		if !validName(bc.RequestID) || !validBranchName(bc.Branch) || !h.BranchPattern.MatchString(bc.Branch) {
			return Result{}, &PreflightError{Err: errors.New("the branch name does not match the configured branch pattern")}
		}
	case !ValidRequestBranchPrefix(prefix):
		return Result{}, &PreflightError{Err: errors.New("the configured request branch prefix is invalid")}
	case bc.BranchPrefix != "" && bc.BranchPrefix != prefix:
		return Result{}, &PreflightError{Err: errors.New("the request uses a branch prefix other than the configured one")}
	case !validName(bc.RequestID) || bc.Branch != prefix+bc.RequestID || !validBranchName(bc.Branch):
		return Result{}, &PreflightError{Err: errors.New("request branches must be named " + prefix + "<request ID>")}
	}
	switch {
	case !validBranchName(h.DefaultBranch) || bc.Branch == h.DefaultBranch:
		return Result{}, &PreflightError{Err: errors.New("the default branch is never a request branch")}
	case !commitIDRE.MatchString(bc.ExpectedHead) || (len(bc.ExpectedHead) != 40 && len(bc.ExpectedHead) != 64):
		// A full SHA only: GitLab reports full IDs, so an abbreviated one
		// would never match the new branch head and the branch this call
		// created would be left behind.
		return Result{}, &PreflightError{Err: errors.New("the previewed default-branch commit must be a full SHA")}
	}
	bc.BranchPrefix, bc.BranchPattern = prefix, h.BranchPattern

	b, exists, err := c.GetBranch(ctx, bc.Branch)
	if err != nil {
		return Result{}, &PreflightError{Err: err}
	}
	if exists && b.Protected {
		// A protected branch under the request prefix is not one this
		// server made: never commit to it.
		return Result{}, &PreflightError{Err: errors.New("the request branch " + bc.Branch + " exists and is protected; it is not a request branch this server can write")}
	}
	if exists && h.BranchPattern != nil && !carriesRequest(b.Commit.Message, bc.RequestID) {
		// A typed name must never write into someone else's branch.
		return Result{}, &PreflightError{Err: errors.New("the branch " + bc.Branch + " already exists; type a branch name that is not used yet")}
	}
	created := false
	if !exists {
		if err := c.CreateBranch(ctx, bc.Branch, bc.ExpectedHead); err != nil {
			var he *HTTPError
			if errors.As(err, &he) && he.Status >= 400 && he.Status < 500 && he.Status != http.StatusRequestTimeout && he.Status != http.StatusTooManyRequests {
				// GitLab refused: no branch was created by this call (an
				// "already exists" answer means someone else created it).
				return Result{}, &PreflightError{Err: err}
			}
			return Result{}, &PartialError{Branch: bc.Branch, Stage: "branch_unconfirmed", Resumable: true}
		}
		created = true
		if b, exists, err = c.GetBranch(ctx, bc.Branch); err != nil || !exists {
			return Result{}, &PartialError{Branch: bc.Branch, Stage: "branch_unconfirmed", Resumable: true}
		}
		if b.Commit.ID != bc.ExpectedHead {
			// Someone pushed to the new branch already: keep it.
			return Result{}, &ConflictError{Paths: []string{bc.Branch}, Reason: "the new request branch moved before the commit; it was kept for review"}
		}
	}
	if carriesRequest(b.Commit.Message, bc.RequestID) {
		// Already committed by an earlier attempt: the submitter reports
		// it (and ensures the merge request) without committing again.
		return h.S.CommitToBranch(ctx, bc)
	}
	if b.Commit.ID != bc.ExpectedHead {
		return Result{}, &ConflictError{Paths: []string{bc.Branch}, Reason: "the request branch is not at the previewed commit; preview again"}
	}
	def, ok, err := c.GetBranch(ctx, h.DefaultBranch)
	if err != nil || !ok {
		h.cleanup(ctx, created, bc)
		return Result{}, &PreflightError{Err: errors.New("cannot read the default branch")}
	}
	if def.Commit.ID != bc.ExpectedHead {
		h.cleanup(ctx, created, bc)
		return Result{}, &ConflictError{Paths: []string{h.DefaultBranch}, Reason: "the default branch changed after this request was previewed; preview again"}
	}
	res, err := h.S.CommitToBranch(ctx, bc)
	if err == nil {
		return res, nil
	}
	if nothingCommitted(err) {
		h.cleanup(ctx, created, bc)
		return res, err
	}
	var partial *PartialError
	var done *AlreadyCommittedError
	if errors.As(err, &partial) || errors.As(err, &done) {
		return res, err
	}
	// Timeout, transport failure or 5xx on the commit call: GitLab may have
	// applied it. Keep the branch for reconciliation.
	return Result{}, &PartialError{Branch: bc.Branch, Stage: "commit_uncertain", Resumable: true}
}

// nothingCommitted reports whether a Submitter.CommitToBranch error is
// certain to have written nothing: a preflight failure, a conflict (found
// before the commit or reported by GitLab), or GitLab refusing the commit
// with a definite 4xx answer.
func nothingCommitted(err error) bool {
	var pre *PreflightError
	var conflict *ConflictError
	if errors.As(err, &pre) || errors.As(err, &conflict) {
		return true
	}
	var partial *PartialError
	var done *AlreadyCommittedError
	if errors.As(err, &partial) || errors.As(err, &done) {
		return false
	}
	var he *HTTPError
	return errors.As(err, &he) && he.Status >= 400 && he.Status < 500 && he.Status != http.StatusRequestTimeout && he.Status != http.StatusTooManyRequests
}

// cleanup removes a branch this call created, only while its head is still
// the start commit (nothing of anyone's was committed on it). Deletion is
// best effort: a leftover empty branch is harmless and resumable.
func (h HostedCommitter) cleanup(ctx context.Context, created bool, bc BranchCommit) {
	if !created || bc.Branch == h.DefaultBranch || bc.Branch != h.prefix()+bc.RequestID {
		return
	}
	c := h.S.Client
	b, ok, err := c.GetBranch(ctx, bc.Branch)
	if err != nil || !ok || b.Commit.ID != bc.ExpectedHead {
		return
	}
	_ = c.DeleteBranch(ctx, bc.Branch)
}

// OpenMergeRequest implements the request engine's Committer.
func (h HostedCommitter) OpenMergeRequest(ctx context.Context, branch string) (*MergeRequest, error) {
	return h.S.Client.OpenMergeRequest(ctx, branch)
}
