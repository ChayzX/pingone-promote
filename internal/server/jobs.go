package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"pingone-promote/internal/catalog"
	"pingone-promote/internal/exporter"
	"pingone-promote/internal/gitlab"
	"pingone-promote/internal/neutralize"
	"pingone-promote/internal/overlay"
	"pingone-promote/internal/request"
)

// JobTimeout bounds one background job.
const JobTimeout = 30 * time.Minute

// ConflictMessage is shown when the default branch moved after a preview.
const ConflictMessage = "The default branch changed after the preview. Preview again."

var errJobRunning = errors.New("Something is already running for this request. Wait for it to finish or cancel it.")

// startJob runs fn in the background. It never uses the HTTP request's
// context: the job outlives the request and stops on cancel, timeout or
// server shutdown. The caller holds s.drafts.mu.
func (s *Server) startJob(d *Draft, kind string, fn func(ctx context.Context, stage func(string)) error) error {
	if d.job != nil && d.job.Running {
		return errJobRunning
	}
	ctx, cancel := context.WithTimeout(s.base, JobTimeout)
	job := &Job{Kind: kind, Running: true, Started: s.now(), Stage: "Waiting for a free worker", cancel: cancel}
	d.job = job
	d.Updated = s.now()
	stage := func(text string) {
		s.drafts.mu.Lock()
		job.Stage = text
		s.drafts.mu.Unlock()
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel()
		var err error
		select {
		case s.jobs <- struct{}{}:
			stage("Working")
			err = s.safeRun(ctx, fn, stage)
			<-s.jobs
		case <-ctx.Done():
			err = ctx.Err()
		}
		if err == nil && ctx.Err() != nil {
			err = ctx.Err()
		}
		s.drafts.mu.Lock()
		defer s.drafts.mu.Unlock()
		job.Running, job.Finished = false, s.now()
		switch {
		case err == nil:
		case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errBranchTaken) || errors.Is(err, errNoSourceID):
			job.Err = plainJobError(err)
		default:
			job.Err = s.redact(err.Error())
		}
		if err != nil {
			s.log.Warn("job failed", "kind", kind, "error", job.Err)
		}
	}()
	return nil
}

// errInternal is shown when a job panicked; nothing about the panic is.
var errInternal = errors.New("Something went wrong on the server. Try again; if it keeps happening, tell the operator.")

// safeRun runs a job and turns a panic into errInternal, so a bug cannot
// take the server down or leave the job marked running.
func (s *Server) safeRun(ctx context.Context, fn func(context.Context, func(string)) error, stage func(string)) (err error) {
	defer func() {
		if v := recover(); v != nil {
			s.log.Error("job panic")
			err = errInternal
		}
	}()
	return fn(ctx, stage)
}

// snapshotDirs are the directories besides the root that a snapshot reads.
func (s *Server) snapshotDirs() []string {
	dirs := []string{s.layout.ModuleDir}
	if d := path.Dir(strings.ReplaceAll(s.layout.OverlayPattern, "{env}", "x")); d != "." && d != s.layout.ModuleDir {
		dirs = append(dirs, d)
	}
	return dirs
}

// envFor is env with the source's configured environment ID, if any.
func (s *Server) envFor(project gitlab.Project, workspace, head string, req *request.Request, exp request.Exporter) request.Env {
	e := s.env(project, workspace, head, req.RequestID, exp)
	e.SourceEnvironmentID = s.cfg.envID(req.Source)
	if s.branchRE() != nil {
		e.Branch, e.BranchPattern = req.BranchName, s.branchRE()
	}
	return e
}

// noSourceIDText explains errNoSourceID to the person and the operator.
const noSourceIDText = "This server does not know the PingOne environment ID of the source environment, so it cannot read it. It reads it from the pingone_environment_id line of the source's .tfvars file on the default branch: check that the file exists where overlayPattern says (default {env}.tfvars at the repository root, named after the source exactly as in \"sources\") and sets pingone_environment_id = \"<environment ID>\". Otherwise the operator adds it under \"sourceEnvironmentIds\" in the server configuration and restarts the server."

// errBranchTaken is a typed branch name that already exists in GitLab.
var errBranchTaken = errors.New("that Git branch already exists; type a branch name that is not used yet")

// errNoSourceID is a source whose PingOne environment ID is not known.
var errNoSourceID = errors.New("the PingOne environment ID of the source is not known")

func (s *Server) env(project gitlab.Project, workspace, head, requestID string, exp request.Exporter) request.Env {
	return request.Env{Workspace: workspace, Layout: s.layout, Rollout: s.cfg.Rollout, AllowedSources: s.cfg.Sources,
		Exporter: exp, Credentials: s.creds, TerraformBin: s.cfg.Tools.Terraform,
		Committer: gitlab.HostedCommitter{S: &gitlab.Submitter{Client: s.gl, Formatter: s.cfg.Tools.Terraform}, DefaultBranch: project.DefaultBranch, BranchPrefix: s.cfg.BranchPrefix, BranchPattern: s.branchRE()},
		Branch:    s.cfg.BranchPrefix + requestID, Head: head, DefaultBranch: project.DefaultBranch, ProjectURL: project.WebURL,
		BranchPrefix: s.cfg.BranchPrefix, NoRequestFile: true, MRLabels: s.cfg.GitLab.MRLabels, SecretsConfig: s.secretsCfg}
}

// runPreview exports, merges into a fresh snapshot of the default branch
// and keeps the result, the snapshot and the export for submit.
func (s *Server) runPreview(d *Draft, req *request.Request) func(context.Context, func(string)) error {
	fp := fingerprint(*req)
	return func(ctx context.Context, stage func(string)) error {
		stage("Checking the GitLab project")
		project, err := s.gl.VerifyProject(ctx)
		if err != nil {
			return err
		}
		if s.branchRE() != nil {
			stage("Checking that the branch name is free")
			// This request's own branch (an earlier submit) is fine.
			if b, exists, err := s.gl.GetBranch(ctx, req.BranchName); err != nil {
				return err
			} else if exists && !strings.Contains(b.Commit.Message, gitlab.RequestTrailer+req.RequestID) {
				return &branchTakenError{name: req.BranchName, free: s.freeBranch(ctx, req.BranchName)}
			}
		}
		stage("Reading the default branch from GitLab")
		snap, head, err := s.gl.Snapshot(ctx, project.DefaultBranch, s.snapshotDirs())
		if err != nil {
			return err
		}
		// The repository's own local modules are read too, so the merge can
		// tell whether one of them already holds the PingOne resources.
		if extra := localModuleDirs(snap, s.snapshotDirs()); len(extra) > 0 {
			os.RemoveAll(snap)
			if snap, head, err = s.gl.Snapshot(ctx, project.DefaultBranch, append(s.snapshotDirs(), extra...)); err != nil {
				return err
			}
		}
		if pe, ok := s.exporter.(plainErrors); ok {
			if pe.davinciPending() {
				stage("Waiting for the DaVinci export of " + req.Source + " (it started with the item list and takes about a minute), then merging")
			} else {
				stage("Exporting the source environment and merging")
			}
		} else {
			stage("Exporting the source environment and merging")
		}
		rec, frozen := newRecorder(s.exporter)
		res := request.Run(ctx, s.envFor(project, snap, head, req, rec), req)
		s.scrub(res)
		// Values the environments already have are entered, then the
		// request is previewed again with them (the export is reused).
		if ctx.Err() == nil && s.autofill(ctx, d, res, stage) {
			s.drafts.mu.Lock()
			req2, err := s.buildRequest(d, true)
			s.drafts.mu.Unlock()
			if err == nil {
				stage("Merging again with the values filled in")
				req, fp = req2, fingerprint(*req2)
				rec, frozen = newRecorder(s.exporter)
				res = request.Run(ctx, s.envFor(project, snap, head, req, rec), req)
				s.scrub(res)
			} else {
				s.log.Warn("filled-in values were refused", "error", err.Error())
			}
		}
		if ctx.Err() != nil {
			os.RemoveAll(snap)
			return ctx.Err()
		}
		s.drafts.mu.Lock()
		defer s.drafts.mu.Unlock()
		if old := d.preview; old != nil {
			os.RemoveAll(old.snapshot)
		}
		if s.drafts.byID[d.ID] != d {
			os.RemoveAll(snap) // the draft expired meanwhile
			return nil
		}
		d.preview = &preview{result: res, fingerprint: fp, head: head, defaultBranch: project.DefaultBranch, snapshot: snap, frozen: frozen, at: s.now()}
		d.submitted, d.conflict = nil, false
		d.Updated = s.now()
		return nil
	}
}

// runSubmit commits the previewed request: the same snapshot head as the
// expected head and the frozen export, never a new one.
func (s *Server) runSubmit(d *Draft, req *request.Request, p *preview) func(context.Context, func(string)) error {
	return func(ctx context.Context, stage func(string)) error {
		finish := func(res *request.Result, conflict bool) {
			s.drafts.mu.Lock()
			defer s.drafts.mu.Unlock()
			d.submitted, d.conflict = res, conflict
			d.Updated = s.now()
		}
		stage("Checking the GitLab project")
		project, err := s.gl.VerifyProject(ctx)
		if err != nil {
			return err
		}
		if project.DefaultBranch != p.defaultBranch {
			finish(nil, true)
			return nil
		}
		b, ok, err := s.gl.GetBranch(ctx, project.DefaultBranch)
		if err != nil {
			return err
		}
		if !ok || b.Commit.ID != p.head {
			// The request branch may exist already (an earlier submit of
			// this request): then the committer reports it as already
			// submitted rather than a conflict.
			if rb, exists, err := s.gl.GetBranch(ctx, req.BranchName); err != nil || !exists || !strings.Contains(rb.Commit.Message, gitlab.RequestTrailer+req.RequestID) {
				finish(nil, true)
				return nil
			}
		}
		stage(commitStage)
		res := request.Run(ctx, s.envFor(project, p.snapshot, p.head, req, replayer{f: p.frozen}), req)
		s.scrub(res)
		finish(res, res.Error != nil && res.Error.Code == request.ErrConflict)
		return nil
	}
}

// runSearch loads the source environment's resource list once per draft
// and source (it is cached for a while) and filters it.
func (s *Server) runSearch(source string) func(context.Context, func(string)) error {
	return func(ctx context.Context, stage func(string)) error {
		stage("Checking the GitLab project")
		project, err := s.gl.VerifyProject(ctx)
		if err != nil {
			return err
		}
		stage("Finding the source environment")
		id, err := s.sourceEnvironmentID(ctx, project.DefaultBranch, source)
		if err != nil {
			return err
		}
		stage("Listing the source environment's resources")
		creds := s.creds
		creds.ExportEnvironmentID = id
		// Reading the list reads the source again; previews keep using
		// the previous read until the new one is in, then reuse it.
		var list []exporter.Resource
		if pe, ok := s.exporter.(plainErrors); ok {
			list, err = pe.refresh(ctx, creds)
		} else {
			list, err = s.exporter.CatalogAllIDs(ctx, creds)
		}
		if err != nil {
			return err
		}
		items := make([]Item, 0, len(list))
		for _, r := range list {
			if selectable(r.String()) {
				items = append(items, itemOf(r))
			}
		}
		sort.Slice(items, func(i, j int) bool { return items[i].Address < items[j].Address })
		var dv string
		if pe, ok := s.exporter.(plainErrors); ok {
			dv = pe.davinciProblem(creds)
		}
		s.drafts.mu.Lock()
		defer s.drafts.mu.Unlock()
		s.drafts.catalogs[source] = &catalogEntry{at: s.now(), items: items, davinci: dv}
		if dv != "" {
			s.log.Warn("item list read without DaVinci items", "source", source, "items", len(items))
		} else {
			s.log.Info("item list read", "source", source, "items", len(items))
		}
		return nil
	}
}

// sourceEnvironmentID reads pingone_environment_id from the source
// environment's tfvars on the default branch, as the engine does.
func (s *Server) sourceEnvironmentID(ctx context.Context, branch, source string) (string, error) {
	if id := s.cfg.envID(source); id != "" {
		return id, nil
	}
	p, err := s.layout.Overlay(source)
	if err != nil {
		return "", err
	}
	f, err := s.gl.GetFile(ctx, branch, p)
	if err != nil {
		return "", err
	}
	if !f.Exists {
		return "", fmt.Errorf("%s does not exist on the default branch, so the source environment is unknown", p)
	}
	o, err := overlay.ParseOverlay(f.Content, p)
	if err != nil {
		return "", err
	}
	v, ok := o.Values[neutralize.EnvironmentIDVariable]
	if !ok || v.Type().FriendlyName() != "string" {
		return "", fmt.Errorf("%w: %s does not set %s, and the server configuration has no sourceEnvironmentIds entry for %s", errNoSourceID, p, neutralize.EnvironmentIDVariable, source)
	}
	return v.AsString(), nil
}

// selectable reports whether the request accepts the address: the request
// package decides (configuration resources only, never identity data).
func selectable(addr string) bool {
	r := request.Request{RequestVersion: request.Version, RequestID: "probe-request", Source: "probe", Resources: []request.ExcludeRef{{Address: addr}}}
	return r.Validate() == nil
}

func searchItems(items []Item, query string) ([]Item, int) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil, 0
	}
	var exact, partial []Item
	for _, it := range items {
		switch {
		case strings.EqualFold(it.ID, q) || strings.EqualFold(it.Address, q) || strings.EqualFold(it.Name, q):
			exact = append(exact, it)
		case strings.Contains(strings.ToLower(it.Name), q) || strings.Contains(strings.ToLower(it.Address), q) || it.ID != "" && strings.Contains(strings.ToLower(it.ID), q):
			partial = append(partial, it)
		}
	}
	out := append(exact, partial...)
	n := len(out)
	if n > MaxSearchResults {
		out = out[:MaxSearchResults]
	}
	return out, n
}

// friendlyNames name common types the way people say them; every other
// type uses catalog.DisplayName.
var friendlyNames = map[string]string{
	"pingone_davinci_flow":                          "DaVinci flow",
	"pingone_davinci_flow_deploy":                   "DaVinci flow deployment",
	"pingone_davinci_flow_enable":                   "DaVinci flow enablement",
	"pingone_davinci_variable":                      "DaVinci variable",
	"pingone_davinci_connector_instance":            "DaVinci connection",
	"pingone_davinci_application":                   "DaVinci application",
	"pingone_davinci_application_flow_policy":       "DaVinci flow policy",
	"pingone_application":                           "application",
	"pingone_application_sign_on_policy_assignment": "application sign-on policy assignment",
	"pingone_application_flow_policy_assignment":    "application flow policy assignment",
	"pingone_notification_template_content":         "notification template",
	"pingone_notification_policy":                   "notification policy",
	"pingone_sign_on_policy":                        "sign-on policy",
	"pingone_sign_on_policy_action":                 "sign-on policy step",
	"pingone_mfa_device_policy":                     "MFA policy",
	"pingone_mfa_fido2_policy":                      "FIDO2 policy",
	"pingone_mfa_settings":                          "MFA settings",
	"pingone_password_policy":                       "password policy",
	"pingone_identity_provider":                     "external identity provider",
	"pingone_resource":                              "resource (API)",
	"pingone_resource_scope":                        "resource scope",
	"pingone_schema_attribute":                      "user attribute",
	"pingone_branding_theme":                        "branding theme",
	"pingone_custom_domain":                         "custom domain",
	"pingone_risk_predictor":                        "risk predictor",
	"pingone_risk_policy":                           "risk policy",
	"pingone_verify_policy":                         "identity verification policy",
	"pingone_authorize_api_service":                 "API service (Authorize)",
}

// resourceLabel is how a type is named on screens.
func resourceLabel(typ string) string {
	if n, ok := friendlyNames[typ]; ok {
		return n
	}
	return catalog.DisplayName(typ)
}

// filterItems searches by name or ID within one type, or lists every item
// of the type when the query is empty.
// filterItems returns the first matches of query within type typ ("" any
// type) and how many there are. An empty query lists every item (of the
// type), sorted by type and name.
func filterItems(items []Item, query, typ string) ([]Item, int) {
	var of []Item
	for _, it := range items {
		if typ == "" || it.Type == typ {
			of = append(of, it)
		}
	}
	if strings.TrimSpace(query) == "" {
		sort.SliceStable(of, func(i, j int) bool {
			a, b := resourceLabel(of[i].Type), resourceLabel(of[j].Type)
			if a != b {
				return a < b
			}
			return strings.ToLower(of[i].Name) < strings.ToLower(of[j].Name)
		})
		n := len(of)
		if n > MaxListResults {
			of = of[:MaxListResults]
		}
		return of, n
	}
	return searchItems(of, query)
}

// plainJobError is the page text for a failed background read or job.
func plainJobError(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "Cancelled."
	case errors.Is(err, context.DeadlineExceeded):
		return "This took too long and was stopped."
	case errors.Is(err, errBranchTaken):
		var bt *branchTakenError
		if errors.As(err, &bt) {
			msg := "The Git branch " + bt.name + " already exists. Go back, type a branch name that is not used yet, and preview again."
			if bt.free != "" {
				msg += " " + bt.free + " is free."
			}
			return msg
		}
		return "The Git branch already exists. Go back, type a branch name that is not used yet, and preview again."
	case errors.Is(err, errNoSourceID):
		return noSourceIDText
	}
	return err.Error()
}

var localSourceRE = regexp.MustCompile(`(?m)^\s*source\s*=\s*"\./([A-Za-z0-9_./-]+)"`)

// localModuleDirs lists the local module folders the root .tf files call
// that are not among have (at most 5).
func localModuleDirs(snap string, have []string) []string {
	known := map[string]bool{}
	for _, d := range have {
		known[d] = true
	}
	entries, err := os.ReadDir(snap)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tf") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(snap, e.Name()))
		if err != nil {
			continue
		}
		for _, m := range localSourceRE.FindAllStringSubmatch(string(src), -1) {
			d := path.Clean(m[1])
			if d == "." || strings.HasPrefix(d, "..") || known[d] || len(out) >= 5 {
				continue
			}
			known[d] = true
			out = append(out, d)
		}
	}
	return out
}

// branchTakenError is a typed branch name that exists already; free is a
// name with a number added that does not, when one was found.
type branchTakenError struct{ name, free string }

func (e *branchTakenError) Error() string { return errBranchTaken.Error() + ": " + e.name }
func (e *branchTakenError) Unwrap() error { return errBranchTaken }

var branchSuffixRE = regexp.MustCompile(`^(.*)-([0-9]+)$`)

// freeBranch finds the first unused name after branch with a number
// added (CIAM-1234 -> CIAM-1234-1, CIAM-1234-1 -> CIAM-1234-2) that the
// branch pattern accepts, or "".
func (s *Server) freeBranch(ctx context.Context, branch string) string {
	base, n := branch, 0
	if m := branchSuffixRE.FindStringSubmatch(branch); m != nil && s.branchRE().MatchString(m[1]) {
		base, n = m[1], atoiOr(m[2], 0)
	}
	for i := n + 1; i <= n+20; i++ {
		c := base + "-" + strconv.Itoa(i)
		if !s.branchRE().MatchString(c) {
			return ""
		}
		if _, exists, err := s.gl.GetBranch(ctx, c); err != nil {
			return ""
		} else if !exists {
			return c
		}
	}
	return ""
}

func atoiOr(s string, def int) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
