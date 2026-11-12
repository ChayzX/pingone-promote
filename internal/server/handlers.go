package server

import (
	"encoding/json"
	"net/http"
	"pingone-promote/internal/catalog"
	"sort"
	"strconv"
	"strings"
	"time"

	"pingone-promote/internal/request"
)

// Handlers only translate between forms, drafts and the request engine;
// all resource-specific work happens in the engine.

func (s *Server) redirect(w http.ResponseWriter, r *http.Request, to string) {
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func draftURL(d *Draft, suffix string) string { return "/drafts/" + d.ID + suffix }

// withDraft runs fn with this session's draft, holding the drafts lock.
// fn writes to a buffer that is sent after the lock is released, so a slow
// client never holds up other requests.
func (s *Server) withDraft(w http.ResponseWriter, r *http.Request, fn func(w http.ResponseWriter, d *Draft)) {
	sess := sessionFrom(r)
	s.drafts.mu.Lock()
	var d *Draft
	if sess != nil {
		d = s.drafts.get(sess.id, r.PathValue("id"))
	}
	if d == nil {
		s.drafts.mu.Unlock()
		s.renderStatus(w, r, http.StatusNotFound, "Not found", "This draft does not exist in this browser. Drafts expire after a while and belong to the browser that started them.")
		return
	}
	buf := newBufferedWriter()
	func() {
		// A panic in fn still releases the lock; protect turns it into a
		// plain 500 page.
		defer s.drafts.mu.Unlock()
		fn(buf, d)
	}()
	buf.flushTo(w)
}

type homeView struct {
	Drafts []draftRow
}

type draftRow struct {
	ID, RequestID, Source, Title, State string
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	s.drafts.mu.Lock()
	var rows []draftRow
	for _, d := range s.drafts.list(sess.id) {
		state := "Draft"
		switch {
		case d.job != nil && d.job.Running:
			state = "Working"
		case d.submitted != nil && d.submitted.Error == nil:
			state = "Submitted"
		case d.preview != nil:
			state = "Previewed"
		}
		title := d.Title
		if title == "" {
			title = "(no title)"
		}
		rows = append(rows, draftRow{ID: d.ID, RequestID: d.RequestID, Source: d.Source, Title: title, State: state})
	}
	s.drafts.mu.Unlock()
	s.render(w, r, http.StatusOK, "home", "Drafts", homeView{Drafts: rows}, false)
}

func (s *Server) newDraft(w http.ResponseWriter, r *http.Request) {
	d, err := s.drafts.create(sessionFrom(r).id, s.cfg.Sources)
	if err != nil {
		s.renderStatus(w, r, http.StatusTooManyRequests, "Too many drafts", err.Error())
		return
	}
	s.drafts.mu.Lock()
	s.preloadCatalog(d)
	s.drafts.mu.Unlock()
	s.redirect(w, r, draftURL(d, ""))
}

type draftView struct {
	Types   []typeOption
	D       *Draft
	Sources []string
	Rollout []string
	Notice  string
	Problem string
	Running bool
	// Loading: the source's item list is being read in the background.
	Loading      bool
	HasPreview   bool
	Results      []itemView
	Selected     []itemView
	Excluded     []itemView
	ListLoaded   bool
	SearchedOnce bool
	// Sections are the families that can be copied whole.
	Sections []sectionOption
	// Branch is the Git branch a submit creates; TypedBranch: the person
	// types it (BranchPattern, BranchExample explain the rule).
	Branch        string
	TypedBranch   bool
	BranchPattern string
	BranchExample string
}

type sectionOption struct {
	Value, Label string
	Checked      bool
}

func sectionOptions(d *Draft) []sectionOption {
	var out []sectionOption
	for _, f := range catalog.Families {
		if l, ok := catalog.FamilyLabels[f]; ok {
			out = append(out, sectionOption{Value: f, Label: l, Checked: contains(d.Families, f)})
		}
	}
	return out
}

type itemView struct {
	Address, Kind, Name, ID string
	Selected, Excluded      bool
}

func viewItems(items []Item, d *Draft) []itemView {
	sel, exc := map[string]bool{}, map[string]bool{}
	for _, it := range d.Selected {
		sel[it.Address] = true
	}
	for _, it := range d.Excluded {
		exc[it.Address] = true
	}
	out := make([]itemView, 0, len(items))
	for _, it := range items {
		out = append(out, itemView{Address: it.Address, Kind: resourceLabel(it.Type), Name: it.Name, ID: it.ID, Selected: sel[it.Address], Excluded: exc[it.Address]})
	}
	return out
}

func (s *Server) editDraft(w http.ResponseWriter, r *http.Request) {
	s.withDraft(w, r, func(w http.ResponseWriter, d *Draft) {
		v := draftView{D: d, Sources: s.cfg.Sources, Rollout: s.cfg.Rollout, Notice: d.notice, Problem: d.problem,
			Running: d.job != nil && d.job.Running && d.job.Kind != "search", Loading: d.job != nil && d.job.Running && d.job.Kind == "search",
			HasPreview: d.preview != nil,
			Results:    viewItems(d.Results, d), Selected: viewItems(d.Selected, d), Excluded: viewItems(d.Excluded, d),
			ListLoaded: d.catalogs[d.Source] != nil, SearchedOnce: d.Query != "" || d.TypeFilter != "", Types: typeOptions(d),
			Sections: sectionOptions(d), Branch: s.branchFor(d),
			TypedBranch: s.branchRE() != nil, BranchPattern: s.cfg.BranchPattern, BranchExample: s.cfg.BranchExample}
		d.notice, d.problem = "", ""
		s.render(w, r, http.StatusOK, "draft", "New request", v, false)
	})
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// saveDraft stores the main form. With action=preview it then starts a
// preview.
// applyMain stores step 1 of the request form; ok is false when the page
// was redirected because of a problem.
func (s *Server) applyMain(w http.ResponseWriter, r *http.Request, d *Draft) (ok bool) {
	person, idErr := s.identity.Who(r)
	f := r.PostForm
	if !f.Has("source") {
		return true // an older form without step 1
	}
	{
		d.Name = strings.TrimSpace(f.Get("name"))
		if idErr == nil {
			d.RequestedBy = person.Recorded()
		} else {
			d.RequestedBy = ""
		}
		if src := f.Get("source"); src != d.Source {
			if !contains(s.cfg.Sources, src) {
				d.problem = "Choose one of the listed source environments."
				s.redirect(w, r, draftURL(d, ""))
				return false
			}
			if len(d.Selected)+len(d.Excluded) > 0 {
				d.notice = "The source environment changed, so the selected and excluded lists were cleared."
			}
			d.Source, d.Selected, d.Excluded, d.Results, d.Choices = src, nil, nil, nil, map[string]string{}
			s.preloadCatalog(d)
		}
		d.Whole = f.Get("scope") == request.ScopeAll
		d.Families = nil
		if f.Get("scope") == "sections" {
			for _, fam := range catalog.Families {
				if catalog.ExportableFamily(fam) && contains(f["family"], fam) {
					d.Families = append(d.Families, fam)
				}
			}
			if len(d.Families) == 0 {
				d.notice = "Tick at least one section to copy whole sections."
			}
		}
		d.ConfirmAll = d.Whole && f.Get("confirm_all") == "yes"
		d.Upstream = f.Get("upstream") == "yes"
		if m := f.Get("mode"); m == "mr" || m == "branch" {
			d.Mode = m
		}
		if s.branchRE() != nil && f.Has("branch") {
			if b := strings.TrimSpace(f.Get("branch")); len(b) <= 255 {
				d.Branch = b
			}
		}
		d.Title = strings.TrimSpace(f.Get("title"))
		d.Description = f.Get("description")
		d.Updated = s.now()
		if idErr != nil && f.Get("action") == "preview" {
			d.problem = idErr.Error()
			s.redirect(w, r, draftURL(d, ""))
			return false
		}
	}
	return true
}

func (s *Server) saveDraft(w http.ResponseWriter, r *http.Request) {
	s.withDraft(w, r, func(w http.ResponseWriter, d *Draft) {
		f := r.PostForm
		if !s.applyMain(w, r, d) {
			return
		}
		if _, idErr := s.identity.Who(r); idErr != nil {
			d.problem = idErr.Error()
			s.redirect(w, r, draftURL(d, ""))
			return
		}
		if f.Get("action") != "preview" {
			d.notice = "Saved."
			s.redirect(w, r, draftURL(d, ""))
			return
		}
		s.preview(w, r, d, draftURL(d, ""))
	})
}

// preview validates the draft and starts a preview job; problems go back
// to the page named by back.
func (s *Server) preview(w http.ResponseWriter, r *http.Request, d *Draft, back string) {
	s.awaitCatalog(r, d)
	req, err := s.buildRequest(d, true)
	if err == nil {
		err = s.startJob(d, "preview", s.runPreview(d, req))
	}
	if err != nil {
		d.problem = s.redact(err.Error())
		s.redirect(w, r, back)
		return
	}
	s.redirect(w, r, draftURL(d, "/job"))
}

func (s *Server) startPreview(w http.ResponseWriter, r *http.Request) {
	s.withDraft(w, r, func(w http.ResponseWriter, d *Draft) { s.preview(w, r, d, draftURL(d, "")) })
}

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
		if d.Source == "" {
			d.problem = "Choose a source environment first."
			s.redirect(w, r, draftURL(d, ""))
			return
		}
		fresh := func() *catalogEntry {
			if c := d.catalogs[d.Source]; c != nil && s.now().Sub(c.at) < catalogMaxAge {
				return c
			}
			return nil
		}
		if r.PostForm.Get("refresh") == "yes" {
			delete(d.catalogs, d.Source)
		}
		// The results always appear on this page: the list is read now
		// (or the background read started with the draft is awaited).
		if fresh() == nil && !(d.job != nil && d.job.Running && d.job.Kind == "search") {
			if err := s.startJob(d, "search", s.runSearch(d, d.Source)); err != nil {
				d.problem = err.Error()
				s.redirect(w, r, draftURL(d, ""))
				return
			}
		}
		s.awaitCatalog(r, d)
		switch c := fresh(); {
		case c != nil:
			d.Results = filterItems(c.items, q, d.TypeFilter)
		case d.job != nil && d.job.Running:
			d.notice = "Still reading the items of " + d.Source + ". Search again in a moment."
		case d.job != nil && d.job.Err != "":
			d.problem = "The items of " + d.Source + " could not be read: " + d.job.Err
		}
		s.redirect(w, r, draftURL(d, "#results"))
	})
}

// pick adds an item to, or removes it from, the selected or excluded list.
func (s *Server) pick(w http.ResponseWriter, r *http.Request) {
	s.withDraft(w, r, func(w http.ResponseWriter, d *Draft) {
		addr, op := r.PostForm.Get("address"), r.PostForm.Get("op")
		remove := func(list []Item) []Item {
			out := list[:0:0]
			for _, it := range list {
				if it.Address != addr {
					out = append(out, it)
				}
			}
			return out
		}
		find := func() (Item, bool) {
			if c := d.catalogs[d.Source]; c != nil {
				for _, it := range c.items {
					if it.Address == addr {
						return it, true
					}
				}
			}
			return Item{}, false
		}
		switch op {
		case "select", "exclude":
			it, ok := find()
			if !ok {
				d.problem = "That resource is not in the source environment's list. Search again."
				break
			}
			if len(d.Selected)+len(d.Excluded) >= MaxSelected {
				d.problem = "The lists are full."
				break
			}
			d.Selected, d.Excluded = remove(d.Selected), remove(d.Excluded)
			if op == "select" {
				d.Selected = append(d.Selected, it)
			} else {
				d.Excluded = append(d.Excluded, it)
			}
		case "unselect":
			d.Selected = remove(d.Selected)
		case "unexclude":
			d.Excluded = remove(d.Excluded)
		default:
			d.problem = "Unknown action."
		}
		d.Updated = s.now()
		s.redirect(w, r, draftURL(d, "#lists"))
	})
}

type jobView struct {
	D       *Draft
	Kind    string
	Stage   string
	Err     string
	Running bool
	Back    string
	// AutoRefresh is off after "Stop refreshing" (?refresh=0), so the
	// page meets WCAG 2.2.1 (timing adjustable).
	AutoRefresh bool
}

var jobTitles = map[string]string{"search": "Listing resources", "preview": "Preparing the preview", "submit": "Submitting"}

func (s *Server) jobPage(w http.ResponseWriter, r *http.Request) {
	s.withDraft(w, r, func(w http.ResponseWriter, d *Draft) {
		j := d.job
		if j == nil {
			s.redirect(w, r, draftURL(d, ""))
			return
		}
		if !j.Running && j.Err == "" {
			switch j.Kind {
			case "search":
				s.redirect(w, r, draftURL(d, "#results"))
			case "preview":
				s.redirect(w, r, draftURL(d, "/preview"))
			default:
				s.redirect(w, r, draftURL(d, "/result"))
			}
			return
		}
		back := draftURL(d, "")
		if j.Kind == "submit" {
			back = draftURL(d, "/preview")
		}
		auto := j.Running && r.URL.Query().Get("refresh") != "0"
		s.render(w, r, http.StatusOK, "job", jobTitles[j.Kind], jobView{D: d, Kind: j.Kind, Stage: j.Stage, Err: j.Err, Running: j.Running, Back: back, AutoRefresh: auto}, auto)
	})
}

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	s.withDraft(w, r, func(w http.ResponseWriter, d *Draft) {
		if d.job != nil && d.job.Running && d.job.cancel != nil {
			d.job.cancel()
		}
		s.redirect(w, r, draftURL(d, "/job"))
	})
}

type envView struct {
	// Summary replaces the blocker list when every blocker is a value or
	// an ID filled in on the page.
	Summary  string
	Env      string
	Label    string
	Ready    bool
	Blockers []blockerView
	Values   []valueField
	Imports  []valueField
}

type valueField struct {
	Name, Key, Value string
	// Label and Hint explain the value in plain language.
	Label, Hint string
	// List values (URL lists) are typed one per line.
	List bool
}

type changeView struct {
	request.ChangeOut
	Kind       string
	Name       string
	StatusText string
	ActionText string
	Dependency bool
	Choice     string
}

type typeView struct {
	Kind string
	request.TypeCounts
}

type previewView struct {
	D           *Draft
	R           *request.Result
	Problem     string
	Fresh       bool
	CanSubmit   bool
	Running     bool
	Types       []typeView
	Changes     []changeView
	Envs        []envView
	HasBlockers bool
	Unchanged   int
	// AlmostReady: the only blockers are values the person fills in here.
	AlmostReady bool
	NeedEnvs    []string
	Skipped     []string
	Warnings    []string
	// Failed: the preview stopped with an error, so nothing was checked.
	Failed bool
	// WhyNot says why the request cannot be submitted yet.
	WhyNot string
	Branch string
}

var statusText = map[string]string{"new": "new", "changed-in-source": "changed in source", "git-newer": "Git is newer",
	"changed-in-both": "changed in both", "matches": "matches", "not-compared": "not compared"}

var actionText = map[string]string{"added": "added", "updated": "updated", "kept_differs": "Git version kept", "unchanged": "no change",
	"excluded": "excluded", "moved": "moved"}

func (s *Server) previewPage(w http.ResponseWriter, r *http.Request) {
	s.withDraft(w, r, func(w http.ResponseWriter, d *Draft) {
		p := d.preview
		if p == nil {
			s.redirect(w, r, draftURL(d, ""))
			return
		}
		res := p.result
		v := previewView{D: d, R: res, Problem: d.problem, Running: d.job != nil && d.job.Running}
		d.problem = ""
		if req, err := s.buildRequest(d, true); err == nil && fingerprint(*req) == p.fingerprint {
			v.Fresh = true
		}
		for t, c := range res.Summary.ByType {
			v.Types = append(v.Types, typeView{Kind: capitalize(resourceLabel(t)), TypeCounts: c})
		}
		sort.Slice(v.Types, func(i, j int) bool { return v.Types[i].Kind < v.Types[j].Kind })
		for _, c := range res.Changes {
			if d.Whole && c.Action == "unchanged" {
				v.Unchanged++
				continue
			}
			typ, _, _ := strings.Cut(c.Address, ".")
			cv := changeView{ChangeOut: c, Kind: resourceLabel(typ), Name: itemName(d, c.Address), StatusText: statusText[c.Status], ActionText: actionText[c.Action],
				Dependency: c.Role == "dependency" && (c.Action != "unchanged" || d.Choices[c.Address] != ""), Choice: d.Choices[c.Address]}
			cv.Kind = capitalize(cv.Kind)
			if cv.StatusText == "" {
				cv.StatusText = c.Status
			}
			if cv.ActionText == "" {
				cv.ActionText = c.Action
			}
			v.Changes = append(v.Changes, cv)
		}
		envs := append([]string{d.Source}, s.cfg.Rollout...)
		if _, ok := res.Blockers[request.RequestBlockers]; ok {
			envs = append(envs, request.RequestBlockers)
		}
		for _, env := range envs {
			ev := envView{Env: env, Label: env}
			if env == request.RequestBlockers {
				ev.Label = "Whole request"
			}
			ev.Ready = len(res.Blockers[env]) == 0
			for _, b := range res.Blockers[env] {
				ev.Blockers = append(ev.Blockers, blockerText(d, res, env, b))
				switch {
				case b.Code == "missing_value" && b.Var != "":
					label, hint := valueLabel(d, res, b.Var)
					f := valueField{Name: "v:" + env + ":" + b.Var, Key: b.Var, Value: d.Values[env][b.Var], Label: capitalize(label), Hint: hint, List: isListValue(d, b.Var)}
					if f.List {
						f.Value = jsonToLines(f.Value)
						f.Hint = "One per line."
						if sv := res.Variables[b.Var].SourceValue; sv != "" {
							f.Hint += " In " + d.Source + ": " + strings.ReplaceAll(jsonToLines(sv), "\n", ", ")
						}
					}
					ev.Values = append(ev.Values, f)
				case b.Code == "missing_import" && b.Address != "":
					ev.Imports = append(ev.Imports, valueField{Name: "i:" + env + ":" + b.Address, Key: b.Address, Value: d.ImportIDs[env][b.Address],
						Label: capitalize("ID of the existing " + describe(d, b.Address)), Hint: "It already exists in " + env + "; find its ID in the PingOne admin console there."})
				}
			}
			if !ev.Ready && env != request.RequestBlockers {
				v.NeedEnvs = append(v.NeedEnvs, env)
			}
			if !ev.Ready && len(ev.Values)+len(ev.Imports) == len(res.Blockers[env]) {
				var parts []string
				for _, t := range []string{countText(len(ev.Values), "value", "values"), countText(len(ev.Imports), "ID of an existing item", "IDs of existing items")} {
					if t != "" {
						parts = append(parts, t)
					}
				}
				ev.Summary = strings.Join(parts, " and ") + " to fill in below"
			}
			v.Envs = append(v.Envs, ev)
		}
		v.HasBlockers = len(res.Blockers) > 0
		v.AlmostReady = onlyMissingValues(res)
		v.Skipped, v.Warnings = splitWarnings(res.Warnings)
		v.CanSubmit = v.Fresh && !v.HasBlockers && res.Status == request.StatusPreviewed && res.Error == nil && !v.Running
		// A blocked preview also carries an error; only one that stopped
		// before checking anything has no blockers and no changes.
		v.Failed = res.Error != nil && len(res.Blockers) == 0 && len(res.Changes) == 0
		v.Branch = s.branchFor(d)
		switch {
		case v.CanSubmit:
		case v.Running:
			v.WhyNot = "Something is still running for this request."
		case !v.Fresh:
			v.WhyNot = "The request changed after this preview. Preview again."
		case v.Failed:
			v.WhyNot = "The preview stopped before it could check anything (see the message at the top). Fix that, then preview again."
		case len(v.NeedEnvs) > 0:
			v.WhyNot = "Not every environment is ready: " + strings.Join(v.NeedEnvs, ", ") + ". See Readiness by environment."
		case v.HasBlockers:
			v.WhyNot = "The request has a problem to fix first. See Readiness by environment."
		default:
			v.WhyNot = "The preview did not finish. Preview again."
		}
		s.render(w, r, http.StatusOK, "preview", "Preview", v, false)
	})
}

// saveChoices stores per-dependency choices and previews again.
func (s *Server) saveChoices(w http.ResponseWriter, r *http.Request) {
	s.withDraft(w, r, func(w http.ResponseWriter, d *Draft) {
		if d.preview == nil {
			s.redirect(w, r, draftURL(d, ""))
			return
		}
		deps := map[string]bool{}
		for _, c := range d.preview.result.Changes {
			if c.Role == "dependency" {
				deps[c.Address] = true
			}
		}
		for a := range d.Choices {
			deps[a] = true
		}
		next := map[string]string{}
		for a := range deps {
			switch c := r.PostForm.Get("c:" + a); c {
			case "update", "keep", "exclude":
				next[a] = c
			case "":
			default:
				d.problem = "Choose update, keep the Git version or exclude."
				s.redirect(w, r, draftURL(d, "/preview"))
				return
			}
		}
		d.Choices = next
		d.Updated = s.now()
		s.preview(w, r, d, draftURL(d, "/preview"))
	})
}

// saveValues stores per-environment values (and import IDs) and previews
// again. Nothing is kept when the request refuses a value, so a value that
// looks like a secret is never shown again.
func (s *Server) saveValues(w http.ResponseWriter, r *http.Request) {
	s.withDraft(w, r, func(w http.ResponseWriter, d *Draft) {
		envs := append([]string{d.Source}, s.cfg.Rollout...)
		values := map[string]map[string]string{}
		imports := map[string]map[string]string{}
		for env, m := range d.Values {
			values[env] = map[string]string{}
			for k, v := range m {
				values[env][k] = v
			}
		}
		for env, m := range d.ImportIDs {
			imports[env] = map[string]string{}
			for k, v := range m {
				imports[env][k] = v
			}
		}
		for key, vals := range r.PostForm {
			kind, rest, ok := strings.Cut(key, ":")
			if !ok || kind != "v" && kind != "i" || len(vals) != 1 {
				continue
			}
			env, name, ok := strings.Cut(rest, ":")
			if !ok || !contains(envs, env) || name == "" || len(name) > 512 || len(vals[0]) > 8<<10 {
				continue
			}
			dst := values
			if kind == "i" {
				dst = imports
			}
			if dst[env] == nil {
				dst[env] = map[string]string{}
			}
			text := strings.TrimSpace(vals[0])
			if kind == "v" && isListValue(d, name) && text != "" && !strings.HasPrefix(text, "[") {
				text = linesToJSON(text)
			}
			if text == "" {
				delete(dst[env], name)
			} else {
				dst[env][name] = text
			}
		}
		oldV, oldI := d.Values, d.ImportIDs
		d.Values, d.ImportIDs = values, imports
		if _, err := s.buildRequest(d, true); err != nil {
			d.Values, d.ImportIDs = oldV, oldI
			d.problem = "Nothing was saved. " + s.redact(err.Error())
			s.redirect(w, r, draftURL(d, "/preview"))
			return
		}
		d.Updated = s.now()
		s.preview(w, r, d, draftURL(d, "/preview"))
	})
}

func (s *Server) startSubmit(w http.ResponseWriter, r *http.Request) {
	s.withDraft(w, r, func(w http.ResponseWriter, d *Draft) {
		back := draftURL(d, "/preview")
		p := d.preview
		if p == nil {
			s.redirect(w, r, draftURL(d, ""))
			return
		}
		if m := r.PostForm.Get("mode"); m == "mr" || m == "branch" {
			d.Mode = m
		}
		if p.result.Status != request.StatusPreviewed || p.result.Error != nil || len(p.result.Blockers) > 0 {
			d.problem = "The preview has problems. Fix them and preview again before submitting."
			s.redirect(w, r, back)
			return
		}
		req, err := s.buildRequest(d, false)
		if err != nil {
			d.problem = s.redact(err.Error())
			s.redirect(w, r, back)
			return
		}
		if fingerprint(*req) != p.fingerprint {
			d.problem = "The request changed after the preview. Preview again."
			s.redirect(w, r, back)
			return
		}
		if err := s.startJob(d, "submit", s.runSubmit(d, req, p)); err != nil {
			d.problem = err.Error()
			s.redirect(w, r, back)
			return
		}
		s.redirect(w, r, draftURL(d, "/job"))
	})
}

type resultView struct {
	D        *Draft
	R        *request.Result
	Conflict bool
	Message  string
}

func (s *Server) resultPage(w http.ResponseWriter, r *http.Request) {
	s.withDraft(w, r, func(w http.ResponseWriter, d *Draft) {
		if d.submitted == nil && !d.conflict {
			s.redirect(w, r, draftURL(d, "/preview"))
			return
		}
		v := resultView{D: d, R: d.submitted, Conflict: d.conflict}
		switch {
		case d.conflict:
			v.Message = ConflictMessage
		case d.submitted.Status == request.StatusNoChanges:
			v.Message = "Git already has all of this. Nothing was committed."
		case d.submitted.Status == request.StatusAlreadySubmitted:
			v.Message = "This request was already submitted. Nothing new was committed."
		case d.submitted.Status == request.StatusSubmitted && d.submitted.MRUrl != "":
			v.Message = "Submitted. A merge request is open for review."
		case d.submitted.Status == request.StatusSubmitted:
			v.Message = "Submitted to a branch. No merge request was opened."
		case d.submitted.Error != nil && d.submitted.Error.Code == request.ErrPartial:
			v.Message = "The result is uncertain; check GitLab. The request branch was kept so nothing is lost; do not submit again until you have checked it."
		case d.submitted.Error != nil:
			v.Message = "The submit did not complete."
		}
		s.render(w, r, http.StatusOK, "result", "Submission", v, false)
	})
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
		out = append(out, typeOption{Value: it.Type, Label: resourceLabel(it.Type), Selected: it.Type == d.TypeFilter})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}

// isListValue reports a per-environment value that holds a list (its
// source value is a JSON array), such as redirect URLs.
func isListValue(d *Draft, name string) bool {
	if d.preview == nil || d.preview.result == nil {
		return false
	}
	return strings.HasPrefix(d.preview.result.Variables[name].SourceValue, "[")
}

// linesToJSON turns one value per line into a JSON array of strings.
func linesToJSON(text string) string {
	var items []string
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			items = append(items, l)
		}
	}
	b, _ := json.Marshal(items)
	return string(b)
}

// jsonToLines shows a stored JSON array of strings one per line.
func jsonToLines(v string) string {
	var items []string
	if json.Unmarshal([]byte(v), &items) != nil {
		return v
	}
	return strings.Join(items, "\n")
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func countText(n int, one, many string) string {
	switch n {
	case 0:
		return ""
	case 1:
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// searchWait bounds how long a search request waits for the item list.
const searchWait = 3 * time.Minute

// preloadCatalog starts reading the source's item list in the background,
// so the first search is fast. The caller holds s.drafts.mu.
func (s *Server) preloadCatalog(d *Draft) {
	if d.Source == "" || d.catalogs[d.Source] != nil || (d.job != nil && d.job.Running) {
		return
	}
	_ = s.startJob(d, "search", s.runSearch(d, d.Source))
}

// awaitCatalog waits, without holding the lock, for a running list read
// to finish. The caller holds s.drafts.mu, and holds it again on return.
func (s *Server) awaitCatalog(r *http.Request, d *Draft) {
	deadline := time.Now().Add(searchWait)
	for d.job != nil && d.job.Running && d.job.Kind == "search" && time.Now().Before(deadline) && r.Context().Err() == nil {
		s.drafts.mu.Unlock()
		time.Sleep(150 * time.Millisecond)
		s.drafts.mu.Lock()
	}
}
