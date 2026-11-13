package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"pingone-promote/internal/catalog"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"pingone-promote/internal/request"
)

// Handlers only translate between forms, drafts and the request engine;
// all resource-specific work happens in the engine.

func (s *Server) redirect(w http.ResponseWriter, r *http.Request, to string) {
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// draftURL is a draft's address: with suffix "" or "#...", the page of
// its current step.
func draftURL(d *Draft, suffix string) string {
	if suffix == "" || strings.HasPrefix(suffix, "#") {
		return "/drafts/" + d.ID + "/" + stepPath(d.Step) + suffix
	}
	return "/drafts/" + d.ID + suffix
}

func stepPath(step int) string {
	switch step {
	case 2:
		return "items"
	case 3:
		return "send"
	}
	return "scope"
}

// draftHome sends a draft's address (and the older ?step=N form) to the
// page of its current step.
func (s *Server) draftHome(w http.ResponseWriter, r *http.Request) {
	s.withDraft(w, r, func(w http.ResponseWriter, d *Draft) {
		if n, err := strconv.Atoi(r.URL.Query().Get("step")); err == nil && n >= 1 && n <= 3 {
			d.Step = n
		}
		s.redirect(w, r, draftURL(d, ""))
	})
}

// stepPage shows step n of a draft.
func (s *Server) stepPage(n int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.withDraft(w, r, func(w http.ResponseWriter, d *Draft) {
			d.Step = n
			s.showDraft(w, r, d)
		})
	}
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
	s.loadList(d, d.Source)
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
	// ListError: the item list could not be read; PendingQuery: a search
	// waits for the list (Loading) and shows when it is in.
	ListError    string
	PendingQuery bool
	// Step is the page shown (1-3); CheckedSections names the ticked
	// sections; ListAge says how old the item list is.
	Step            int
	CheckedSections []string
	ListAge         string
	// ResultsTotal: matches in all (Results shows the first ones);
	// ListSize: items in the source's list; SearchNotice and
	// SearchProblem: the last search's outcome, shown with the results.
	ResultsTotal int
	ListSize     int
	// DaVinciMissing: why the item list has no DaVinci items.
	DaVinciMissing string
	SearchNotice   string
	SearchProblem  string
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

func (s *Server) showDraft(w http.ResponseWriter, r *http.Request, d *Draft) {
	{
		var listErr string
		waiting := s.resolveSearch(d)
		if d.Step == 2 && d.catalogs[d.Source] == nil && !waiting {
			// Reading the list starts when the items page is opened, unless
			// the last read failed (the page offers to try again).
			if l := d.loads[d.Source]; l != nil && l.finished() && l.err != "" {
				listErr = l.err
			} else {
				s.loadList(d, d.Source)
			}
		}
		loading := waiting || (d.catalogs[d.Source] == nil && d.loads[d.Source] != nil && !d.loads[d.Source].finished())
		v := draftView{D: d, Sources: s.cfg.Sources, Rollout: s.cfg.Rollout, Notice: d.notice, Problem: d.problem,
			Running: d.job != nil && d.job.Running && d.job.Kind != "search", Loading: loading, ListError: listErr, PendingQuery: d.pending && d.Query != "",
			HasPreview: d.preview != nil,
			Results:    viewItems(d.Results, d), Selected: viewItems(d.Selected, d), Excluded: viewItems(d.Excluded, d),
			ListLoaded: d.catalogs[d.Source] != nil, SearchedOnce: d.searched, Types: typeOptions(d),
			ResultsTotal: d.ResultsTotal, SearchNotice: d.searchNotice, SearchProblem: d.searchProblem,
			Sections: sectionOptions(d), Branch: s.branchFor(d),
			TypedBranch: s.branchRE() != nil, BranchPattern: s.cfg.BranchPattern, BranchExample: s.cfg.BranchExample}
		if c := d.catalogs[d.Source]; c != nil {
			v.ListSize = len(c.items)
			v.DaVinciMissing = c.davinci
			if age := s.now().Sub(c.at); age >= time.Minute {
				v.ListAge = fmt.Sprintf("%d minutes", int(age.Minutes()))
			}
		}
		v.Step = d.Step
		if v.Step < 1 || v.Step > 3 {
			v.Step = 1
		}
		for _, o := range v.Sections {
			if o.Checked {
				v.CheckedSections = append(v.CheckedSections, o.Label)
			}
		}
		d.notice, d.problem, d.searchNotice, d.searchProblem = "", "", "", ""
		titles := map[int]string{1: "New request: what to copy", 2: "New request: choose items", 3: "New request: send for review"}
		s.render(w, r, http.StatusOK, "draft", titles[v.Step], v, v.Step == 2 && v.Loading)
	}
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
	step := f.Get("step") // "" applies every field (one-page form)
	if step == "" && !f.Has("source") {
		return true // an older form without step 1
	}
	if step == "" || step == "1" {
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
			d.ResultsTotal, d.searched = 0, false
			s.loadList(d, src)
		}
		d.Whole = f.Get("scope") == request.ScopeAll
		d.Families = nil
		d.WantSections = f.Get("scope") == "sections"
		if !d.WantSections && len(f["family"]) > 0 {
			d.notice = "The ticked sections are only used with Whole sections; you chose another option, so they were ignored."
		}
		if d.WantSections {
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
	}
	if step == "" || step == "3" {
		if m := f.Get("mode"); m == "mr" || m == "branch" {
			d.Mode = m
		}
		if s.branchRE() != nil && f.Has("branch") {
			b := strings.TrimSpace(f.Get("branch"))
			if len(b) > 255 || !asciiPrintable(b) {
				d.problem = "The Git branch name must be at most 255 plain characters (letters, digits, - _ . /), with no spaces."
			} else {
				d.Branch = b
			}
		}
		title, desc := strings.TrimSpace(f.Get("title")), f.Get("description")
		switch {
		case len(title) > 255 || strings.IndexFunc(title, func(r rune) bool { return unicode.IsControl(r) || r == '\u2028' || r == '\u2029' || r == '\u0085' }) >= 0:
			d.problem = "The merge request title must be one line of at most 255 characters; it was not saved."
		case len(desc) > 64<<10:
			d.problem = "The note for reviewers must be at most 64 KB; it was not saved."
		default:
			d.Title, d.Description = title, desc
		}
	}
	d.Updated = s.now()
	// The name is typed on step 1; later steps use the saved one.
	if idErr != nil && f.Get("action") == "preview" && (step == "" || step == "1") {
		d.problem = idErr.Error()
		s.redirect(w, r, draftURL(d, ""))
		return false
	}
	return true
}

func (s *Server) saveDraft(w http.ResponseWriter, r *http.Request) {
	s.withDraft(w, r, func(w http.ResponseWriter, d *Draft) {
		f := r.PostForm
		if !s.applyMain(w, r, d) {
			return
		}
		if st := f.Get("step"); st != "" && st != "1" {
			// The name was typed and checked on step 1.
		} else if _, idErr := s.identity.Who(r); idErr != nil {
			d.problem = idErr.Error()
			s.redirect(w, r, draftURL(d, ""))
			return
		}
		switch f.Get("action") {
		case "next":
			if why := s.stepProblem(d); why != "" {
				d.problem = why
			} else if d.Step < 3 {
				d.Step = max(d.Step, 1) + 1
				if d.Step == 2 {
					s.loadList(d, d.Source)
				}
			}
			s.redirect(w, r, draftURL(d, ""))
			return
		case "back":
			if d.Step > 1 {
				d.Step--
			}
			s.redirect(w, r, draftURL(d, ""))
			return
		case "preview":
			if d.problem != "" {
				d.Step = 3 // the title and note are on step 3
				s.redirect(w, r, draftURL(d, ""))
				return
			}
		default:
			if d.notice == "" && d.problem == "" {
				d.notice = "Saved."
			}
			s.redirect(w, r, draftURL(d, ""))
			return
		}
		d.Step = 3
		s.preview(w, r, d, draftURL(d, ""))
	})
}

// stepProblem says what keeps the current step from moving on.
func (s *Server) stepProblem(d *Draft) string {
	if max(d.Step, 1) != 1 {
		return ""
	}
	switch {
	case d.Name == "":
		return "Type your name."
	case d.WantSections && len(d.Families) == 0:
		return "Tick at least one section to copy whole sections, or choose another option."
	case d.Whole && !d.ConfirmAll:
		return "Tick the confirmation to copy the whole environment, or choose another option."
	}
	return ""
}

// preview validates the draft and starts a preview job; problems go back
// to the page named by back.
func (s *Server) preview(w http.ResponseWriter, r *http.Request, d *Draft, back string) {
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

// pick adds an item to, or removes it from, the selected or excluded list.
func (s *Server) pick(w http.ResponseWriter, r *http.Request) {
	s.withDraft(w, r, func(w http.ResponseWriter, d *Draft) {
		addr, op := r.PostForm.Get("address"), r.PostForm.Get("op")
		d.Step = 2
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
		// A commit to GitLab is not interrupted half way.
		if d.job != nil && d.job.Running && d.job.cancel != nil && d.job.Stage != commitStage {
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
	// Entered are values and IDs already entered, editable. Filled counts
	// those the server filled in from what the environment has.
	Entered []valueField
	Filled  int
}

func importField(d *Draft, env, addr string) valueField {
	return valueField{Name: "i:" + env + ":" + addr, Key: addr, Value: d.ImportIDs[env][addr],
		Label: capitalize("ID of the existing " + describe(d, addr)), Hint: "It already exists in " + env + "; find its ID in the PingOne admin console there."}
}

func hasField(fs []valueField, key string) bool {
	for _, f := range fs {
		if f.Key == key {
			return true
		}
	}
	return false
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
	// Choosable: the row offers update / keep (and exclude when
	// CanExclude).
	Choosable  bool
	CanExclude bool
	// Why says why the item is in the request.
	Why    string
	Choice string
}

// differs: statuses where Git and the source disagree, so the person
// chooses which version the request keeps.
var differs = map[string]bool{"changed-in-source": true, "git-newer": true, "changed-in-both": true}

type typeView struct {
	Kind string
	request.TypeCounts
}

type previewView struct {
	D         *Draft
	R         *request.Result
	Problem   string
	Fresh     bool
	CanSubmit bool
	// NoChanges: Git already has everything in the request. Kept counts
	// items that differ from the source and keep their Git version.
	NoChanges bool
	Kept      int
	// Filled counts values the server filled in from the environments.
	Filled      int
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
			if c.Action == "kept_differs" {
				v.Kept++
			}
			if d.Whole && c.Action == "unchanged" {
				v.Unchanged++
				continue
			}
			typ, _, _ := strings.Cut(c.Address, ".")
			cv := changeView{ChangeOut: c, Kind: resourceLabel(typ), Name: itemName(d, c.Address), StatusText: statusText[c.Status], ActionText: actionText[c.Action],
				Choice: d.Choices[c.Address]}
			// A dependency can be updated, kept or left out; an item the
			// person asked for that differs from Git can be updated or kept.
			cv.Choosable = c.Role == "dependency" && (c.Action != "unchanged" || cv.Choice != "") || differs[c.Status]
			cv.CanExclude = c.Role == "dependency"
			cv.Why = whyText(d, c.Role)
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
					ev.Imports = append(ev.Imports, importField(d, env, b.Address))
				}
			}
			// What was entered stays editable: a typo can be corrected,
			// or cleared to enter it again.
			okV, _ := fillable(d, env)
			for _, name := range sortedKeysS(d.Values[env]) {
				if !okV[name] || hasField(ev.Values, name) {
					continue
				}
				label, hint := valueLabel(d, res, name)
				f := valueField{Name: "v:" + env + ":" + name, Key: name, Value: d.Values[env][name], Label: capitalize(label), Hint: hint, List: isListValue(d, name)}
				if f.List {
					f.Value, f.Hint = jsonToLines(f.Value), "One per line."
				}
				if from := d.Filled[env][name]; from != "" {
					f.Hint = "Filled in from what " + from + " has now; check it. " + f.Hint
					ev.Filled++
				}
				ev.Entered = append(ev.Entered, f)
			}
			for _, a := range sortedKeysS(d.ImportIDs[env]) {
				if !hasField(ev.Imports, a) {
					f := importField(d, env, a)
					if from := d.Filled[env][a]; from != "" {
						f.Hint = "Filled in from what " + from + " has now (the existing item with the same name); check it."
						ev.Filled++
					}
					ev.Entered = append(ev.Entered, f)
				}
			}
			v.Filled += ev.Filled
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
		if v.CanSubmit && len(res.Files) == 0 {
			v.CanSubmit, v.NoChanges = false, true
		}
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
		deps, canExclude := map[string]bool{}, map[string]bool{}
		for _, c := range d.preview.result.Changes {
			if c.Role == "dependency" || differs[c.Status] {
				deps[c.Address] = true
			}
			if c.Role == "dependency" {
				canExclude[c.Address] = true
			}
		}
		for a := range d.Choices {
			deps[a] = true
		}
		next := map[string]string{}
		for a := range deps {
			switch c := r.PostForm.Get("c:" + a); c {
			case "exclude":
				if !canExclude[a] {
					d.problem = "Only an item your selection depends on can be left out here; leave other items out in step 2."
					s.redirect(w, r, draftURL(d, "/preview"))
					return
				}
				next[a] = c
			case "update", "keep":
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
			// Only what this request asks for: never the environment ID
			// or a variable the request does not declare.
			okV, okI := fillable(d, env)
			if kind == "v" && !okV[name] || kind == "i" && !okI[name] {
				d.problem = "Nothing was saved: this request has no value named " + name + "."
				s.redirect(w, r, draftURL(d, "/preview"))
				return
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
			var ve *request.ValidationError
			if errors.As(err, &ve) && ve.Plain != "" {
				// envValues.<env>.<name>
				parts := strings.SplitN(ve.Field, ".", 3)
				if len(parts) == 3 && d.preview != nil && d.preview.result != nil {
					label, _ := valueLabel(d, d.preview.result, parts[2])
					d.problem = "Nothing was saved: the value of " + label + " for " + parts[1] + " " + ve.Plain + "."
				}
			}
			s.redirect(w, r, draftURL(d, "/preview"))
			return
		}
		for env, m := range d.Filled {
			for k := range m {
				if d.Values[env][k] != oldV[env][k] || d.ImportIDs[env][k] != oldI[env][k] {
					delete(m, k) // changed by the person
				}
			}
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
		out = append(out, typeOption{Value: it.Type, Label: capitalize(resourceLabel(it.Type)), Selected: it.Type == d.TypeFilter})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Label) < strings.ToLower(out[j].Label) })
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

// searchWait bounds how long a search request waits for the item list; it
// stays under the server's write timeout.
const searchWait = 40 * time.Second

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

// awaitList waits, without holding the lock, for l to finish, at most
// searchWait. The caller holds s.drafts.mu, and holds it again on return.
func (s *Server) awaitList(r *http.Request, l *listLoad) {
	if l == nil || l.finished() {
		return
	}
	s.drafts.mu.Unlock()
	defer s.drafts.mu.Lock()
	t := time.NewTimer(searchWait)
	defer t.Stop()
	select {
	case <-l.done:
	case <-t.C:
	case <-r.Context().Done():
	}
}

func asciiPrintable(s string) bool {
	for _, r := range s {
		if r <= 0x20 || r >= 0x7f {
			return false
		}
	}
	return true
}

// fillable lists the values and import IDs a person may enter for env
// after the draft's last preview: what the preview asks for (a missing
// value, the ID of an existing item to adopt) and what was entered then.
// Values Git or the source already has are not changed here.
func fillable(d *Draft, env string) (values, imports map[string]bool) {
	values, imports = map[string]bool{}, map[string]bool{}
	if d.preview == nil || d.preview.result == nil {
		return
	}
	res := d.preview.result
	for _, b := range res.Blockers[env] {
		switch {
		case b.Code == "missing_value" && b.Var != "":
			values[b.Var] = true
		case b.Code == "missing_import" && b.Address != "":
			imports[b.Address] = true
		}
	}
	// Entered earlier, so asked for then (the blocker is gone now).
	for name := range d.Values[env] {
		values[name] = true
	}
	for a := range d.ImportIDs[env] {
		imports[a] = true
	}
	return
}

func sortedKeysS(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// commitStage is the job stage that writes to GitLab.
const commitStage = "Committing to GitLab"

func whyText(d *Draft, role string) string {
	switch role {
	case "selected":
		if len(d.Families) > 0 {
			return "in a chosen section, or picked"
		}
		return "you picked it"
	case "dependency":
		return "your selection uses it"
	case "environment":
		return "whole environment"
	case "excluded":
		return "excluded"
	}
	return role
}
