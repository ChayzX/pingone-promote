package gitlab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"pingone-promote/internal/overlay"
	"pingone-promote/internal/pipelinesecrets"
	"pingone-promote/internal/tffmt"
)

// Mode selects how a change set is delivered.
type Mode string

const (
	ModeMR     Mode = "mr"     // push a branch and open a merge request (default)
	ModeBranch Mode = "branch" // push a named branch only
)

// SubmissionTrailer marks commits made by pingone-promote so an interrupted
// submission can be recognized and resumed.
const SubmissionTrailer = "PingOne-Promote-Submission: "

// PathPolicy lists the only repository paths a submission may write. All are
// configurable; CI configuration is never writable.
type PathPolicy struct {
	EnvironmentsDir     string // tfvars live at <dir>/<env>.tfvars
	SecretsTFPath       string // the root stack's secrets.tf
	CustomerSecretsPath string // the pipeline's customer_secrets.yml
}

// DefaultPathPolicy matches the reference layout.
var DefaultPathPolicy = PathPolicy{EnvironmentsDir: "environments", SecretsTFPath: "secrets.tf", CustomerSecretsPath: "customer_secrets.yml"}

var envFileRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,62}\.tfvars$`)

// FileKind is what a permitted path holds.
type FileKind string

const (
	FileTfvars          FileKind = "tfvars"
	FileSecretsTF       FileKind = "secrets_tf"
	FileCustomerSecrets FileKind = "customer_secrets"
)

// Classify returns the kind of a permitted path, or an error for any other
// path.
func (p PathPolicy) Classify(file string) (FileKind, error) {
	if !validRepoPath(file) || isCIFile(file) {
		return "", fmt.Errorf("path %q is not writable", file)
	}
	switch {
	case file == p.SecretsTFPath:
		return FileSecretsTF, nil
	case file == p.CustomerSecretsPath:
		return FileCustomerSecrets, nil
	case path.Dir(file) == p.EnvironmentsDir && envFileRE.MatchString(path.Base(file)):
		return FileTfvars, nil
	}
	return "", fmt.Errorf("path %q is outside the submission allow-list", file)
}

// Validate checks the policy itself.
func (p PathPolicy) Validate() error {
	for _, f := range []string{p.SecretsTFPath, p.CustomerSecretsPath} {
		if !validRepoPath(f) || isCIFile(f) {
			return fmt.Errorf("submission path %q is invalid", f)
		}
	}
	if !validRepoPath(p.EnvironmentsDir) {
		return errors.New("environments directory is invalid")
	}
	if p.SecretsTFPath == p.CustomerSecretsPath {
		return errors.New("secrets.tf and customer_secrets.yml paths must differ")
	}
	return nil
}

// EnvironmentPath is the tfvars path for env.
func (p PathPolicy) EnvironmentPath(env string) (string, error) {
	f := p.EnvironmentsDir + "/" + env + ".tfvars"
	if _, err := p.Classify(f); err != nil {
		return "", err
	}
	return f, nil
}

func isCIFile(f string) bool {
	base := strings.ToLower(path.Base(f))
	return base == ".gitlab-ci.yml" || strings.HasSuffix(base, ".gitlab-ci.yml") || strings.HasSuffix(base, "gitlab-ci.yaml")
}

// File is one file of a change set.
type File struct {
	Path    string
	Content []byte
}

// ChangeSet is one submission: every file lands in ONE commit.
type ChangeSet struct {
	ID            string // submission ID, recorded as a commit trailer
	Mode          Mode
	Branch        string
	CommitMessage string
	Title         string
	Description   string
	Files         []File
	// Base holds the last_commit_id each path had when the draft was based;
	// "" means the file was absent. Paths not in Base are not checked
	// before commit (GitLab still enforces last_commit_id on update).
	Base map[string]string
	// Resume continues a submission whose branch already carries this ID.
	Resume bool
	// Labels are added to the merge request.
	Labels []string
}

// Result is a completed (or resumed) submission.
type Result struct {
	Branch   string        `json:"branch"`
	CommitID string        `json:"commitId"`
	MR       *MergeRequest `json:"mergeRequest,omitempty"`
	Resumed  bool          `json:"resumed,omitempty"`
}

// PreflightError means nothing was written; correcting the cause and
// retrying is safe.
type PreflightError struct{ Err error }

func (e *PreflightError) Error() string {
	return "GitLab submission preflight failed; no GitLab writes occurred: " + e.Err.Error()
}
func (e *PreflightError) Unwrap() error { return e.Err }

// Retryable is always true for a preflight failure.
func (e *PreflightError) Retryable() bool { return true }

// ConflictError means a file changed on the default branch since the draft
// was based (optimistic concurrency), or GitLab refused the commit for that
// reason. Nothing was written.
type ConflictError struct {
	Paths  []string
	Reason string
}

func (e *ConflictError) Error() string {
	return "GitLab files changed since the draft was based (" + strings.Join(e.Paths, ", ") + "): " + e.Reason + "; rebase the draft and submit again"
}

// PartialError means an earlier attempt left a branch, commit or merge request
// behind. Resumable submissions can be continued with ChangeSet.Resume.
type PartialError struct {
	Branch    string        `json:"branch"`
	CommitID  string        `json:"commitId,omitempty"`
	MR        *MergeRequest `json:"mergeRequest,omitempty"`
	Stage     string        `json:"stage"`
	Resumable bool          `json:"resumable"`
}

func (e *PartialError) Error() string {
	msg := "a previous submission attempt left branch " + e.Branch + " (" + e.Stage + ")"
	if e.Resumable {
		return msg + "; resume to continue it"
	}
	return msg + "; it was not created by this submission, so choose another branch name"
}

// Submitter delivers change sets to one project.
type Submitter struct {
	Client *Client
	Paths  PathPolicy
	// Formatter is the terraform or tofu binary; empty searches PATH.
	Formatter string
}

var commitMessageMax = 2000

func (s *Submitter) policy() PathPolicy {
	if s.Paths == (PathPolicy{}) {
		return DefaultPathPolicy
	}
	return s.Paths
}

// prepare validates and formats a change set without contacting GitLab.
func (s *Submitter) prepare(ctx context.Context, cs *ChangeSet) error {
	if !validName(cs.ID) {
		return errors.New("submission ID is invalid")
	}
	switch cs.Mode {
	case "":
		cs.Mode = ModeMR
	case ModeMR, ModeBranch:
	default:
		return errors.New("mode must be mr or branch")
	}
	if !validBranchName(cs.Branch) {
		return errors.New("branch name is invalid; use a safe Git branch name")
	}
	if strings.TrimSpace(cs.CommitMessage) == "" || len(cs.CommitMessage) > commitMessageMax || strings.ContainsRune(cs.CommitMessage, 0) || !utf8.ValidString(cs.CommitMessage) {
		return errors.New("commit message must contain text and be at most 2000 characters")
	}
	if cs.Mode == ModeMR && strings.TrimSpace(cs.Title) == "" {
		cs.Title = strings.SplitN(strings.TrimSpace(cs.CommitMessage), "\n", 2)[0]
	}
	if len(cs.Description) > 1<<20 || len(cs.Title) > 255 {
		return errors.New("merge request title or description is too long")
	}
	if err := s.policy().Validate(); err != nil {
		return err
	}
	if len(cs.Files) == 0 {
		return errors.New("a submission needs at least one file")
	}
	seen := map[string]bool{}
	for i, f := range cs.Files {
		kind, err := s.policy().Classify(f.Path)
		if err != nil {
			return err
		}
		if seen[f.Path] {
			return fmt.Errorf("path %q appears twice", f.Path)
		}
		seen[f.Path] = true
		if len(f.Content) > maxFile || !utf8.Valid(f.Content) {
			return fmt.Errorf("%s is too large or not UTF-8", f.Path)
		}
		switch kind {
		case FileTfvars:
			// Literal-only HCL attributes: true of v1 schema files and of v2
			// overlays. The v1 schema itself is validated by the Draft API.
			if _, err := overlay.ParseOverlay(f.Content, f.Path); err != nil {
				return fmt.Errorf("%s is not a valid tfvars file: %w", f.Path, err)
			}
			fallthrough
		case FileSecretsTF:
			// The real formatter is the gate: a missing binary or any
			// difference blocks the submission.
			out, err := tffmt.FormatWithTerraform(ctx, s.Formatter, f.Content)
			if err != nil {
				return fmt.Errorf("%s: %w", f.Path, err)
			}
			cs.Files[i].Content = out
		case FileCustomerSecrets:
			// YAML is not Terraform: it is checked by parsing instead.
			if _, err := pipelinesecrets.Parse(f.Content); err != nil {
				return fmt.Errorf("%s: %w", f.Path, err)
			}
		}
	}
	sort.Slice(cs.Files, func(i, j int) bool { return cs.Files[i].Path < cs.Files[j].Path })
	return nil
}

func trailer(id string) string { return SubmissionTrailer + id }

func carries(message, id string) bool {
	for _, line := range strings.Split(message, "\n") {
		if strings.TrimSpace(line) == trailer(id) {
			return true
		}
	}
	return false
}

// Submit writes the change set in one commit on a new branch created from
// the default branch, then opens a merge request in mr mode.
func (s *Submitter) Submit(ctx context.Context, cs ChangeSet) (Result, error) {
	if err := s.prepare(ctx, &cs); err != nil {
		return Result{}, &PreflightError{Err: err}
	}
	c := s.Client
	project, err := c.VerifyProject(ctx)
	if err != nil {
		return Result{}, &PreflightError{Err: err}
	}
	if cs.Branch == project.DefaultBranch {
		return Result{}, &PreflightError{Err: errors.New("the default branch cannot be a submission branch")}
	}
	branch, exists, err := c.GetBranch(ctx, cs.Branch)
	if err != nil {
		return Result{}, &PreflightError{Err: err}
	}
	if exists && branch.Protected {
		return Result{}, &PreflightError{Err: fmt.Errorf("branch %q is protected", cs.Branch)}
	}
	var mr *MergeRequest
	if cs.Mode == ModeMR {
		if mr, err = c.OpenMergeRequest(ctx, cs.Branch); err != nil {
			return Result{}, &PreflightError{Err: err}
		}
	}
	if exists {
		ours := carries(branch.Commit.Message, cs.ID)
		partial := &PartialError{Branch: cs.Branch, CommitID: branch.Commit.ID, MR: mr, Stage: "branch_exists", Resumable: ours}
		if !ours || !cs.Resume {
			return Result{}, partial
		}
		// Resume: the commit is in place; finish the merge request.
		res := Result{Branch: cs.Branch, CommitID: branch.Commit.ID, MR: mr, Resumed: true}
		if cs.Mode == ModeMR && mr == nil {
			if res.MR, err = s.openMR(ctx, project, cs); err != nil {
				return Result{}, &PartialError{Branch: cs.Branch, CommitID: branch.Commit.ID, Stage: "merge_request_failed", Resumable: true}
			}
		}
		return res, nil
	}
	if mr != nil {
		return Result{}, &PartialError{Branch: cs.Branch, MR: mr, Stage: "merge_request_exists", Resumable: false}
	}

	// Optimistic concurrency against the default branch.
	actions := make([]map[string]string, 0, len(cs.Files))
	var conflicts []string
	for _, f := range cs.Files {
		cur, err := c.GetFile(ctx, project.DefaultBranch, f.Path)
		if err != nil {
			return Result{}, &PreflightError{Err: err}
		}
		if base, checked := cs.Base[f.Path]; checked && base != cur.LastCommitID {
			conflicts = append(conflicts, f.Path)
			continue
		}
		a := map[string]string{"action": "create", "file_path": f.Path, "content": string(f.Content)}
		if cur.Exists {
			a["action"], a["last_commit_id"] = "update", cur.LastCommitID
		}
		actions = append(actions, a)
	}
	if len(conflicts) > 0 {
		return Result{}, &ConflictError{Paths: conflicts, Reason: "the default branch has newer versions"}
	}
	commit := map[string]any{
		"branch":         cs.Branch,
		"start_branch":   project.DefaultBranch,
		"commit_message": strings.TrimRight(cs.CommitMessage, "\n") + "\n\n" + trailer(cs.ID),
		"actions":        actions,
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := c.Do(ctx, http.MethodPost, c.project()+"/repository/commits", commit, &created); err != nil {
		var he *HTTPError
		if errors.As(err, &he) && he.Conflict {
			paths := make([]string, 0, len(cs.Files))
			for _, f := range cs.Files {
				paths = append(paths, f.Path)
			}
			return Result{}, &ConflictError{Paths: paths, Reason: "GitLab rejected the commit because a file changed"}
		}
		return Result{}, fmt.Errorf("commit change set: %w", err)
	}
	if created.ID == "" {
		return Result{}, &PartialError{Branch: cs.Branch, Stage: "commit_unconfirmed", Resumable: true}
	}
	res := Result{Branch: cs.Branch, CommitID: created.ID}
	if cs.Mode == ModeMR {
		if res.MR, err = s.openMR(ctx, project, cs); err != nil {
			return Result{}, &PartialError{Branch: cs.Branch, CommitID: created.ID, Stage: "merge_request_failed", Resumable: true}
		}
	}
	return res, nil
}

func (s *Submitter) openMR(ctx context.Context, project Project, cs ChangeSet) (*MergeRequest, error) {
	c := s.Client
	payload := map[string]any{"source_branch": cs.Branch, "target_branch": project.DefaultBranch, "title": cs.Title,
		"description": cs.Description, "remove_source_branch": true, "squash": false}
	if len(cs.Labels) > 0 {
		payload["labels"] = strings.Join(cs.Labels, ",")
	}
	var mr MergeRequest
	if err := c.Do(ctx, http.MethodPost, c.project()+"/merge_requests", payload, &mr); err != nil {
		return nil, err
	}
	if mr.ID < 1 || mr.IID < 1 {
		return nil, errors.New("GitLab returned an incomplete merge request identity")
	}
	base, _ := parseBase(c.Config.BaseURL)
	link, err := url.Parse(mr.WebURL)
	if err != nil || !sameGitLabLocation(base, link) {
		return nil, errors.New("GitLab returned a merge request URL outside the configured server path")
	}
	mr.Branch = cs.Branch
	return &mr, nil
}

// RevertRequest reverts an earlier submission commit.
type RevertRequest struct {
	ID          string // new submission ID for the revert
	CommitID    string // the submission commit to revert
	Paths       []string
	Mode        Mode
	Branch      string
	Title       string
	Description string
	// Confirm allows the revert although a path changed after CommitID.
	Confirm bool
}

var commitIDRE = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// Revert creates a branch from the default branch, applies a revert commit of
// req.CommitID on it, and opens a merge request in mr mode. It refuses when a
// path changed after the reverted commit unless req.Confirm is set.
func (s *Submitter) Revert(ctx context.Context, req RevertRequest) (Result, error) {
	if !validName(req.ID) || !commitIDRE.MatchString(req.CommitID) || !validBranchName(req.Branch) || len(req.Paths) == 0 {
		return Result{}, &PreflightError{Err: errors.New("revert needs a submission ID, commit ID, branch and paths")}
	}
	if req.Mode == "" {
		req.Mode = ModeMR
	}
	if req.Mode != ModeMR && req.Mode != ModeBranch {
		return Result{}, &PreflightError{Err: errors.New("mode must be mr or branch")}
	}
	for _, p := range req.Paths {
		if _, err := s.policy().Classify(p); err != nil {
			return Result{}, &PreflightError{Err: err}
		}
	}
	c := s.Client
	project, err := c.VerifyProject(ctx)
	if err != nil {
		return Result{}, &PreflightError{Err: err}
	}
	if req.Branch == project.DefaultBranch {
		return Result{}, &PreflightError{Err: errors.New("the default branch cannot be a revert branch")}
	}
	if _, exists, err := c.GetBranch(ctx, req.Branch); err != nil {
		return Result{}, &PreflightError{Err: err}
	} else if exists {
		return Result{}, &PartialError{Branch: req.Branch, Stage: "branch_exists", Resumable: false}
	}
	var changed []string
	for _, p := range req.Paths {
		cur, err := c.GetFile(ctx, project.DefaultBranch, p)
		if err != nil {
			return Result{}, &PreflightError{Err: err}
		}
		if cur.LastCommitID != req.CommitID {
			changed = append(changed, p)
		}
	}
	if len(changed) > 0 && !req.Confirm {
		return Result{}, &ConflictError{Paths: changed, Reason: "the files changed after the submission (or it is not merged yet); confirm to revert anyway"}
	}
	if err := c.Do(ctx, http.MethodPost, c.project()+"/repository/branches", map[string]string{"branch": req.Branch, "ref": project.DefaultBranch}, nil); err != nil {
		return Result{}, &PreflightError{Err: fmt.Errorf("create revert branch: %w", err)}
	}
	var reverted struct {
		ID string `json:"id"`
	}
	if err := c.Do(ctx, http.MethodPost, c.project()+"/repository/commits/"+req.CommitID+"/revert", map[string]string{"branch": req.Branch}, &reverted); err != nil || reverted.ID == "" {
		var he *HTTPError
		if errors.As(err, &he) && he.Conflict {
			return Result{}, &ConflictError{Paths: req.Paths, Reason: "GitLab cannot revert the commit cleanly; branch " + req.Branch + " was created without changes"}
		}
		return Result{}, &PartialError{Branch: req.Branch, Stage: "revert_failed", Resumable: false}
	}
	res := Result{Branch: req.Branch, CommitID: reverted.ID}
	if req.Mode == ModeMR {
		title := req.Title
		if title == "" {
			title = "Revert pingone-promote submission " + req.CommitID[:min(len(req.CommitID), 12)]
		}
		if res.MR, err = s.openMR(ctx, project, ChangeSet{Branch: req.Branch, Title: title, Description: req.Description}); err != nil {
			return Result{}, &PartialError{Branch: req.Branch, CommitID: reverted.ID, Stage: "merge_request_failed", Resumable: false}
		}
	}
	return res, nil
}
