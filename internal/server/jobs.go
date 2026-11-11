package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"pingone-promote/internal/catalog"
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
		case errors.Is(err, context.Canceled):
			job.Err = "Cancelled."
		case errors.Is(err, context.DeadlineExceeded):
			job.Err = "This took too long and was stopped."
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

func (s *Server) env(project gitlab.Project, workspace, head, requestID string, exp request.Exporter) request.Env {
	return request.Env{Workspace: workspace, Layout: s.layout, Rollout: s.cfg.Rollout, AllowedSources: s.cfg.Sources,
		Exporter: exp, Credentials: s.creds, TerraformBin: s.cfg.Tools.Terraform,
		Committer: gitlab.HostedCommitter{S: &gitlab.Submitter{Client: s.gl, Formatter: s.cfg.Tools.Terraform}, DefaultBranch: project.DefaultBranch, BranchPrefix: s.cfg.BranchPrefix},
		Branch:    s.cfg.BranchPrefix + requestID, Head: head, DefaultBranch: project.DefaultBranch, ProjectURL: project.WebURL,
		BranchPrefix: s.cfg.BranchPrefix, NoRequestFile: true, MRLabels: s.cfg.GitLab.MRLabels}
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
		stage("Reading the default branch from GitLab")
		snap, head, err := s.gl.Snapshot(ctx, project.DefaultBranch, s.snapshotDirs())
		if err != nil {
			return err
		}
		stage("Exporting the source environment and merging")
		rec, frozen := newRecorder(s.exporter)
		res := request.Run(ctx, s.env(project, snap, head, req.RequestID, rec), req)
		s.scrub(res)
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
			if rb, exists, err := s.gl.GetBranch(ctx, s.cfg.BranchPrefix+req.RequestID); err != nil || !exists || !strings.Contains(rb.Commit.Message, gitlab.RequestTrailer+req.RequestID) {
				finish(nil, true)
				return nil
			}
		}
		stage("Committing to GitLab")
		res := request.Run(ctx, s.env(project, p.snapshot, p.head, req.RequestID, replayer{f: p.frozen}), req)
		s.scrub(res)
		finish(res, res.Error != nil && res.Error.Code == request.ErrConflict)
		return nil
	}
}

// runSearch loads the source environment's resource list once per draft
// and source (it is cached for a while) and filters it.
func (s *Server) runSearch(d *Draft, source string) func(context.Context, func(string)) error {
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
		list, err := s.exporter.CatalogAllIDs(ctx, creds)
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
		s.drafts.mu.Lock()
		defer s.drafts.mu.Unlock()
		d.catalogs[source] = &catalogEntry{at: s.now(), items: items}
		if d.Source == source {
			d.Results = filterItems(items, d.Query, d.TypeFilter)
		}
		d.Updated = s.now()
		return nil
	}
}

// sourceEnvironmentID reads pingone_environment_id from the source
// environment's tfvars on the default branch, as the engine does.
func (s *Server) sourceEnvironmentID(ctx context.Context, branch, source string) (string, error) {
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
		return "", fmt.Errorf("%s does not set %s", p, neutralize.EnvironmentIDVariable)
	}
	return v.AsString(), nil
}

// selectable reports whether the request accepts the address: the request
// package decides (configuration resources only, never identity data).
func selectable(addr string) bool {
	r := request.Request{RequestVersion: request.Version, RequestID: "probe-request", Source: "probe", Resources: []request.ExcludeRef{{Address: addr}}}
	return r.Validate() == nil
}

func searchItems(items []Item, query string) []Item {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
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
	if len(out) > MaxSearchResults {
		out = out[:MaxSearchResults]
	}
	return out
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
func filterItems(items []Item, query, typ string) []Item {
	if typ == "" {
		return searchItems(items, query)
	}
	var of []Item
	for _, it := range items {
		if it.Type == typ {
			of = append(of, it)
		}
	}
	if strings.TrimSpace(query) == "" {
		if len(of) > 200 {
			of = of[:200]
		}
		return of
	}
	return searchItems(of, query)
}
