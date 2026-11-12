package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"pingone-promote/internal/exporter"
	"pingone-promote/internal/request"
)

// Draft limits. Drafts live only in memory: a restart loses them, and
// everything durable is in GitLab.
const (
	MaxDrafts           = 500
	MaxDraftsPerSession = 20
	DraftIdleExpiry     = 8 * time.Hour
	MaxSelected         = 200
	MaxSearchResults    = 50
	// MaxListResults caps a listing with no name typed.
	MaxListResults = 200
	catalogMaxAge  = 30 * time.Minute
	// catalogRefresh re-reads every source's item list in the background,
	// so a search rarely waits.
	catalogRefresh = 20 * time.Minute
)

// Item is one resource of the source environment.
type Item struct {
	Address string
	Type    string
	Name    string
	ID      string
}

func itemOf(r exporter.Resource) Item {
	return Item{Address: r.String(), Type: r.Type, Name: r.Name, ID: r.ID}
}

// Job is the one background job a draft can have at a time.
type Job struct {
	Kind     string // search, preview, submit
	Running  bool
	Started  time.Time
	Finished time.Time
	Stage    string
	Err      string
	cancel   context.CancelFunc
}

type catalogEntry struct {
	at    time.Time
	items []Item
	// davinci: why the DaVinci items are missing from items ("" when
	// they were read).
	davinci string
}

// preview is the latest preview of a draft: its result, the inputs it was
// for, the Git snapshot it read and the source export it used. Submit
// reuses the snapshot and the export, so what was reviewed is what is
// committed.
type preview struct {
	result        *request.Result
	fingerprint   string
	head          string
	defaultBranch string
	snapshot      string
	frozen        *frozenExport
	at            time.Time
}

// Draft is one request being prepared in one browser session.
type Draft struct {
	ID        string
	SessionID string
	RequestID string
	Created   time.Time
	Updated   time.Time

	Name        string // as typed
	RequestedBy string // Person.Recorded()
	Source      string
	Whole       bool
	// Families are whole sections chosen (scope "sections"); Whole wins.
	Families   []string
	ConfirmAll bool
	Upstream   bool
	Mode       string
	// Branch is the branch name typed by the person (branchPattern set).
	Branch      string
	Title       string
	Description string
	Selected    []Item
	Excluded    []Item
	Choices     map[string]string            // address -> update | keep | exclude
	Values      map[string]map[string]string // env -> variable -> text
	ImportIDs   map[string]map[string]string // env -> address -> ID

	Query string
	// TypeFilter limits search results to one resource type ("" all).
	TypeFilter string
	Results    []Item
	// ResultsTotal counts every match; Results holds the first ones.
	ResultsTotal int
	// searched: a search ran; searchNotice and searchProblem explain its
	// outcome next to the results, where the page scrolls to.
	searched bool
	// pending: a search waits for the item list (pendingLoad, the read it
	// waits for; pendingRefresh: it asked to read the list again). The
	// items page shows a loading notice and refreshes until it is in.
	pending        bool
	pendingLoad    *listLoad
	pendingRefresh bool
	searchNotice   string
	searchProblem  string

	catalogs map[string]*catalogEntry
	// loads are background reads of a source's item list, apart from job
	// (a preview or submit never waits for, or is refused by, a list read).
	loads map[string]*listLoad
	// WantSections: "whole sections" is chosen, even before one is ticked.
	WantSections bool
	// Step is the page of the request form shown: 1 what to copy, 2
	// choose items, 3 send for review.
	Step    int
	job     *Job
	preview *preview
	// submitted is the latest submit result; conflict marks a default
	// branch that moved after the preview.
	submitted *request.Result
	conflict  bool
	// notice and problem are shown once on the next page.
	notice  string
	problem string
}

type drafts struct {
	mu   sync.Mutex
	byID map[string]*Draft
	now  func() time.Time
	// catalogs and loads are the source environments' item lists and
	// their background reads, shared by every draft (each draft's
	// catalogs and loads fields are these same maps). Guarded by mu.
	catalogs map[string]*catalogEntry
	loads    map[string]*listLoad
}

var errTooManyDrafts = errors.New("There are too many open drafts. Finish or wait for older drafts to expire.")

func newRequestID(now time.Time) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := []byte(randomToken(6))
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return now.UTC().Format("20060102-150405") + "-" + string(b[:6])
}

func (ds *drafts) create(sessionID string, sources []string) (*Draft, error) {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	mine := 0
	for _, d := range ds.byID {
		if d.SessionID == sessionID {
			mine++
		}
	}
	if mine >= MaxDraftsPerSession || len(ds.byID) >= MaxDrafts {
		return nil, errTooManyDrafts
	}
	now := ds.now()
	d := &Draft{ID: randomToken(18), SessionID: sessionID, RequestID: newRequestID(now), Created: now, Updated: now,
		Upstream: true, Mode: "mr", Choices: map[string]string{}, Values: map[string]map[string]string{},
		ImportIDs: map[string]map[string]string{}, catalogs: ds.catalogs, loads: ds.loads}
	if len(sources) > 0 {
		d.Source = sources[0]
	}
	ds.byID[d.ID] = d
	return d, nil
}

// get returns a draft of this session; another session's draft is "not
// found", never "forbidden", so IDs cannot be probed. The caller holds
// ds.mu while it reads or changes the draft.
func (ds *drafts) get(sessionID, id string) *Draft {
	d := ds.byID[id]
	if d == nil || d.SessionID != sessionID {
		return nil
	}
	return d
}

// owns reports whether a session has drafts.
func (ds *drafts) owns(sessionID string) bool {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	for _, d := range ds.byID {
		if d.SessionID == sessionID {
			return true
		}
	}
	return false
}

func (ds *drafts) list(sessionID string) []*Draft {
	var out []*Draft
	for _, d := range ds.byID {
		if d.SessionID == sessionID {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out
}

// expire removes idle drafts and drafts of ended sessions, cancelling their
// jobs and deleting their snapshots.
func (ds *drafts) expire(goneSessions []string) {
	gone := map[string]bool{}
	for _, s := range goneSessions {
		gone[s] = true
	}
	ds.mu.Lock()
	defer ds.mu.Unlock()
	for id, d := range ds.byID {
		running := d.job != nil && d.job.Running
		if gone[d.SessionID] || !running && ds.now().Sub(d.Updated) > DraftIdleExpiry {
			ds.dropLocked(id)
		}
	}
}

func (ds *drafts) dropLocked(id string) {
	d := ds.byID[id]
	if d == nil {
		return
	}
	if d.job != nil && d.job.cancel != nil {
		d.job.cancel()
	}
	if d.preview != nil {
		os.RemoveAll(d.preview.snapshot)
	}
	delete(ds.byID, id)
}

func (ds *drafts) closeAll() {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	for id := range ds.byID {
		ds.dropLocked(id)
	}
}

// encodeValue turns a typed value into JSON: a JSON list or object as
// written, anything else as a string (Terraform converts strings to
// numbers and booleans where a variable needs one).
func encodeValue(text string) json.RawMessage {
	t := strings.TrimSpace(text)
	if (strings.HasPrefix(t, "[") || strings.HasPrefix(t, "{")) && json.Valid([]byte(t)) {
		return json.RawMessage(t)
	}
	b, _ := json.Marshal(t)
	return b
}

// ImportIDRecorder puts per-environment import IDs (environment -> resource
// address -> ID) on a request.
type ImportIDRecorder interface {
	SetImportIDs(req *request.Request, ids map[string]map[string]string) error
}

// requestImportIDs records them in request.ImportIDs, which the merge
// writes to each environment's import_ids (adoption of built-in objects).
type requestImportIDs struct{}

func (requestImportIDs) SetImportIDs(req *request.Request, ids map[string]map[string]string) error {
	for env, m := range ids {
		for a, id := range m {
			if id == "" {
				continue
			}
			if req.ImportIDs == nil {
				req.ImportIDs = map[string]map[string]string{}
			}
			if req.ImportIDs[env] == nil {
				req.ImportIDs[env] = map[string]string{}
			}
			req.ImportIDs[env][a] = id
		}
	}
	return nil
}

// buildRequest turns the draft into a request.
func (s *Server) buildRequest(d *Draft, dryRun bool) (*request.Request, error) {
	up := d.Upstream
	req := &request.Request{RequestVersion: request.Version, RequestID: d.RequestID, RequestedBy: d.RequestedBy, Source: d.Source,
		Scope: request.ScopeSelected, IncludeUpstream: &up, Mode: d.Mode, BranchName: s.branchFor(d), NamedBranch: s.branchRE() != nil,
		Title: d.Title, Description: d.Description, DryRun: dryRun}
	if d.Whole {
		req.Scope, req.ConfirmAll = request.ScopeAll, d.ConfirmAll
	} else {
		req.Families = append(req.Families, d.Families...)
		for _, it := range d.Selected {
			req.Resources = append(req.Resources, request.ExcludeRef{Address: it.Address})
		}
	}
	for _, it := range d.Excluded {
		req.Exclude = append(req.Exclude, request.ExcludeRef{Address: it.Address})
	}
	if len(d.Choices) > 0 {
		req.DependencyChoices = map[string]string{}
		for a, c := range d.Choices {
			req.DependencyChoices[a] = c
		}
	}
	for env, vals := range d.Values {
		for k, v := range vals {
			if strings.TrimSpace(v) == "" {
				continue
			}
			if req.EnvValues == nil {
				req.EnvValues = map[string]map[string]json.RawMessage{}
			}
			if req.EnvValues[env] == nil {
				req.EnvValues[env] = map[string]json.RawMessage{}
			}
			req.EnvValues[env][k] = encodeValue(v)
		}
	}
	if d.RequestedBy == "" {
		return nil, errors.New("Type your name.")
	}
	if !d.Whole && d.WantSections && len(d.Families) == 0 {
		return nil, errors.New("Tick at least one section in step 1, or choose another option.")
	}
	if s.branchRE() != nil && !s.branchRE().MatchString(d.Branch) {
		if d.Branch == "" {
			return nil, errors.New("Type the Git branch name in step 3" + s.branchExampleText() + ".")
		}
		return nil, errors.New("The Git branch name " + d.Branch + " is not allowed: it must match " + s.cfg.BranchPattern + s.branchExampleText() + ".")
	}
	if err := s.importIDs.SetImportIDs(req, d.ImportIDs); err != nil {
		return nil, err
	}
	if err := req.Validate(); err != nil {
		return nil, plainValidation(err)
	}
	return req, nil
}

// plainValidation keeps the request package's message, which names the
// field and never repeats a value.
func plainValidation(err error) error {
	var ve *request.ValidationError
	if errors.As(err, &ve) {
		if ve.Field != "" {
			return fmt.Errorf("%s: %s", ve.Field, ve.Msg)
		}
		return errors.New(ve.Msg)
	}
	return err
}

// fingerprint identifies what a preview previewed: everything that changes
// the export or the files, but not how it is delivered (mode, title,
// description) or who asks.
func fingerprint(r request.Request) string {
	r.DryRun, r.Mode, r.Title, r.Description, r.CommitMessage, r.RequestedBy = false, "", "", "", "", ""
	b, _ := json.Marshal(r)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Scope is the page's choice: "selected", "sections" or "all".
func (d *Draft) Scope() string {
	switch {
	case d.Whole:
		return request.ScopeAll
	case len(d.Families) > 0 || d.WantSections:
		return "sections"
	}
	return request.ScopeSelected
}

// branchFor is the branch a submit uses: the typed name when the
// configuration has a branch pattern, else prefix + request ID.
func (s *Server) branchFor(d *Draft) string {
	if s.branchRE() != nil {
		return d.Branch
	}
	return s.cfg.BranchPrefix + d.RequestID
}

func (s *Server) branchExampleText() string {
	if s.cfg.BranchExample == "" {
		return ""
	}
	return ", for example " + s.cfg.BranchExample
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
