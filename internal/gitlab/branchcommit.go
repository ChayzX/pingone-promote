package gitlab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"pingone-promote/internal/pipelinesecrets"
	"pingone-promote/internal/tfvars"
)

// RequestTrailer marks the commit a request job made (ADR 0017).
const RequestTrailer = "PingOne-Promote-Request: "

// RequestBranchPrefix is the only branch namespace a request job commits to.
const RequestBranchPrefix = "pingone-promote/"

// RequestFile is the request DaVinci commits; the job deletes it.
const RequestFile = ".pingone-promote/request.json"

// LayoutPolicy is the v2 allow-list (ADR 0013, revised): the shared
// definitions directory (*.tf directly inside it), named root Terraform
// files, environment overlays matching a pattern, and customer_secrets.yml.
// CI files and other dot paths are never writable; the request file may
// only be deleted.
type LayoutPolicy struct {
	ModuleDir           string   // e.g. definitions
	RootFiles           []string // e.g. main.tf, variables.tf, secrets.tf, imports.tf, versions.tf
	OverlayPattern      string   // e.g. {env}.tfvars
	CustomerSecretsPath string   // customer_secrets.yml
}

// Classify returns the kind of a permitted v2 path.
func (p LayoutPolicy) Classify(file string) (FileKind, error) {
	if !validRepoPath(file) || isCIFile(file) {
		return "", fmt.Errorf("path %q is not writable", file)
	}
	switch {
	case file == p.CustomerSecretsPath:
		return FileCustomerSecrets, nil
	case path.Dir(file) == p.ModuleDir && strings.HasSuffix(file, ".tf"):
		return FileSecretsTF, nil // a Terraform file: fmt gate
	case strings.HasSuffix(file, ".tf"):
		for _, r := range p.RootFiles {
			if file == r {
				return FileSecretsTF, nil
			}
		}
	case strings.HasSuffix(file, ".tfvars") && !strings.HasSuffix(file, ".auto.tfvars"):
		pre, post, ok := strings.Cut(p.OverlayPattern, "{env}")
		if ok && strings.HasPrefix(file, pre) && strings.HasSuffix(file, post) {
			env := strings.TrimSuffix(strings.TrimPrefix(file, pre), post)
			if safeEnvironment.MatchString(env) {
				return FileTfvars, nil
			}
		}
	}
	return "", fmt.Errorf("path %q is outside the submission allow-list", file)
}

// BranchCommit is one request job commit to an existing request branch.
type BranchCommit struct {
	RequestID string
	Branch    string
	// ExpectedHead is the commit the job read (CI_COMMIT_SHA). A different
	// branch head is a conflict: someone pushed in the meantime.
	ExpectedHead  string
	Mode          Mode
	CommitMessage string
	Title         string
	Description   string
	Files         []File
	// DeleteRequestFile removes .pingone-promote/request.json in the same
	// commit so the next pipeline does not run the job again.
	DeleteRequestFile bool
	Policy            LayoutPolicy
	// Formatter is the terraform or tofu binary for the fmt gate.
	Formatter string
}

// AlreadyCommittedError means the branch head already carries this
// request's trailer: the job ran before. Result describes that commit.
type AlreadyCommittedError struct{ Result Result }

func (e *AlreadyCommittedError) Error() string {
	return "the request branch already carries this request's commit " + e.Result.CommitID
}

func carriesRequest(message, id string) bool {
	for _, line := range strings.Split(message, "\n") {
		if strings.TrimSpace(line) == RequestTrailer+id {
			return true
		}
	}
	return false
}

func (s *Submitter) prepareBranchCommit(ctx context.Context, bc *BranchCommit) error {
	if !validName(bc.RequestID) {
		return errors.New("request ID is invalid")
	}
	switch bc.Mode {
	case "":
		bc.Mode = ModeMR
	case ModeMR, ModeBranch:
	default:
		return errors.New("mode must be mr or branch")
	}
	if !validBranchName(bc.Branch) || !strings.HasPrefix(bc.Branch, RequestBranchPrefix) {
		return errors.New("request jobs only commit to branches named " + RequestBranchPrefix + "<request>")
	}
	if !commitIDRE.MatchString(bc.ExpectedHead) {
		return errors.New("expected branch head must be a commit SHA")
	}
	if strings.TrimSpace(bc.CommitMessage) == "" || len(bc.CommitMessage) > commitMessageMax || !utf8.ValidString(bc.CommitMessage) || strings.ContainsRune(bc.CommitMessage, 0) {
		return errors.New("commit message must contain text and be at most 2000 characters")
	}
	if bc.Mode == ModeMR && strings.TrimSpace(bc.Title) == "" {
		bc.Title = strings.SplitN(strings.TrimSpace(bc.CommitMessage), "\n", 2)[0]
	}
	if len(bc.Description) > 1<<20 || len(bc.Title) > 255 {
		return errors.New("merge request title or description is too long")
	}
	if len(bc.Files) == 0 && !bc.DeleteRequestFile {
		return errors.New("nothing to commit")
	}
	seen := map[string]bool{}
	for i, f := range bc.Files {
		kind, err := bc.Policy.Classify(f.Path)
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
			if _, err := tfvars.ParseOverlay(f.Content, f.Path); err != nil {
				return err
			}
			fallthrough
		case FileSecretsTF:
			out, err := tfvars.FormatWithTerraform(ctx, s.Formatter, f.Content)
			if err != nil {
				return fmt.Errorf("%s: %w", f.Path, err)
			}
			bc.Files[i].Content = out
		case FileCustomerSecrets:
			if _, err := pipelinesecrets.Parse(f.Content); err != nil {
				return fmt.Errorf("%s: %w", f.Path, err)
			}
		}
	}
	sort.Slice(bc.Files, func(i, j int) bool { return bc.Files[i].Path < bc.Files[j].Path })
	return nil
}

// CommitToBranch writes a request's change set as ONE commit on the
// existing request branch (no start_branch), deleting the request file in
// the same commit, then opens a merge request in mr mode. It refuses the
// default branch, branches outside pingone-promote/, and a branch head
// other than ExpectedHead. A head that already carries this request's
// trailer returns *AlreadyCommittedError (after ensuring the MR in mr
// mode), so re-running the job never commits twice.
func (s *Submitter) CommitToBranch(ctx context.Context, bc BranchCommit) (Result, error) {
	if err := s.prepareBranchCommit(ctx, &bc); err != nil {
		return Result{}, &PreflightError{Err: err}
	}
	c := s.Client
	project, err := c.VerifyProject(ctx)
	if err != nil {
		return Result{}, &PreflightError{Err: err}
	}
	if bc.Branch == project.DefaultBranch {
		return Result{}, &PreflightError{Err: errors.New("the default branch is never a request branch")}
	}
	branch, exists, err := c.GetBranch(ctx, bc.Branch)
	if err != nil {
		return Result{}, &PreflightError{Err: err}
	}
	if !exists {
		return Result{}, &PreflightError{Err: fmt.Errorf("request branch %q does not exist", bc.Branch)}
	}
	// A protected pingone-promote/* wildcard is the recommended setup
	// (protected CI variables are then available to the job); any other
	// protected branch was refused above by the prefix rule.
	mr, err := c.OpenMergeRequest(ctx, bc.Branch)
	if err != nil {
		return Result{}, &PreflightError{Err: err}
	}
	if carriesRequest(branch.Commit.Message, bc.RequestID) {
		res := Result{Branch: bc.Branch, CommitID: branch.Commit.ID, MR: mr, Resumed: true}
		if bc.Mode == ModeMR && mr == nil {
			if res.MR, err = s.openMR(ctx, project, ChangeSet{Branch: bc.Branch, Title: bc.Title, Description: bc.Description}); err != nil {
				return Result{}, &PartialError{Branch: bc.Branch, CommitID: branch.Commit.ID, Stage: "merge_request_failed", Resumable: true}
			}
		}
		return Result{}, &AlreadyCommittedError{Result: res}
	}
	if branch.Commit.ID != bc.ExpectedHead {
		return Result{}, &ConflictError{Paths: []string{bc.Branch}, Reason: "the request branch moved after the job started (head " + short(branch.Commit.ID) + ", job read " + short(bc.ExpectedHead) + ")"}
	}
	actions := make([]map[string]string, 0, len(bc.Files)+1)
	for _, f := range bc.Files {
		cur, err := c.GetFile(ctx, bc.Branch, f.Path)
		if err != nil {
			return Result{}, &PreflightError{Err: err}
		}
		a := map[string]string{"action": "create", "file_path": f.Path, "content": string(f.Content)}
		if cur.Exists {
			a["action"], a["last_commit_id"] = "update", cur.LastCommitID
		}
		actions = append(actions, a)
	}
	if bc.DeleteRequestFile {
		actions = append(actions, map[string]string{"action": "delete", "file_path": RequestFile})
	}
	commit := map[string]any{
		"branch":         bc.Branch,
		"commit_message": strings.TrimRight(bc.CommitMessage, "\n") + "\n\n" + RequestTrailer + bc.RequestID,
		"actions":        actions,
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := c.Do(ctx, http.MethodPost, c.project()+"/repository/commits", commit, &created); err != nil {
		var he *HTTPError
		if errors.As(err, &he) && he.Conflict {
			paths := []string{}
			for _, f := range bc.Files {
				paths = append(paths, f.Path)
			}
			return Result{}, &ConflictError{Paths: paths, Reason: "GitLab rejected the commit because the branch or a file changed"}
		}
		return Result{}, fmt.Errorf("commit request: %w", err)
	}
	if created.ID == "" {
		return Result{}, &PartialError{Branch: bc.Branch, Stage: "commit_unconfirmed", Resumable: true}
	}
	res := Result{Branch: bc.Branch, CommitID: created.ID, MR: mr}
	if bc.Mode == ModeMR && mr == nil {
		if res.MR, err = s.openMR(ctx, project, ChangeSet{Branch: bc.Branch, Title: bc.Title, Description: bc.Description}); err != nil {
			return Result{}, &PartialError{Branch: bc.Branch, CommitID: created.ID, Stage: "merge_request_failed", Resumable: true}
		}
	}
	return res, nil
}

func short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
