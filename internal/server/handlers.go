package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zclconf/go-cty/cty"
	"pingone-promote/internal/exporter"
	"pingone-promote/internal/gitlab"
	"pingone-promote/internal/neutralize"
	"pingone-promote/internal/overlay"
	"pingone-promote/internal/promote"
	"pingone-promote/internal/request"
	"pingone-promote/internal/secretscan"
)

var commitSHA = regexp.MustCompile(`^[0-9a-f]{40,64}$`)

func validRequestID(id string) bool { return request.RequestIDRE.MatchString(id) }
func busy(d *draft) bool {
	return d.State == "queued" || d.State == "searching" || d.State == "previewing" || d.State == "submitting"
}
func (s *Server) route(w http.ResponseWriter, r *http.Request, sess *session) {
	if r.Method == http.MethodGet {
		switch r.URL.Path {
		case "/assets/app.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = w.Write([]byte(appJS))
			return
		case "/assets/style.css":
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
			_, _ = w.Write([]byte(styleCSS))
			return
		case "/":
			s.render(w, "home", page{Session: sess, Sources: s.cfg.Sources, Rollout: s.cfg.Rollout})
			return
		case "/requests":
			s.requestsPage(w, r, sess, "")
			return
		}
	}
	if r.URL.Path == "/drafts" && r.Method == http.MethodPost {
		s.createDraft(w, r, sess)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/requests/") && r.Method == http.MethodGet {
		id := strings.TrimPrefix(r.URL.Path, "/requests/")
		if !validRequestID(id) {
			http.NotFound(w, r)
			return
		}
		s.requestsPage(w, r, sess, id)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 2 || len(parts) > 3 || parts[0] != "drafts" || !validRequestID(parts[1]) {
		http.NotFound(w, r)
		return
	}
	id := parts[1]
	s.mu.Lock()
	d := s.drafts[id]
	if d == nil || d.Owner != sess.ID {
		s.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	d.Expires = time.Now().Add(draftTTL)
	if r.Method == http.MethodGet {
		if len(parts) == 2 {
			copy := cloneDraft(d)
			s.mu.Unlock()
			s.render(w, "draft", page{Session: sess, Draft: copy, Sources: s.cfg.Sources, Rollout: s.cfg.Rollout})
			return
		}
		if parts[2] == "status" {
			status := struct {
				State, Message string
				Revision       uint64
			}{d.State, d.Message, d.Revision}
			s.mu.Unlock()
			b, _ := json.Marshal(status)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(s.redact(b))
			return
		}
		s.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	if len(parts) != 3 {
		s.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	revision, err := strconv.ParseUint(r.PostForm.Get("revision"), 10, 64)
	if err != nil || revision != d.Revision {
		s.mu.Unlock()
		http.Error(w, "Draft changed; reload before continuing", 409)
		return
	}
	if busy(d) {
		s.mu.Unlock()
		http.Error(w, "Draft has work in progress", 409)
		return
	}
	if d.State == "submitted" || d.State == "no_changes" {
		s.mu.Unlock()
		http.Redirect(w, r, "/drafts/"+id, 303)
		return
	}
	if d.State == "partial" && parts[2] != "submit" {
		s.mu.Unlock()
		http.Error(w, "Reconcile the pending GitLab submission before editing this draft", 409)
		return
	}
	var actionErr error
	switch parts[2] {
	case "search":
		d.Query = strings.TrimSpace(r.PostForm.Get("query"))
		d.TypeFilter = r.PostForm.Get("type")
		if len(d.Query) > 200 || len(d.TypeFilter) > 128 {
			actionErr = errors.New("Search is too long")
		} else {
			actionErr = s.enqueue(d, "searching", func(ctx context.Context, rev uint64) { s.search(ctx, id, rev) })
		}
	case "selection":
		actionErr = s.updateSelection(d, r)
		if actionErr == nil {
			d.Revision++
			s.release(d)
			d.Report = nil
			d.State = "editing"
			d.Message = "Selection saved. Preview to review these choices."
		}
	case "preview":
		if err := d.Request.Validate(); err != nil {
			actionErr = errors.New("Select resources before previewing")
		} else {
			s.release(d)
			d.Report = nil
			actionErr = s.enqueue(d, "previewing", func(ctx context.Context, rev uint64) { s.preview(ctx, id, rev) })
		}
	case "submit":
		if (d.State != "ready" && d.State != "partial") || d.Publication == nil {
			actionErr = errors.New("A ready preview is required before submission")
		} else {
			actionErr = s.enqueue(d, "submitting", func(ctx context.Context, rev uint64) { s.submit(ctx, id, rev) })
		}
	default:
		s.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	s.mu.Unlock()
	if actionErr != nil {
		http.Error(w, actionErr.Error(), 409)
		return
	}
	http.Redirect(w, r, "/drafts/"+id, 303)
}
func cloneRequest(in request.Request) request.Request {
	b, _ := json.Marshal(in)
	var out request.Request
	_ = json.Unmarshal(b, &out)
	return out
}
func cloneDraft(in *draft) *draft {
	out := *in
	out.Request = cloneRequest(in.Request)
	out.Catalog = append([]exporter.Resource(nil), in.Catalog...)
	out.Snapshot = nil
	out.Publication = nil
	if in.Report != nil {
		out.Report = nil
		b, _ := json.Marshal(in.Report)
		_ = json.Unmarshal(b, &out.Report)
	}
	return &out
}
func (s *Server) createDraft(w http.ResponseWriter, r *http.Request, sess *session) {
	if s.deps.Exporter == nil || s.deps.Repository == nil {
		http.Error(w, "Exporter and GitLab must be configured", 503)
		return
	}
	source := r.PostForm.Get("source")
	allowed := false
	for _, v := range s.cfg.Sources {
		if source == v {
			allowed = true
		}
	}
	name := strings.TrimSpace(r.PostForm.Get("name"))
	if !allowed || name == "" || len(name) > 100 || !utf8.ValidString(name) || strings.ContainsAny(name, "\r\n\t\x00") {
		http.Error(w, "Choose a source and enter a name of at most 100 characters", 400)
		return
	}
	for _, c := range name {
		if c < 32 || c == 127 {
			http.Error(w, "Invalid name", 400)
			return
		}
	}
	actor := name + " (unverified)"
	if s.deps.Authenticate != nil {
		identity, err := s.deps.Authenticate.Authenticate(r)
		if err != nil || !identity.Verified || identity.Subject == "" || identity.DisplayName == "" {
			http.Error(w, "Authentication required", 401)
			return
		}
		actor = identity.DisplayName
		if len(actor) > 128 || strings.ContainsAny(actor, "\r\n\t\x00") {
			http.Error(w, "Invalid authenticated identity", 401)
			return
		}
	}
	scope := r.PostForm.Get("scope")
	if scope == "" {
		scope = request.ScopeSelected
	}
	if scope != request.ScopeSelected && scope != request.ScopeAll || scope == request.ScopeAll && r.PostForm.Get("confirmAll") != "yes" {
		http.Error(w, "Confirm whole-environment scope explicitly", 400)
		return
	}
	mode := r.PostForm.Get("mode")
	if mode == "" {
		mode = "mr"
	}
	if mode != "mr" && mode != "branch" {
		http.Error(w, "Invalid delivery mode", 400)
		return
	}
	id, err := randomID()
	if err != nil {
		http.Error(w, "Cannot create draft", 503)
		return
	}
	upstream := r.PostForm.Get("upstream") == "yes"
	d := &draft{ID: id, Owner: sess.ID, State: "editing", Revision: 1, Expires: time.Now().Add(draftTTL), Request: request.Request{RequestVersion: request.Version, RequestID: id, RequestedBy: actor, Source: source, Scope: scope, ConfirmAll: scope == request.ScopeAll, Mode: mode, IncludeUpstream: &upstream}}
	s.mu.Lock()
	count := 0
	for _, v := range s.drafts {
		if v.Owner == sess.ID {
			count++
		}
	}
	if count >= maxSessionDrafts || len(s.drafts) >= maxDrafts {
		s.mu.Unlock()
		http.Error(w, "Draft capacity reached; existing drafts expire after one hour", 429)
		return
	}
	s.drafts[id] = d
	if v := s.sessions[sess.ID]; v != nil {
		v.Name = name
	}
	s.mu.Unlock()
	http.Redirect(w, r, "/drafts/"+id, 303)
}

// enqueue is called with the server mutex held. HTTP cancellation never owns
// background work; only the server deadline and Close can cancel it.
func (s *Server) enqueue(d *draft, state string, fn func(context.Context, uint64)) error {
	// Starting background work consumes the rendered revision too: a form
	// opened before a preview must not become valid again when it finishes.
	rev := d.Revision + 1
	j := job{draftID: d.ID, revision: rev, run: func(ctx context.Context) { fn(ctx, rev) }}
	select {
	case s.jobs <- j:
		d.Revision = rev
		d.State = state
		d.Message = "Work in progress. This page refreshes when it completes."
		return nil
	default:
		return errors.New("Worker queue is full; retry shortly")
	}
}
func (s *Server) sourceID(snap Snapshot, source string) (string, error) {
	p, err := s.layout.Overlay(source)
	if err != nil {
		return "", err
	}
	raw, ok, err := promote.DirRepo(snap.Workspace).Read(p)
	if err != nil || !ok {
		return "", errors.New("source overlay missing")
	}
	o, err := overlay.ParseOverlay(raw, p)
	if err != nil {
		return "", err
	}
	v, ok := o.Values[neutralize.EnvironmentIDVariable]
	if !ok || v.Type() != cty.String || v.IsNull() || !v.IsKnown() {
		return "", errors.New("source environment ID missing")
	}
	return v.AsString(), nil
}
func (s *Server) credentials(id string) exporter.Credentials {
	return exporter.Credentials{ClientID: s.secrets.WorkerClientID, ClientSecret: s.secrets.WorkerClientSecret, WorkerEnvironmentID: s.cfg.PingOne.WorkerEnvironmentID, ExportEnvironmentID: id, RegionCode: s.cfg.PingOne.RegionCode}
}
func (s *Server) jobInput(id string, rev uint64) (request.Request, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.drafts[id]
	if d == nil || d.Revision != rev {
		return request.Request{}, false
	}
	return cloneRequest(d.Request), true
}
func (s *Server) jobFailure(id string, rev uint64, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d := s.drafts[id]; d != nil && d.Revision == rev {
		d.State = "failed"
		d.Message = msg
	}
}
func (s *Server) search(ctx context.Context, id string, rev uint64) {
	req, ok := s.jobInput(id, rev)
	if !ok {
		return
	}
	snap, err := s.deps.Repository.Snapshot(ctx, "", s.layout)
	if err != nil {
		s.jobFailure(id, rev, "Could not read the GitLab configuration snapshot.")
		return
	}
	if snap.Close != nil {
		defer snap.Close()
	}
	sourceID, err := s.sourceID(snap, req.Source)
	if err != nil {
		s.jobFailure(id, rev, "The source overlay must define pingone_environment_id.")
		return
	}
	s.mu.Lock()
	d := s.drafts[id]
	if d == nil || d.Revision != rev {
		s.mu.Unlock()
		return
	}
	query, typeFilter := d.Query, d.TypeFilter
	s.mu.Unlock()
	ids := req.LookupIDs()
	ctx = exporter.WithResourceIDs(ctx, ids)
	catalog, err := s.deps.Exporter.CatalogAllIDs(ctx, s.credentials(sourceID))
	if err == nil && len(query) == 36 && exporter.LooksLikeID(query) && (typeFilter == "" || typeFilter == "pingone_image") {
		found := false
		for _, item := range catalog {
			if strings.EqualFold(item.ID, query) {
				found = true
				break
			}
		}
		if !found {
			lookupCtx := exporter.WithResourceIDs(ctx, append(ids, exporter.TypedResourceID{Type: "pingone_image", ID: query}))
			supplemented, lookupErr := s.deps.Exporter.CatalogAllIDs(lookupCtx, s.credentials(sourceID))
			if lookupErr == nil {
				catalog = supplemented
			} else if !errors.Is(lookupErr, exporter.ErrExplicitResourceNotFound) {
				err = lookupErr
			}
		}
	}
	if err != nil || len(catalog) > 2000 {
		s.jobFailure(id, rev, "Resource search failed or exceeded the 2,000-resource limit.")
		return
	}
	bytes := 0
	for _, item := range catalog {
		bytes += len(item.Name) + len(item.ID) + len(item.Type) + len(item.Label)
	}
	if bytes > 4<<20 {
		s.jobFailure(id, rev, "Resource catalog exceeded the size limit.")
		return
	}
	sort.Slice(catalog, func(i, j int) bool { return catalog[i].Address.String() < catalog[j].Address.String() })
	s.mu.Lock()
	defer s.mu.Unlock()
	if d := s.drafts[id]; d != nil && d.Revision == rev {
		d.Catalog = catalog
		d.State = "editing"
		d.Message = "Search complete. Select resources and save the selection before previewing."
	}
}
func (s *Server) updateSelection(d *draft, r *http.Request) error {
	req := cloneRequest(d.Request)
	req.Resources = nil
	req.Exclude = nil
	allowed := map[string]exporter.Resource{}
	for _, v := range d.Catalog {
		allowed[v.Address.String()] = v
	}
	selector := func(a string) request.ExcludeRef {
		if item := allowed[a]; item.Type == "pingone_image" {
			return request.ExcludeRef{Type: item.Type, ID: item.ID}
		}
		return request.ExcludeRef{Address: a}
	}
	for _, a := range r.PostForm["resource"] {
		if _, ok := allowed[a]; !ok {
			return errors.New("Selection must come from the source catalog")
		}
		req.Resources = append(req.Resources, selector(a))
	}
	for _, a := range r.PostForm["exclude"] {
		if _, ok := allowed[a]; !ok {
			return errors.New("Exclusion must come from the source catalog")
		}
		req.Exclude = append(req.Exclude, selector(a))
	}
	req.DependencyChoices = map[string]string{}
	req.EnvValues = map[string]map[string]json.RawMessage{}
	req.ImportIDs = map[string]map[string]string{}
	req.SecretMappings = map[string]string{}
	if d.Report != nil {
		for _, c := range d.Report.Changes {
			if c.Role != "dependency" && c.Role != "environment" && c.Role != "selected" {
				continue
			}
			choice := r.PostForm.Get("choice." + c.Address)
			if choice == "" {
				continue
			}
			if choice == "keep" && !canKeep(c) {
				return errors.New("New dependencies cannot keep a Git version")
			}
			if choice != "update" && choice != "keep" && choice != "exclude" {
				return errors.New("Invalid dependency choice")
			}
			req.DependencyChoices[c.Address] = choice
		}
		for env, values := range d.Request.EnvValues {
			for key := range values {
				raw := strings.TrimSpace(r.PostForm.Get("value." + env + "." + key))
				if raw == "" {
					continue
				}
				if !json.Valid([]byte(raw)) {
					return errors.New("Environment values must be JSON")
				}
				if req.EnvValues[env] == nil {
					req.EnvValues[env] = map[string]json.RawMessage{}
				}
				req.EnvValues[env][key] = json.RawMessage(raw)
			}
		}
		for env, bs := range d.Report.Blockers {
			if env == request.RequestBlockers {
				continue
			}
			for _, b := range bs {
				if b.Code == promote.BlockMissingImport && b.Address != "" {
					if id := strings.TrimSpace(r.PostForm.Get("adopt." + env + "." + b.Address)); id != "" {
						if req.ImportIDs[env] == nil {
							req.ImportIDs[env] = map[string]string{}
						}
						req.ImportIDs[env][b.Address] = id
					}
					continue
				}
				if b.Code != promote.BlockMissingValue || b.Var == "" {
					continue
				}
				raw := strings.TrimSpace(r.PostForm.Get("value." + env + "." + b.Var))
				if raw == "" {
					continue
				}
				if !json.Valid([]byte(raw)) {
					return errors.New("Environment values must be JSON, such as a quoted string or number")
				}
				if req.EnvValues[env] == nil {
					req.EnvValues[env] = map[string]json.RawMessage{}
				}
				req.EnvValues[env][b.Var] = json.RawMessage(raw)
			}
		}
		for _, secret := range d.Report.RequiredSecrets {
			name := strings.TrimSpace(r.PostForm.Get("secret." + secret.Key))
			if name != "" {
				req.SecretMappings[secret.Key] = name
			}
		}
	}
	// Preserve adoption IDs and accept edits only to previously offered fields.
	for env, ids := range d.Request.ImportIDs {
		if req.ImportIDs[env] == nil {
			req.ImportIDs[env] = map[string]string{}
		}
		for a, id := range ids {
			if edit := strings.TrimSpace(r.PostForm.Get("adopt." + env + "." + a)); edit != "" {
				id = edit
			}
			if _, ok := req.ImportIDs[env][a]; !ok {
				req.ImportIDs[env][a] = id
			}
		}
	}
	// Preserve already supplied values through repeated readiness previews.
	for env, values := range d.Request.EnvValues {
		if req.EnvValues[env] == nil {
			req.EnvValues[env] = map[string]json.RawMessage{}
		}
		for k, v := range values {
			if _, ok := req.EnvValues[env][k]; !ok {
				req.EnvValues[env][k] = v
			}
		}
	}
	for k, v := range d.Request.SecretMappings {
		if req.SecretMappings[k] == "" {
			req.SecretMappings[k] = v
		}
	}
	for k, v := range d.Request.DependencyChoices {
		if req.DependencyChoices[k] == "" {
			req.DependencyChoices[k] = v
		}
	}
	if err := req.Validate(); err != nil {
		return errors.New("Selection or values failed validation; secrets accept names only")
	}
	d.Request = req
	return nil
}

type captureCommitter struct{ publication *gitlab.BranchCommit }

func (c *captureCommitter) CommitToBranch(_ context.Context, bc gitlab.BranchCommit) (gitlab.Result, error) {
	b, _ := json.Marshal(bc)
	var clone gitlab.BranchCommit
	_ = json.Unmarshal(b, &clone)
	c.publication = &clone
	return gitlab.Result{Branch: bc.Branch}, nil
}
func (c *captureCommitter) OpenMergeRequest(context.Context, string) (*gitlab.MergeRequest, error) {
	return nil, nil
}
func (s *Server) preview(ctx context.Context, id string, rev uint64) {
	req, ok := s.jobInput(id, rev)
	if !ok {
		return
	}
	snap, err := s.deps.Repository.Snapshot(ctx, "", s.layout)
	if err != nil {
		s.jobFailure(id, rev, "Could not read the GitLab configuration snapshot.")
		return
	}
	retained := false
	defer func() {
		if !retained && snap.Close != nil {
			_ = snap.Close()
		}
	}()
	capture := &captureCommitter{}
	res := request.Run(ctx, request.Env{Workspace: snap.Workspace, Layout: s.layout, Rollout: s.cfg.Rollout, AllowedSources: s.cfg.Sources, Exporter: s.deps.Exporter, Credentials: s.credentials(""), TerraformBin: s.deps.TerraformBin, Committer: capture, Branch: s.cfg.BranchPrefix + id, Head: snap.Head, DefaultBranch: snap.DefaultBranch, ProjectURL: snap.ProjectURL, BranchPrefix: s.cfg.BranchPrefix, NoRequestFile: true, MRLabels: s.cfg.GitLab.MRLabels}, &req)
	unsafe := len(secretscan.ScanText("diff", []byte(res.Diff))) > 0
	size := len(res.Diff)
	for _, bs := range res.Blockers {
		for _, b := range bs {
			if b.Code == promote.BlockSecretFinding || b.Code == promote.BlockSecretInOverlay {
				unsafe = true
			}
		}
	}
	if capture.publication != nil {
		for _, text := range []string{capture.publication.CommitMessage, capture.publication.Title, capture.publication.Description, strings.Join(capture.publication.Labels, "\n")} {
			if len(secretscan.ScanText("publication", []byte(text))) > 0 {
				unsafe = true
			}
			for _, v := range []string{s.secrets.WorkerClientID, s.secrets.WorkerClientSecret, s.secrets.GitLabToken} {
				if v != "" && strings.Contains(text, v) {
					unsafe = true
				}
			}
		}
		files := map[string][]byte{}
		for _, f := range capture.publication.Files {
			files[f.Path] = f.Content
			size += len(f.Content)
			for _, v := range []string{s.secrets.WorkerClientID, s.secrets.WorkerClientSecret, s.secrets.GitLabToken} {
				if v != "" && strings.Contains(string(f.Content), v) {
					unsafe = true
				}
			}
		}
		unsafe = unsafe || len(secretscan.ScanFiles(files)) > 0
	}
	if size > maxArtifact {
		unsafe = true
	}
	if unsafe {
		res.Diff = ""
		res.Warnings = nil
		res.Error = &request.ErrorOut{Code: request.ErrBlocked, Type: "merge", Message: "Preview content withheld: secret findings or artifact size limit require operator review."}
		res.Status = request.StatusBlocked
		capture.publication = nil
	}
	if res.Error != nil {
		if !unsafe {
			res.Error.Message = safeError(res.Error.Code)
		}
		res.Error.Candidates = nil
	}
	if res.Status == request.StatusSubmitted {
		res.Status = request.StatusPreviewed
	}
	res.DryRun = true
	// Scrub at the model boundary, before template escaping.
	raw, _ := json.Marshal(res)
	raw = s.redact(raw)
	var safe request.Result
	if err := json.Unmarshal(raw, &safe); err != nil {
		s.jobFailure(id, rev, "Preview could not be safely rendered.")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.drafts[id]
	if d == nil || d.Revision != rev {
		return
	}
	d.Report = &safe
	d.Message = "Review the preview and readiness checks."
	d.State = "blocked"
	if safe.Status == request.StatusNoChanges {
		d.State = "no_changes"
		d.Message = "The repository already matches. Nothing was written."
	} else if safe.Error == nil && capture.publication != nil {
		d.State = "ready"
		d.Publication = capture.publication
		d.Snapshot = &snap
		retained = true
	} else if safe.Status == request.StatusFailed {
		d.State = "failed"
	} else {
		d.State = "needs_values"
	}
}
func safeError(code string) string {
	switch code {
	case request.ErrConflict:
		return "The default branch changed after preview. Preview again before submitting."
	case request.ErrPartial:
		return "GitLab may contain a partial submission. Inspect Requests before retrying."
	case request.ErrBlocked:
		return "Readiness checks blocked submission. Resolve the listed fields and preview again."
	default:
		return "Preparation failed. Check source configuration and exporter availability; raw tool diagnostics are withheld."
	}
}
func (s *Server) submit(ctx context.Context, id string, rev uint64) {
	s.mu.Lock()
	d := s.drafts[id]
	if d == nil || d.Revision != rev || d.Publication == nil || d.Snapshot == nil {
		s.mu.Unlock()
		return
	}
	bc := *d.Publication
	head := d.Snapshot.Head
	s.mu.Unlock()
	out, err := s.deps.Repository.Publish(ctx, head, bc.Branch, bc)
	s.mu.Lock()
	defer s.mu.Unlock()
	d = s.drafts[id]
	if d == nil || d.Revision != rev {
		return
	}
	if err != nil {
		var conflict *gitlab.ConflictError
		var partial *gitlab.PartialError
		var done *gitlab.AlreadyCommittedError
		switch {
		case errors.As(err, &done):
			out = done.Result
		case errors.As(err, &conflict):
			d.State = "conflict"
			d.Message = safeError(request.ErrConflict)
			s.release(d)
			return
		case errors.As(err, &partial):
			d.State = "partial"
			d.Message = safeError(request.ErrPartial)
			if d.Report != nil {
				d.Report.CommitID = partial.CommitID
			}
			return
		default:
			d.State = "partial"
			d.Message = "Submission could not be confirmed. Inspect Requests; the reviewed publication is retained for reconciliation."
			return
		}
	}
	d.State = "submitted"
	d.Message = "Published. The enterprise pipeline owns deployment; inspect its plans before applying."
	d.Report.Status = request.StatusSubmitted
	d.Report.DryRun = false
	d.Report.CommitID = out.CommitID
	if out.MR != nil {
		d.Report.MRIid = out.MR.IID
		d.Report.MRUrl = s.safeLink(out.MR.WebURL)
	}
	s.release(d)
}
func (s *Server) safeLink(raw string) string {
	if g, ok := s.deps.Repository.(*gitLabRepository); ok {
		return g.safeURL(raw)
	}
	return ""
}
func (s *Server) requestsPage(w http.ResponseWriter, r *http.Request, sess *session, id string) {
	if s.deps.Repository == nil {
		http.Error(w, "GitLab is not configured", 503)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	records, err := s.deps.Repository.Requests(ctx)
	if err != nil {
		http.Error(w, "Could not read requests from GitLab", 502)
		return
	}
	p := page{Session: sess, Requests: records, Rollout: s.cfg.Rollout}
	if id != "" {
		found := false
		for _, v := range records {
			if v.ID == id {
				p.Record = &v
				found = true
				break
			}
		}
		if !found {
			http.NotFound(w, r)
			return
		}
		if p.Record.State == "merged" {
			p.RolloutRecords, err = s.deps.Repository.Rollout(ctx, p.Record.Commit)
			if err != nil {
				p.Message = "Rollout status could not be read."
			}
		} else {
			p.Message = "Rollout is unknown until this request is merged and a pipeline is linked to its exact commit."
		}
	}
	s.render(w, "requests", p)
}
