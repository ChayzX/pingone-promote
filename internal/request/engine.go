package request

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/zclconf/go-cty/cty"
	ctyjson "github.com/zclconf/go-cty/cty/json"
	"net/url"
	"os"
	"path/filepath"
	"pingone-promote/internal/catalog"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"

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
	CatalogAllIDs(ctx context.Context, creds exporter.Credentials) ([]exporter.Resource, error)
	ScopedExport(ctx context.Context, creds exporter.Credentials, seeds, exclude []string) (*exporter.Export, error)
	WholeExport(ctx context.Context, creds exporter.Credentials, exclude []string) (*exporter.Export, error)
}

// SelectedExporter supports a scoped export without upstream dependencies.
// Exporters that cannot provide this must fail rather than silently widen selection.
type SelectedExporter interface {
	SelectedExport(context.Context, exporter.Credentials, []string, []string) (*exporter.Export, error)
}

// Committer writes the request commit (GitLab in CI or the hosted server).
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
	// BranchPrefix is the request branch namespace (default
	// pingone-promote/, PINGONE_PROMOTE_BRANCH_PREFIX). The job runs only on
	// <BranchPrefix><requestId>.
	BranchPrefix string
	// BranchPattern, when set (hosted server), replaces the prefix rule:
	// the request names its branch (BranchName), which must match it whole.
	BranchPattern *regexp.Regexp
	// NoRequestFile is set by the hosted server: there is no
	// .pingone-promote/request.json on the branch, so the commit does not
	// delete one, and a request that changes nothing commits nothing.
	NoRequestFile bool
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
	res.Scope = req.Scope
	if res.Scope == "" {
		res.Scope = ScopeSelected
	}
	res.Branch = env.Branch
	r := &runner{env: env, req: req, res: res, layout: env.Layout.WithDefaults()}
	r.run(ctx)
	// Every status, including blocked and failed, is redacted.
	res.redact()
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
	head := promote.DirRepo(r.env.Workspace)
	sel, err := r.resolve(ctx, creds, head)
	if err != nil {
		return // resolve filled the error
	}
	var exp *exporter.Export
	if req.Whole() {
		exp, err = r.env.Exporter.WholeExport(ctx, creds, sel.patterns())
	} else if !req.Upstream() {
		selected, ok := r.env.Exporter.(SelectedExporter)
		if !ok {
			res.fail(ErrPreflight, "export", "this exporter cannot export without upstream dependencies", false)
			return
		}
		exp, err = selected.SelectedExport(ctx, creds, sel.seeds, sel.patterns())
	} else {
		exp, err = r.env.Exporter.ScopedExport(ctx, creds, sel.seeds, sel.patterns())
	}
	if err != nil {
		var lc *exporter.LabelCollisionError
		code := ErrPreflight
		if errors.As(err, &lc) {
			code = ErrBlocked
		}
		res.fail(code, "export", err.Error(), !errors.As(err, &lc))
		return
	}
	// Resources the export refers to but does not contain (the combined
	// exporter reports them) are treated like excluded ones: a Git-managed
	// copy keeps its address, any other becomes a per-environment ID
	// variable.
	if len(exp.Referenced) > 0 {
		managed, err := promote.ManagedAddresses(head, r.layout)
		if err != nil {
			res.fail(ErrPreflight, "internal", err.Error(), false)
			return
		}
		for a, id := range exp.Referenced {
			if _, ok := sel.excluded[a]; !ok {
				sel.excluded[a] = &exclusion{id: id, managed: managed[a], referenced: true}
			}
		}
	}
	res.Warnings = append(res.Warnings, exp.Warnings...)
	neu, err := neutralize.Neutralize(exp.NeutralizeInput(), neutralize.Options{
		SourceEnvironmentID: sourceID, ConnectorEnvLocal: req.ConnectorEnvLocal, InlineVariables: set(req.InlineValues), SecretNames: req.SecretMappings,
		Excluded: sel.neutralizeExcluded(), Lift: LiftRules()})
	if err != nil {
		res.fail(ErrBlocked, "merge", err.Error(), false)
		return
	}
	sel.reportedOnly(neu)
	for _, f := range exp.Findings {
		res.Blockers[RequestBlockers] = append(res.Blockers[RequestBlockers], BlockerOut{Code: promote.BlockSecretFinding,
			Message: fmt.Sprintf("the raw export looks like it contains a secret (%s) at %s:%d", f.Kind, f.File, f.Line)})
	}
	res.Warnings = append(res.Warnings, neu.Warnings...)
	res.Variables = describeVariables(neu)
	var selected []string
	for _, s := range sel.seeds {
		selected = append(selected, s)
		if strings.HasPrefix(s, neutralize.TypeFlow+".") {
			selected = append(selected, exporter.Companions(s)...)
		}
		selected = append(selected, ownedChildren(exp.Owners, s)...)
	}
	choices := map[string]promote.Choice{}
	for a, c := range req.DependencyChoices {
		choices[a] = promote.Choice(c)
	}
	mreq := promote.MergeRequest{Layout: r.layout, Export: neu, Head: head, Selected: selected, Choices: choices, WholeEnvironment: req.Whole(),
		SourceEnv: req.Source, Rollout: r.env.Rollout, EnvValues: req.Values(), SecretsConfig: r.env.SecretsConfig,
		Adopt: exp.Adopt, ImportIDs: req.ImportIDs}
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
	// The fmt gate runs on every file, preview or not; the formatter runs
	// for several files at a time, and results are taken in path order.
	var fmtPaths []string
	for _, p := range m.ChangedPaths() {
		if strings.HasSuffix(p, ".tf") || strings.HasSuffix(p, ".tfvars") {
			fmtPaths = append(fmtPaths, p)
		}
	}
	type fmtResult struct {
		out []byte
		err error
	}
	fmtResults := make([]fmtResult, len(fmtPaths))
	var fmtWG sync.WaitGroup
	fmtSem := make(chan struct{}, 4)
	for i, p := range fmtPaths {
		fmtWG.Add(1)
		go func(i int, content []byte) {
			defer fmtWG.Done()
			fmtSem <- struct{}{}
			defer func() { <-fmtSem }()
			fmtResults[i].out, fmtResults[i].err = tffmt.FormatWithTerraform(ctx, r.env.TerraformBin, content)
		}(i, m.Files[p])
	}
	fmtWG.Wait()
	for i, p := range fmtPaths {
		out, err := fmtResults[i].out, fmtResults[i].err
		var mismatch *tffmt.FormatMismatchError
		if errors.As(err, &mismatch) {
			// A file people edit (an environment's tfvars, ...) may not be
			// fmt-clean already; the commit carries the formatted file,
			// provided formatting it again changes nothing. Files this tool
			// creates must be canonical as generated.
			if _, existed, rerr := head.Read(p); rerr == nil && existed {
				if again, ferr := tffmt.FormatWithTerraform(ctx, r.env.TerraformBin, out); ferr == nil && bytes.Equal(again, out) {
					res.Warnings = append(res.Warnings, p+" is also reformatted with terraform fmt (it was not fmt-clean before)")
					err = nil
				}
			}
		}
		if err != nil {
			res.fail(ErrPreflight, "merge", p+": "+err.Error(), false)
			return
		}
		m.Files[p] = out
	}
	// A file GitLab would refuse is a blocker now, not a failed submit.
	for _, p := range m.ChangedPaths() {
		if n := len(m.Files[p]); n > gitlab.MaxFileSize {
			res.Blockers[RequestBlockers] = append(res.Blockers[RequestBlockers], BlockerOut{Code: "file_too_large", Address: p,
				Message: fmt.Sprintf("%s would be %d MB, more than the %d MB GitLab accepts from this tool; copy fewer items at once (whole sections, or leave some out)", p, n>>20, gitlab.MaxFileSize>>20)})
		}
	}
	diff, err := m.Diff(head)
	if err != nil {
		res.fail(ErrPreflight, "internal", err.Error(), true)
		return
	}
	limit := MaxDiff
	if req.Whole() {
		limit = MaxDiffAll
	}
	res.Diff, res.DiffTruncated = truncateDiff(diff, limit)
	for _, p := range m.ChangedPaths() {
		action := "update"
		if _, ok, _ := head.Read(p); !ok {
			action = "create"
		}
		res.Files = append(res.Files, FileOut{Path: p, Action: action})
	}
	if r.env.Committer != nil && !r.env.NoRequestFile {
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

func (r *runner) branchPrefix() string {
	if r.env.BranchPrefix != "" {
		return r.env.BranchPrefix
	}
	return BranchPrefix
}

// checkContext enforces the branch and environment rules.
func (r *runner) checkContext() error {
	req, env := r.req, r.env
	if err := r.layout.Validate(); err != nil {
		return err
	}
	prefix := r.branchPrefix()
	if !gitlab.ValidRequestBranchPrefix(prefix) {
		return fmt.Errorf("the request branch prefix %q is invalid: it must start with a letter or digit, end with / or -, and be at most 60 characters", prefix)
	}
	if env.BranchPattern != nil {
		if !req.NamedBranch || !env.BranchPattern.MatchString(req.BranchName) {
			return invalid("branchName", "does not match the branch pattern "+env.BranchPattern.String())
		}
		if env.Committer != nil && (env.Branch != req.BranchName || env.Branch == env.DefaultBranch || env.Head == "") {
			return errors.New("the request branch does not match the branch the request names")
		}
	} else if req.BranchName != "" && req.BranchName != prefix+req.RequestID {
		return invalid("branchName", fmt.Sprintf("must be %s%s (the job's branch prefix is %s)", prefix, req.RequestID, prefix))
	}
	if env.Committer != nil && env.BranchPattern == nil {
		switch {
		case env.Branch == "" || env.Head == "":
			return errors.New("the request branch and commit are unknown (CI_COMMIT_BRANCH, CI_COMMIT_SHA)")
		case env.Branch == env.DefaultBranch:
			return errors.New("requests never run on the default branch")
		case env.Branch != prefix+req.RequestID:
			return fmt.Errorf("request %s must run on branch %s%s, not %s", req.RequestID, prefix, req.RequestID, env.Branch)
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
		return "", fmt.Errorf("the PingOne environment ID of %s is not known: %s does not set pingone_environment_id, and the server configuration has no sourceEnvironmentIds entry for it", r.req.Source, p)
	}
	return v.AsString(), nil
}

// selection is what a request resolves to: the seeds to export and the
// resources to leave out.
type selection struct {
	seeds []string
	// excluded maps every excluded address (flow companions included) to
	// what is known about it.
	excluded map[string]*exclusion
}

type exclusion struct {
	id      string
	managed bool
	// derived marks a flow's deploy and enable: they follow their flow and
	// are not reported on their own.
	derived bool
	// referenced marks a resource the export refers to without containing
	// it (not chosen by the request).
	referenced bool
}

func (s *selection) patterns() []string {
	out := make([]string, 0, len(s.excluded))
	for a := range s.excluded {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

func (s *selection) neutralizeExcluded() []neutralize.Excluded {
	var out []neutralize.Excluded
	for _, a := range s.patterns() {
		e := s.excluded[a]
		out = append(out, neutralize.Excluded{Address: a, Managed: e.managed, ID: e.id})
	}
	return out
}

// reportedOnly drops the derived companions from the excluded report.
func (s *selection) reportedOnly(neu *neutralize.Result) {
	kept := neu.Excluded[:0]
	for _, e := range neu.Excluded {
		if x := s.excluded[e.Address]; x != nil && x.derived {
			continue
		}
		kept = append(kept, e)
	}
	neu.Excluded = kept
}

// resolve turns the request's includes and excludes into addresses. It
// never guesses: no match or several matches is a typed error, for an
// exclude the same as for an include.
func (r *runner) resolve(ctx context.Context, creds exporter.Credentials, head promote.Repo) (*selection, error) {
	req := r.req
	sel := &selection{excluded: map[string]*exclusion{}}
	var depExcluded []string
	for a, c := range req.DependencyChoices {
		if c == string(promote.ChoiceExclude) {
			depExcluded = append(depExcluded, a)
		}
	}
	sort.Strings(depExcluded)
	items := len(req.Flows) + len(req.Applications) + len(req.FlowPolicies) + len(req.Resources) + len(req.Families)
	var cat []exporter.Resource
	if items+len(req.Exclude)+len(depExcluded) > 0 || req.Whole() {
		withIDs, allIDs := false, false
		if !req.Whole() && !req.Upstream() {
			withIDs, allIDs = true, true
		}
		for _, resource := range req.Resources {
			if resource.ID != "" {
				withIDs, allIDs = true, true
			}
		}
		for _, f := range req.Flows {
			withIDs = withIDs || f.ID != ""
		}
		for _, e := range req.Exclude {
			if e.ID != "" {
				withIDs = true
				allIDs = allIDs || e.Type != ExcludeFlow
			}
			// An excluded flow referenced inside another flow's node
			// properties carries no ID in the export; the catalog has it.
			withIDs = withIDs || e.Type == ExcludeFlow || strings.HasPrefix(e.Address, neutralize.TypeFlow+".")
		}
		for _, a := range depExcluded {
			withIDs = withIDs || strings.HasPrefix(a, neutralize.TypeFlow+".")
		}
		var err error
		if allIDs {
			cat, err = r.env.Exporter.CatalogAllIDs(ctx, creds)
		} else {
			cat, err = r.env.Exporter.Catalog(ctx, creds, withIDs)
		}
		if err != nil {
			r.res.fail(ErrPreflight, "export", err.Error(), true)
			return nil, err
		}
	}
	unresolved := errors.New("unresolved")
	fail := func(field, msg string, matches []exporter.Resource, query string) error {
		r.res.fail(ErrPreflight, "validation", msg, false)
		r.res.Error.Field = field
		for _, m := range matches {
			r.res.Error.Candidates = append(r.res.Error.Candidates, Selected{Query: query, Address: m.String(), Name: m.Name, ID: m.ID})
		}
		return unresolved
	}
	// pickOne finds exactly one resource.
	pickOne := func(field, kind string, ref FlowRef, matches []exporter.Resource) (*exporter.Resource, error) {
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
			return &m, nil
		case 0:
			return nil, fail(field, fmt.Sprintf("no %s matches %q in %s", kind, query, req.Source), nil, query)
		}
		return nil, fail(field, fmt.Sprintf("%d %ss match %q in %s; select one by ID", len(matches), kind, query, req.Source), matches, query)
	}
	includedBy := map[string]string{} // address -> request field
	include := func(field, kind string, ref FlowRef, matches []exporter.Resource) error {
		m, err := pickOne(field, kind, ref, matches)
		if err != nil {
			return err
		}
		query := ref.ID
		if query == "" {
			query = ref.Name
		}
		includedBy[m.String()] = field
		sel.seeds = append(sel.seeds, m.String())
		if !req.Whole() {
			r.res.Selected = append(r.res.Selected, Selected{Query: query, Address: m.String(), Name: m.Name, ID: m.ID})
		}
		return nil
	}
	for i, f := range req.Flows {
		q := f.ID
		if q == "" {
			q = f.Name
		}
		if err := include(fmt.Sprintf("flows[%d]", i), "flow", f, exporter.Search(cat, q)); err != nil {
			return nil, err
		}
	}
	for i, a := range req.Applications {
		if err := include(fmt.Sprintf("applications[%d]", i), "application", a, exporter.SearchType(cat, neutralize.TypeApplication, a.Name)); err != nil {
			return nil, err
		}
	}
	for i, p := range req.FlowPolicies {
		if err := include(fmt.Sprintf("flowPolicies[%d]", i), "flow policy", p, exporter.SearchType(cat, neutralize.TypeFlowPolicy, p.Name)); err != nil {
			return nil, err
		}
	}
	for i, ref := range req.Resources {
		field := fmt.Sprintf("resources[%d]", i)
		query := ref.ID
		if query == "" {
			query = ref.Name
		}
		var matches []exporter.Resource
		if ref.Address != "" {
			query = ref.Address
			for _, candidate := range cat {
				if candidate.String() == ref.Address {
					matches = append(matches, candidate)
				}
			}
		} else {
			typ, _ := resourceType(ref.Type)
			matches = exporter.SearchType(cat, typ, query)
		}
		if err := include(field, "resource", FlowRef{ID: ref.ID, Name: query}, matches); err != nil {
			return nil, err
		}
	}
	if len(req.Families) > 0 && !req.Whole() {
		want := set(req.Families)
		requestExcluded := map[string]bool{}
		for _, e := range req.Exclude {
			requestExcluded[e.Address] = e.Address != ""
		}
		found, builtIn, builtInOf := map[string]int{}, 0, map[string]int{}
		for _, c := range cat {
			d, ok := catalog.Get(c.Type)
			// Owned children (a flow's deploy and enable, ...) travel with
			// their parent, so an excluded parent takes them along.
			if !ok || !want[d.Family] || d.Owned || requestExcluded[c.String()] || includedBy[c.String()] != "" || req.DependencyChoices[c.String()] == string(promote.ChoiceExclude) {
				continue
			}
			// As for the whole environment: items that exist in every
			// environment are copied only when picked.
			if c.BuiltIn {
				builtIn++
				builtInOf[d.Family]++
				continue
			}
			found[d.Family]++
			includedBy[c.String()] = "families"
			sel.seeds = append(sel.seeds, c.String())
			r.res.Selected = append(r.res.Selected, Selected{Query: d.Family, Address: c.String(), Name: c.Name, ID: c.ID})
		}
		if builtIn > 0 {
			r.res.Warnings = append(r.res.Warnings, builtInNote(builtIn))
		}
		for _, f := range req.Families {
			if found[f] == 0 && builtInOf[f] > 0 {
				r.res.Warnings = append(r.res.Warnings, fmt.Sprintf("%s: %s has only items PingOne creates in every environment, so none are copied", catalog.FamilyLabels[f], req.Source))
			} else if found[f] == 0 {
				r.res.Warnings = append(r.res.Warnings, fmt.Sprintf("%s: nothing of this kind was found in %s", catalog.FamilyLabels[f], req.Source))
			}
		}
		if len(sel.seeds) == 0 && builtIn > 0 {
			return nil, fail("families", "the chosen sections hold only items PingOne creates in every environment, which are not copied; pick one of them to copy it", nil, "")
		}
		if len(sel.seeds) == 0 {
			return nil, fail("families", "nothing in the chosen sections was found in "+req.Source, nil, "")
		}
	}
	if req.Whole() {
		// A whole-environment request has no selected item: the listed
		// items were only resolved so that a conflicting exclude is caught.
		sel.seeds = nil
		builtIn := 0
		for _, c := range cat {
			if d, ok := catalog.Get(c.Type); c.BuiltIn && ok && !d.Owned {
				builtIn++
			}
		}
		if builtIn > 0 {
			r.res.Warnings = append(r.res.Warnings, builtInNote(builtIn))
		}
		if items > 0 {
			r.res.Warnings = append(r.res.Warnings, "flows, applications and flowPolicies are ignored when scope is all; the whole environment is exported")
		}
	}
	ids := map[string]string{}
	for _, c := range cat {
		if c.ID != "" {
			ids[c.String()] = c.ID
		}
	}
	exclude := func(addr, from string) {
		if _, ok := sel.excluded[addr]; ok {
			return
		}
		sel.excluded[addr] = &exclusion{id: ids[addr]}
		if strings.HasPrefix(addr, neutralize.TypeFlow+".") {
			for _, c := range exporter.Companions(addr) {
				if _, ok := sel.excluded[c]; !ok {
					sel.excluded[c] = &exclusion{derived: true}
				}
			}
		}
	}
	requestExcluded := map[string]string{} // address -> exclude[i]
	for i, e := range req.Exclude {
		field := fmt.Sprintf("exclude[%d]", i)
		var m *exporter.Resource
		var err error
		if e.Address != "" {
			var found []exporter.Resource
			for _, c := range cat {
				if c.String() == e.Address {
					found = append(found, c)
				}
			}
			if len(found) == 0 {
				return nil, fail(field+".address", fmt.Sprintf("no resource has the address %s in %s", e.Address, req.Source), nil, e.Address)
			}
			m = &found[0]
		} else {
			typ, _ := resourceType(e.Type)
			ref := FlowRef{ID: e.ID, Name: e.Name}
			query := e.ID
			if query == "" {
				query = e.Name
			}
			if m, err = pickOne(field, kindName(e.Type), ref, exporter.SearchType(cat, typ, query)); err != nil {
				return nil, err
			}
		}
		requestExcluded[m.String()] = field
		exclude(m.String(), field)
	}
	// An include that is also excluded is a conflict, named on both sides.
	for _, s := range sel.seeds {
		if f, ok := requestExcluded[s]; ok {
			return nil, fail(f, fmt.Sprintf("%s resolves to %s, which %s also includes; a resource cannot be both included and excluded", f, s, includedBy[s]), nil, s)
		}
	}
	// Excluding a dependency: the selected item itself cannot be one.
	selected := map[string]bool{}
	for _, s := range sel.seeds {
		selected[s] = true
		if strings.HasPrefix(s, neutralize.TypeFlow+".") {
			for _, c := range exporter.Companions(s) {
				selected[c] = true
				includedBy[c] = includedBy[s]
			}
		}
	}
	for _, a := range depExcluded {
		if selected[a] {
			return nil, fail("dependencyChoices."+a, fmt.Sprintf("dependencyChoices.%s is exclude, but %s is the selected item (%s) itself; it cannot be excluded", a, a, includedBy[a]), nil, a)
		}
		exclude(a, "dependencyChoices."+a)
	}
	// Without upstream dependencies, every non-selected catalog item is an
	// explicit omission. This repairs embedded references just like an exclude.
	if !req.Whole() && !req.Upstream() {
		for _, c := range cat {
			if !selected[c.String()] && strings.HasPrefix(c.Type, "pingone_davinci_") {
				exclude(c.String(), "includeUpstream")
			}
		}
	}
	// Which of them Git already manages: references to those keep pointing
	// at the Git address.
	if len(sel.excluded) > 0 {
		managed, err := promote.ManagedAddresses(head, r.layout)
		if err != nil {
			r.res.fail(ErrPreflight, "internal", err.Error(), false)
			return nil, err
		}
		for a, e := range sel.excluded {
			e.managed = managed[a]
		}
	}
	sort.Strings(sel.seeds)
	return sel, nil
}

func kindName(t string) string {
	switch t {
	case ExcludeFlowPolicy:
		return "flow policy"
	case ExcludeConnection:
		return "connection"
	}
	return t
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
	if msg == "" && req.Whole() {
		msg = "PingOne: whole " + req.Source + " environment"
	}
	if msg == "" {
		var names []string
		for _, s := range r.res.Selected {
			// Names come from the tenant: keep them on one line so they
			// cannot add trailers or a second paragraph.
			names = append(names, oneLine(s.Name))
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

// oneLine replaces control characters (newlines included) and Unicode
// line separators with spaces.
func oneLine(s string) string {
	return strings.Map(func(c rune) rune {
		if unicode.IsControl(c) || c == '\u2028' || c == '\u2029' || c == '\u0085' {
			return ' '
		}
		return c
	}, s)
}

// fencedNote quotes free text from the requester as a code block whose
// fence the text cannot close, so it cannot hide or imitate the generated
// summary.
func fencedNote(text string) string {
	text = strings.ReplaceAll(text, "`", "'")
	text = strings.ReplaceAll(text, "~~~", "~ ~ ~")
	return "```text\n" + text + "\n```\n"
}

func (r *runner) footer() string {
	by := r.req.RequestedBy
	if by == "" {
		by = "unknown"
	}
	return "Requested-by: " + by + "\nRequest-id: " + r.req.RequestID
}

// maxDescriptionRows keeps a whole-environment merge request description far
// below GitLab's size limit.
const maxDescriptionRows = 300

func (r *runner) description() string {
	var b strings.Builder
	// The generated summary comes first; the requester's text follows,
	// fenced, under its own heading.
	b.WriteString("Source environment: `" + r.req.Source + "`\n\n")
	if r.req.Whole() {
		b.WriteString("Scope: the whole environment. Nothing is deleted; a resource that differs from Git keeps its Git version unless it is listed as updated.\n\n")
		b.WriteString("| Type | Total | Added | Updated | Kept (differs) | Unchanged | Moved | Excluded |\n| --- | --- | --- | --- | --- | --- | --- | --- |\n")
		types := make([]string, 0, len(r.res.Summary.ByType))
		for t := range r.res.Summary.ByType {
			types = append(types, t)
		}
		sort.Strings(types)
		for _, t := range types {
			c := r.res.Summary.ByType[t]
			fmt.Fprintf(&b, "| `%s` | %d | %d | %d | %d | %d | %d | %d |\n", t, c.Total, c.Added, c.Updated, c.KeptDiffers, c.Unchanged, c.Moved, c.Excluded)
		}
		b.WriteString("\nChanges (resources identical to Git are not listed):\n\n")
	}
	b.WriteString("| Address | Role | Change |\n| --- | --- | --- |\n")
	rows := 0
	for _, c := range r.res.Changes {
		if r.req.Whole() && c.Action == string(promote.ActionUnchanged) {
			continue
		}
		if rows++; rows > maxDescriptionRows {
			continue
		}
		b.WriteString("| `" + c.Address + "` | " + c.Role + " | " + c.Action + " |\n")
	}
	if rows > maxDescriptionRows {
		fmt.Fprintf(&b, "\n... and %d more; the full list is in result.json of the job.\n", rows-maxDescriptionRows)
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
	if d := strings.TrimSpace(r.req.Description); d != "" {
		b.WriteString("\n### Requester's note\n\n" + fencedNote(d))
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
	if len(files) == 0 && r.env.NoRequestFile {
		res.Status = StatusNoChanges
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
	if req.Whole() && !strings.Contains(title, WholeEnvironmentTitle) {
		prefix := WholeEnvironmentTitle + " "
		if room := 255 - len(prefix); len(title) > room {
			title = strings.TrimRight(strings.ToValidUTF8(title[:room], ""), " ")
		}
		title = prefix + title
	}
	labels := r.env.MRLabels
	if labels == nil {
		labels = []string{"pingone-promote"}
	}
	policy := gitlab.LayoutPolicy{ModuleDir: r.layout.ModuleDir, OverlayPattern: r.layout.OverlayPattern, CustomerSecretsPath: r.layout.CustomerSecretsFile,
		RootFiles: []string{r.layout.ModuleFile, r.layout.VariablesFile, r.layout.SecretsFile, r.layout.ImportsFile, "versions.tf"}}
	out, err := r.env.Committer.CommitToBranch(ctx, gitlab.BranchCommit{RequestID: req.RequestID, Branch: r.env.Branch, ExpectedHead: r.env.Head,
		Mode: mode, CommitMessage: r.commitMessage(), Title: title, Description: r.description(), Files: files, DeleteRequestFile: !r.env.NoRequestFile,
		Policy: policy, Labels: labels, BranchPrefix: r.branchPrefix(), BranchPattern: r.env.BranchPattern})
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

// LiftRules are the neutralizer's per-environment attributes: the default
// rules plus every catalog type's environment-specific attributes.
func LiftRules() []neutralize.LiftRule {
	rules := append([]neutralize.LiftRule(nil), neutralize.DefaultLiftRules...)
	for _, d := range catalog.All() {
		for _, a := range d.EnvLocal {
			rules = append(rules, neutralize.LiftRule{ResourceType: d.Type, Attribute: a})
		}
	}
	return rules
}

// ownedChildren lists the exported children that belong to a selected core
// item (a sign-on policy's actions, an application's attribute mappings),
// transitively: they are part of the selection, not dependencies. The
// combined exporter reports the parent links.
func ownedChildren(owners map[string]string, seed string) []string {
	var out []string
	for child, parent := range owners {
		if parent == seed {
			out = append(out, child)
			out = append(out, ownedChildren(owners, child)...)
		}
	}
	sort.Strings(out)
	return out
}

// describeVariables explains each per-environment value of a neutralized
// export for screens.
func describeVariables(neu *neutralize.Result) map[string]VariableOut {
	out := map[string]VariableOut{}
	for name, v := range neu.Variables {
		if !v.PerEnvironment() || name == neutralize.EnvironmentIDVariable {
			continue
		}
		vo := VariableOut{Kind: string(v.Kind), Description: v.Description}
		switch {
		case v.Kind == neutralize.KindExcludedRef:
			vo.Resource = v.Excluded
		case strings.HasPrefix(v.Origin, "lift:"):
			rest := strings.TrimPrefix(v.Origin, "lift:")
			typ, after, _ := strings.Cut(rest, ".")
			label, attr, _ := strings.Cut(after, ".")
			vo.Resource, vo.Attribute = typ+"."+label, attr
		}
		if sv, ok := neu.SourceValues[name]; ok && sv.IsKnown() && !sv.IsNull() {
			if sv.Type() == cty.String {
				vo.SourceValue = sv.AsString()
			} else if b, err := ctyjson.Marshal(sv, sv.Type()); err == nil {
				vo.SourceValue = string(b)
			}
		}
		if len(vo.SourceValue) > 300 {
			vo.SourceValue = vo.SourceValue[:300] + "..."
		}
		out[name] = vo
	}
	return out
}

func builtInNote(n int) string {
	return fmt.Sprintf("%d items that PingOne creates in every environment (default policies, system applications, predefined attributes and templates) are not copied; each environment keeps its own. To copy one, pick it", n)
}
