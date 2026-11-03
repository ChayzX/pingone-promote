package request

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"pingone-promote/internal/exporter"
	"pingone-promote/internal/gitlab"
	"pingone-promote/internal/neutralize"
	"pingone-promote/internal/overlay"
	"pingone-promote/internal/pipelinesecrets"
	"pingone-promote/internal/promote"
	"pingone-promote/internal/tffmt"
)

// Exporter is the subset of *exporter.Runner the engine uses.
type Exporter interface {
	Catalog(ctx context.Context, creds exporter.Credentials, withIDs bool) ([]exporter.Resource, error)
	ScopedExport(ctx context.Context, creds exporter.Credentials, seeds []string) (*exporter.Export, error)
}

// Committer writes the request commit (GitLab in CI).
type Committer interface {
	CommitToBranch(ctx context.Context, bc gitlab.BranchCommit) (gitlab.Result, error)
	OpenMergeRequest(ctx context.Context, branch string) (*gitlab.MergeRequest, error)
}

// GitLabCommitter adapts a Submitter.
type GitLabCommitter struct{ S *gitlab.Submitter }

// CommitToBranch implements Committer.
func (g GitLabCommitter) CommitToBranch(ctx context.Context, bc gitlab.BranchCommit) (gitlab.Result, error) {
	return g.S.CommitToBranch(ctx, bc)
}

// OpenMergeRequest implements Committer.
func (g GitLabCommitter) OpenMergeRequest(ctx context.Context, branch string) (*gitlab.MergeRequest, error) {
	return g.S.Client.OpenMergeRequest(ctx, branch)
}

// Env is everything the engine needs besides the request.
type Env struct {
	// Workspace is the checked-out config repository (the request branch).
	Workspace string
	Layout    promote.Layout
	// Rollout is the environment chain after the source, in order.
	Rollout []string
	// AllowedSources limits request.source; empty allows any environment
	// that is not in the rollout.
	AllowedSources []string
	// SourceEnvironmentID overrides the source overlay's
	// pingone_environment_id.
	SourceEnvironmentID string
	Exporter            Exporter
	Credentials         exporter.Credentials
	TerraformBin        string
	SecretsConfig       *pipelinesecrets.Config
	// Committer is nil for local runs (files are written to Workspace).
	Committer Committer
	// CI facts: the request branch, the commit the job checked out, the
	// default branch and the project URL.
	Branch, Head, DefaultBranch, ProjectURL string
	// MRLabels are added to merge requests (default pingone-promote).
	MRLabels []string
	// PriorCommit finds an earlier commit of this request on the branch
	// (trailer PingOne-Promote-Request), for idempotent re-submits.
	PriorCommit func(requestID string) (sha string, found bool)
	// BaseRepo reads the repository at request.baseCommit.
	BaseRepo func(commit string) (promote.Repo, error)
}

// Run executes one request. It never returns an error: every failure is a
// typed error in the result, and nothing is committed on failure.
func Run(ctx context.Context, env Env, req *Request) *Result {
	res := newResult()
	res.RequestID, res.RequestedBy, res.DryRun, res.Mode, res.Source = req.RequestID, req.RequestedBy, req.DryRun, req.Mode, req.Source
	res.Branch = env.Branch
	r := &runner{env: env, req: req, res: res, layout: env.Layout.WithDefaults()}
	r.run(ctx)
	return res
}

type runner struct {
	env    Env
	req    *Request
	res    *Result
	layout promote.Layout
}

func (r *runner) urls(commit string) {
	if r.env.ProjectURL == "" {
		return
	}
	base := strings.TrimRight(r.env.ProjectURL, "/")
	if r.env.Branch != "" {
		r.res.BranchURL = base + "/-/tree/" + url.PathEscape(r.env.Branch)
	}
	if commit != "" {
		r.res.DiffURL = base + "/-/commit/" + commit
	} else if r.env.Branch != "" && r.env.DefaultBranch != "" {
		r.res.DiffURL = base + "/-/compare/" + url.PathEscape(r.env.DefaultBranch) + "..." + url.PathEscape(r.env.Branch)
	}
}

func (r *runner) run(ctx context.Context) {
	res, req := r.res, r.req
	if err := r.checkContext(); err != nil {
		res.fail(ErrPreflight, "validation", err.Error(), false)
		return
	}
	r.urls("")
	// Idempotent re-submit: this request was committed before.
	if !req.DryRun && r.env.Committer != nil && r.env.PriorCommit != nil {
		if sha, ok := r.env.PriorCommit(req.RequestID); ok {
			res.Status, res.CommitID = StatusAlreadySubmitted, sha
			r.urls(sha)
			if mr, err := r.env.Committer.OpenMergeRequest(ctx, r.env.Branch); err == nil && mr != nil {
				res.MRUrl, res.MRIid = mr.WebURL, mr.IID
			}
			res.Warnings = append(res.Warnings, "request "+req.RequestID+" was already committed ("+sha+"); nothing new was written")
			return
		}
	}
	sourceID, err := r.sourceEnvironmentID()
	if err != nil {
		res.fail(ErrPreflight, "validation", err.Error(), false)
		return
	}
	creds := r.env.Credentials
	creds.ExportEnvironmentID = sourceID
	seeds, err := r.resolve(ctx, creds)
	if err != nil {
		return // resolve filled the error
	}
	exp, err := r.env.Exporter.ScopedExport(ctx, creds, seeds)
	if err != nil {
		var lc *exporter.LabelCollisionError
		code := ErrPreflight
		if errors.As(err, &lc) {
			code = ErrBlocked
		}
		res.fail(code, "export", err.Error(), !errors.As(err, &lc))
		return
	}
	neu, err := neutralize.Neutralize(&neutralize.Input{Files: exp.Files, ModuleDir: exp.ModuleDir, ModuleName: exp.ModuleName}, neutralize.Options{
		SourceEnvironmentID: sourceID, ConnectorEnvLocal: req.ConnectorEnvLocal, InlineVariables: set(req.InlineValues), SecretNames: req.SecretMappings})
	if err != nil {
		res.fail(ErrBlocked, "merge", err.Error(), false)
		return
	}
	for _, f := range exp.Findings {
		res.Blockers[RequestBlockers] = append(res.Blockers[RequestBlockers], BlockerOut{Code: promote.BlockSecretFinding,
			Message: fmt.Sprintf("the raw export looks like it contains a secret (%s) at %s:%d", f.Kind, f.File, f.Line)})
	}
	res.Warnings = append(res.Warnings, neu.Warnings...)
	var selected []string
	for _, s := range seeds {
		selected = append(selected, s)
		if strings.HasPrefix(s, neutralize.TypeFlow+".") {
			selected = append(selected, exporter.Companions(s)...)
		}
	}
	choices := map[string]promote.Choice{}
	for a, c := range req.DependencyChoices {
		choices[a] = promote.Choice(c)
	}
	head := promote.DirRepo(r.env.Workspace)
	mreq := promote.MergeRequest{Layout: r.layout, Export: neu, Head: head, Selected: selected, Choices: choices, Remove: req.Remove,
		SourceEnv: req.Source, Rollout: r.env.Rollout, EnvValues: req.Values(), SecretsConfig: r.env.SecretsConfig}
	if req.BaseCommit != "" {
		if r.env.BaseRepo == nil {
			res.fail(ErrPreflight, "validation", "baseCommit was given but this run cannot read history", false)
			return
		}
		if mreq.Base, err = r.env.BaseRepo(req.BaseCommit); err != nil {
			res.fail(ErrPreflight, "validation", "cannot read baseCommit: "+err.Error(), false)
			return
		}
	}
	m, err := promote.Merge(mreq)
	if err != nil {
		res.fail(ErrPreflight, "merge", err.Error(), false)
		return
	}
	res.setChanges(m.Changes)
	res.setBlockers(m.Blockers)
	res.Warnings = append(res.Warnings, m.Warnings...)
	r.secrets(m)
	// The fmt gate runs on every file, preview or not.
	for _, p := range m.ChangedPaths() {
		if !strings.HasSuffix(p, ".tf") && !strings.HasSuffix(p, ".tfvars") {
			continue
		}
		out, err := tffmt.FormatWithTerraform(ctx, r.env.TerraformBin, m.Files[p])
		if err != nil {
			res.fail(ErrPreflight, "merge", p+": "+err.Error(), false)
			return
		}
		m.Files[p] = out
	}
	diff, err := m.Diff(head)
	if err != nil {
		res.fail(ErrPreflight, "internal", err.Error(), true)
		return
	}
	if len(diff) > MaxDiff {
		diff, res.DiffTruncated = diff[:MaxDiff], true
	}
	res.Diff = diff
	for _, p := range m.ChangedPaths() {
		action := "update"
		if _, ok, _ := head.Read(p); !ok {
			action = "create"
		}
		res.Files = append(res.Files, FileOut{Path: p, Action: action})
	}
	if r.env.Committer != nil {
		res.Files = append(res.Files, FileOut{Path: gitlab.RequestFile, Action: "delete"})
	}
	switch {
	case res.blocked():
		res.Status = StatusBlocked
		res.Error = &ErrorOut{Code: ErrBlocked, Type: "merge", Message: "the request has blockers; nothing was committed", Retryable: false}
		return
	case req.DryRun:
		res.Status = StatusPreviewed
		return
	}
	r.commit(ctx, m)
}

func set(list []string) map[string]bool {
	out := map[string]bool{}
	for _, v := range list {
		out[v] = true
	}
	return out
}

// checkContext enforces the branch and environment rules.
func (r *runner) checkContext() error {
	req, env := r.req, r.env
	if err := r.layout.Validate(); err != nil {
		return err
	}
	if env.Committer != nil {
		switch {
		case env.Branch == "" || env.Head == "":
			return errors.New("the request branch and commit are unknown (CI_COMMIT_BRANCH, CI_COMMIT_SHA)")
		case env.Branch == env.DefaultBranch:
			return errors.New("requests never run on the default branch")
		case env.Branch != BranchPrefix+req.RequestID:
			return fmt.Errorf("request %s must run on branch %s%s, not %s", req.RequestID, BranchPrefix, req.RequestID, env.Branch)
		}
	}
	rollout := map[string]bool{}
	for _, e := range env.Rollout {
		rollout[e] = true
	}
	if len(env.Rollout) == 0 {
		return errors.New("the rollout (PINGONE_PROMOTE_ROLLOUT) is not configured")
	}
	if rollout[req.Source] {
		return fmt.Errorf("source %s is part of the rollout; the source environment is only exported, never rolled out to", req.Source)
	}
	if len(env.AllowedSources) > 0 && !set(env.AllowedSources)[req.Source] {
		return fmt.Errorf("source %s is not an allowed source environment", req.Source)
	}
	for e := range req.EnvValues {
		if e != req.Source && !rollout[e] {
			return fmt.Errorf("envValues.%s: %s is not the source or in the rollout", e, e)
		}
	}
	return nil
}

func (r *runner) sourceEnvironmentID() (string, error) {
	if r.env.SourceEnvironmentID != "" {
		return r.env.SourceEnvironmentID, nil
	}
	p, err := r.layout.Overlay(r.req.Source)
	if err != nil {
		return "", err
	}
	src, ok, err := promote.DirRepo(r.env.Workspace).Read(p)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("%s does not exist, so the source environment ID is unknown", p)
	}
	o, err := overlay.ParseOverlay(src, p)
	if err != nil {
		return "", err
	}
	v, ok := o.Values[neutralize.EnvironmentIDVariable]
	if !ok || v.Type().FriendlyName() != "string" {
		return "", fmt.Errorf("%s does not set pingone_environment_id", p)
	}
	return v.AsString(), nil
}

// resolve turns flow, application and policy references into addresses.
// It never guesses: no match or several matches is a typed error.
func (r *runner) resolve(ctx context.Context, creds exporter.Credentials) ([]string, error) {
	withIDs := false
	for _, f := range r.req.Flows {
		if f.ID != "" {
			withIDs = true
		}
	}
	cat, err := r.env.Exporter.Catalog(ctx, creds, withIDs)
	if err != nil {
		r.res.fail(ErrPreflight, "export", err.Error(), true)
		return nil, err
	}
	var seeds []string
	pick := func(kind string, ref FlowRef, matches []exporter.Resource) error {
		query := ref.ID
		if query == "" {
			query = ref.Name
		}
		switch len(matches) {
		case 1:
			m := matches[0]
			if ref.ID != "" && ref.Name != "" && !strings.EqualFold(m.Name, ref.Name) {
				r.res.Warnings = append(r.res.Warnings, fmt.Sprintf("%s %s is named %q in the source, not %q", kind, ref.ID, m.Name, ref.Name))
			}
			r.res.Selected = append(r.res.Selected, Selected{Query: query, Address: m.String(), Name: m.Name, ID: m.ID})
			seeds = append(seeds, m.String())
			return nil
		case 0:
			r.res.fail(ErrPreflight, "validation", fmt.Sprintf("no %s matches %q in %s", kind, query, r.req.Source), false)
		default:
			r.res.fail(ErrPreflight, "validation", fmt.Sprintf("%d %ss match %q in %s; select one by ID", len(matches), kind, query, r.req.Source), false)
			for _, m := range matches {
				r.res.Error.Candidates = append(r.res.Error.Candidates, Selected{Query: query, Address: m.String(), Name: m.Name, ID: m.ID})
			}
		}
		return errors.New("unresolved")
	}
	for _, f := range r.req.Flows {
		q := f.ID
		if q == "" {
			q = f.Name
		}
		if err := pick("flow", f, exporter.Search(cat, q)); err != nil {
			return nil, err
		}
	}
	for _, a := range r.req.Applications {
		if err := pick("application", a, exporter.SearchType(cat, neutralize.TypeApplication, a.Name)); err != nil {
			return nil, err
		}
	}
	for _, p := range r.req.FlowPolicies {
		if err := pick("flow policy", p, exporter.SearchType(cat, neutralize.TypeFlowPolicy, p.Name)); err != nil {
			return nil, err
		}
	}
	sort.Strings(seeds)
	return seeds, nil
}

func (r *runner) secrets(m *promote.MergeResult) {
	cfg := pipelinesecrets.DefaultConfig
	if r.env.SecretsConfig != nil {
		cfg = *r.env.SecretsConfig
	}
	added := map[string]bool{}
	for _, n := range m.NewSecrets {
		added[n.VarName] = true
	}
	origin := map[string]string{}
	for k, v := range r.req.SecretMappings {
		origin[v] = k
	}
	for _, name := range m.RequiredSecrets {
		key := name
		if o, ok := origin[name]; ok {
			key = o
		}
		r.res.RequiredSecrets = append(r.res.RequiredSecrets, SecretOut{Key: key, VarName: name, Field: name, Declared: !added[name],
			Vault: cfg.PathPrefix + "/" + name + "@" + cfg.Mount})
	}
}

func (r *runner) commitMessage() string {
	req := r.req
	msg := strings.TrimSpace(req.CommitMessage)
	if msg == "" {
		var names []string
		for _, s := range r.res.Selected {
			names = append(names, s.Name)
		}
		msg = "PingOne: " + strings.Join(names, ", ") + " from " + req.Source
	}
	// Drop trailers the request may carry so they are not duplicated.
	var keep []string
	for _, l := range strings.Split(msg, "\n") {
		if strings.HasPrefix(l, "Requested-by:") || strings.HasPrefix(l, "Request-id:") || strings.HasPrefix(l, gitlab.RequestTrailer) {
			continue
		}
		keep = append(keep, l)
	}
	msg = strings.TrimRight(strings.Join(keep, "\n"), "\n")
	return msg + "\n\n" + r.footer()
}

func (r *runner) footer() string {
	by := r.req.RequestedBy
	if by == "" {
		by = "unknown"
	}
	return "Requested-by: " + by + "\nRequest-id: " + r.req.RequestID
}

func (r *runner) description() string {
	var b strings.Builder
	if d := strings.TrimSpace(r.req.Description); d != "" {
		b.WriteString(d + "\n\n")
	}
	b.WriteString("Source environment: `" + r.req.Source + "`\n\n| Address | Role | Change |\n| --- | --- | --- |\n")
	for _, c := range r.res.Changes {
		b.WriteString("| `" + c.Address + "` | " + c.Role + " | " + c.Action + " |\n")
	}
	var newSecrets []string
	for _, s := range r.res.RequiredSecrets {
		if !s.Declared {
			newSecrets = append(newSecrets, "- `"+s.Vault+"` (TF_VAR_"+s.VarName+")")
		}
	}
	if len(newSecrets) > 0 {
		b.WriteString("\nNew Vault entries to create for every environment:\n\n" + strings.Join(newSecrets, "\n") + "\n")
	}
	b.WriteString("\n" + r.footer() + "\n")
	return b.String()
}

func (r *runner) commit(ctx context.Context, m *promote.MergeResult) {
	res, req := r.res, r.req
	files := make([]gitlab.File, 0, len(m.Files))
	for _, p := range m.ChangedPaths() {
		files = append(files, gitlab.File{Path: p, Content: m.Files[p]})
	}
	if r.env.Committer == nil {
		// Local run: write into the working tree.
		for _, f := range files {
			full := filepath.Join(r.env.Workspace, filepath.FromSlash(f.Path))
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				res.fail(ErrPartial, "internal", err.Error(), true)
				return
			}
			if err := os.WriteFile(full, f.Content, 0o644); err != nil {
				res.fail(ErrPartial, "internal", err.Error(), true)
				return
			}
		}
		res.Status = StatusSubmitted
		if len(files) == 0 {
			res.Status = StatusNoChanges
		}
		return
	}
	mode := gitlab.Mode(req.Mode)
	if len(files) == 0 {
		// Only the request file goes; there is nothing to review.
		mode = gitlab.ModeBranch
	}
	title := req.Title
	if title == "" {
		title = strings.SplitN(r.commitMessage(), "\n", 2)[0]
	}
	labels := r.env.MRLabels
	if labels == nil {
		labels = []string{"pingone-promote"}
	}
	policy := gitlab.LayoutPolicy{ModuleDir: r.layout.ModuleDir, OverlayPattern: r.layout.OverlayPattern, CustomerSecretsPath: r.layout.CustomerSecretsFile,
		RootFiles: []string{r.layout.ModuleFile, r.layout.VariablesFile, r.layout.SecretsFile, r.layout.ImportsFile, "versions.tf"}}
	out, err := r.env.Committer.CommitToBranch(ctx, gitlab.BranchCommit{RequestID: req.RequestID, Branch: r.env.Branch, ExpectedHead: r.env.Head,
		Mode: mode, CommitMessage: r.commitMessage(), Title: title, Description: r.description(), Files: files, DeleteRequestFile: true,
		Policy: policy, Labels: labels})
	var done *gitlab.AlreadyCommittedError
	var conflict *gitlab.ConflictError
	var pre *gitlab.PreflightError
	var partial *gitlab.PartialError
	switch {
	case errors.As(err, &done):
		res.Status = StatusAlreadySubmitted
		out = done.Result
	case errors.As(err, &conflict):
		res.fail(ErrConflict, "gitlab", err.Error(), true)
		return
	case errors.As(err, &pre):
		res.fail(ErrPreflight, "gitlab", err.Error(), true)
		return
	case errors.As(err, &partial):
		res.fail(ErrPartial, "gitlab", err.Error(), partial.Resumable)
		res.CommitID = partial.CommitID
		r.urls(partial.CommitID)
		return
	case err != nil:
		res.fail(ErrPreflight, "gitlab", err.Error(), true)
		return
	default:
		res.Status = StatusSubmitted
		if len(files) == 0 {
			res.Status = StatusNoChanges
		}
	}
	res.CommitID = out.CommitID
	r.urls(out.CommitID)
	if out.MR != nil {
		res.MRUrl, res.MRIid = out.MR.WebURL, out.MR.IID
	}
}
