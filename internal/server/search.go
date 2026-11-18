package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"pingone-promote/internal/catalog"
	"pingone-promote/internal/exporter"
	"pingone-promote/internal/neutralize"
	"pingone-promote/internal/overlay"
	"pingone-promote/internal/request"
)

const (
	MaxSearchResults = 50
	// MaxListResults caps a listing with no name typed.
	MaxListResults = 200
	catalogMaxAge  = 30 * time.Minute
	// catalogRefresh re-reads every source's item list in the background,
	// so a search rarely waits.
	catalogRefresh = 20 * time.Minute
)

type catalogEntry struct {
	at    time.Time
	items []Item
	// davinci: why the DaVinci items are missing from items ("" when
	// they were read).
	davinci string
}

// listLoad is one background read of a source's item list.
type listLoad struct {
	done chan struct{}
	err  string // set before done is closed
}

func (l *listLoad) finished() bool {
	select {
	case <-l.done:
		return true
	default:
		return false
	}
}

// noSourceIDText explains errNoSourceID to the person and the operator.
const noSourceIDText = "This server does not know the PingOne environment ID of the source environment, so it cannot read it. It reads it from the pingone_environment_id line of the source's .tfvars file on the default branch: check that the file exists where overlayPattern says (default {env}.tfvars at the repository root, named after the source exactly as in \"sources\") and sets pingone_environment_id = \"<environment ID>\". Otherwise the operator adds it under \"sourceEnvironmentIds\" in the server configuration and restarts the server."

// errNoSourceID is a source whose PingOne environment ID is not known.
var errNoSourceID = errors.New("the PingOne environment ID of the source is not known")

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	s.withDraft(w, r, func(w http.ResponseWriter, d *Draft) {
		if !s.applyMain(w, r, d) {
			return
		}
		q := strings.TrimSpace(r.PostForm.Get("q"))
		if len(q) > 200 {
			q = q[:200]
		}
		d.Query = q
		d.TypeFilter = ""
		if t := r.PostForm.Get("t"); t != "" && resourceLabel(t) != "" && strings.HasPrefix(t, "pingone_") {
			d.TypeFilter = t
		}
		d.Updated = s.now()
		d.searched = true
		d.Step = 2
		if d.Source == "" {
			d.searchProblem = "Choose a source environment in step 1 first."
			s.redirect(w, r, draftURL(d, "#results"))
			return
		}
		// The search is answered from the item list. When the list is not
		// in yet, the items page shows a loading notice and refreshes by
		// itself until it is, then shows the results.
		source := d.Source
		d.pendingRefresh = r.PostForm.Get("refresh") == "yes"
		if d.pendingRefresh {
			d.pendingLoad = s.reloadList(source)
		} else {
			if l := d.loads[source]; l != nil && l.finished() && l.err != "" && d.catalogs[source] == nil {
				delete(d.loads, source) // try a failed read again
			}
			d.pendingLoad = s.loadList(d, source)
		}
		d.pending = true
		d.Results, d.ResultsTotal = nil, 0
		s.resolveSearch(d)
		s.redirect(w, r, draftURL(d, "#results"))
	})
}

// resolveSearch finishes a search that waited for the item list. It
// reports whether the list is still being read.
func (s *Server) resolveSearch(d *Draft) (waiting bool) {
	if !d.pending {
		return false
	}
	source, l := d.Source, d.pendingLoad
	c := d.catalogs[source]
	switch {
	case l != nil && !l.finished():
		return true
	case l != nil && l.err != "" && (c == nil || d.pendingRefresh):
		d.Results, d.ResultsTotal = nil, 0
		d.searchProblem = "The items of " + source + " could not be read: " + l.err
	case c == nil:
		// The read finished without a list (a draft switched source):
		// start one and keep waiting.
		if d.pendingLoad = s.loadList(d, source); d.pendingLoad != nil {
			return true
		}
		return false
	default:
		d.Results, d.ResultsTotal = filterItems(c.items, d.Query, d.TypeFilter)
	}
	d.pending, d.pendingLoad, d.pendingRefresh = false, nil, false
	return false
}

// loadList starts reading source's item list in the background unless a
// fresh list or a running read exists, and returns the read (nil when the
// list is fresh). The caller holds s.drafts.mu.
func (s *Server) loadList(_ *Draft, source string) *listLoad {
	if source == "" {
		return nil
	}
	if c := s.drafts.catalogs[source]; c != nil && s.now().Sub(c.at) < catalogMaxAge {
		return nil
	}
	return s.reloadList(source)
}

// warm reads every source's item list when the server starts, and again
// every catalogRefresh for the sources a draft used within catalogMaxAge,
// so searches find it ready without reading idle sources all day. A list
// being re-read stays usable until the new one is in.
func (s *Server) warm() {
	start := func(onlyUsed bool) {
		s.drafts.mu.Lock()
		defer s.drafts.mu.Unlock()
		used := map[string]bool{}
		for _, d := range s.drafts.byID {
			if s.now().Sub(d.Updated) < catalogMaxAge {
				used[d.Source] = true
			}
		}
		for _, src := range s.cfg.Sources {
			if !onlyUsed || used[src] {
				s.reloadList(src)
			}
		}
	}
	start(false)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t := time.NewTicker(catalogRefresh)
		defer t.Stop()
		for {
			select {
			case <-s.base.Done():
				return
			case <-t.C:
				start(true)
			}
		}
	}()
}

// reloadList starts a read of source's item list unless one is running,
// and returns it. The caller holds s.drafts.mu.
func (s *Server) reloadList(source string) *listLoad {
	if source == "" {
		return nil
	}
	if l := s.drafts.loads[source]; l != nil && !l.finished() {
		return l
	}
	l := &listLoad{done: make(chan struct{})}
	s.drafts.loads[source] = l
	run := s.runSearch(source)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ctx, cancel := context.WithTimeout(s.base, JobTimeout)
		defer cancel()
		var err error
		select {
		case s.listSlots <- struct{}{}:
			err = s.safeRun(ctx, run, func(string) {})
			<-s.listSlots
		case <-ctx.Done():
			err = ctx.Err()
		}
		s.drafts.mu.Lock()
		if err != nil {
			l.err = s.redact(plainJobError(err))
			s.log.Warn("item list read failed", "error", l.err)
		}
		close(l.done)
		s.drafts.mu.Unlock()
	}()
	return l
}

// runSearch refreshes the shared resource list for one source environment.
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

// filterItems returns a capped list of matches and the total before truncation.
// An empty query lists resources of the requested type, or all types if typ is empty.
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

func itemOf(r exporter.Resource) Item {
	return Item{Address: r.String(), Type: r.Type, Name: variableName(r), ID: r.ID}
}

// variableName shows a DaVinci variable's context with its name: one name
// can exist as a company and a flow instance variable.
func variableName(r exporter.Resource) string {
	if r.Type != neutralize.TypeVariable {
		return r.Name
	}
	for _, ctx := range []string{"company", "flowInstance", "flow", "user"} {
		if strings.HasSuffix(r.Label, "_"+ctx) {
			return r.Name + " (" + ctx + ")"
		}
	}
	return r.Name
}

type typeOption struct {
	Value, Label string
	Selected     bool
}

// typeOptions lists the types to filter the search by: those in the loaded
// list of the source environment, or nothing before the first search.
func typeOptions(d *Draft) []typeOption {
	c := d.catalogs[d.Source]
	if c == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []typeOption
	for _, it := range c.items {
		if seen[it.Type] {
			continue
		}
		seen[it.Type] = true
		out = append(out, typeOption{Value: it.Type, Label: capitalize(resourceLabel(it.Type)), Selected: it.Type == d.TypeFilter})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Label) < strings.ToLower(out[j].Label) })
	return out
}
