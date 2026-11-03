package app

// Terraform authoring: the server-side Draft API (#70). These routes sit
// beside the native promotion routes, which are removed in #44. They reuse
// the loopback security in ServeHTTP (cookie token, CSRF header, Origin and
// Sec-Fetch-Site checks, CSP).
//
// A draft is metadata only: the source inventory reference, selections,
// exclusions, parameter values, secret mappings and acknowledgements, plus
// the repository coordinates and base commit IDs it was computed against.
// Inventory content and target files are held in memory; after a restart a
// draft reports needsRebase until it is rebased. Responses never contain
// secret values: the tfvars carry only placeholders and the inventory
// summaries carry only names and keys.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"pingone-promote/internal/davinci"
	"pingone-promote/internal/gitlab"
	"pingone-promote/internal/pingone"
	"pingone-promote/internal/pipelinesecrets"
	promote "pingone-promote/internal/promotelegacy"
	"pingone-promote/internal/store"
	"pingone-promote/internal/tfvars"
	"pingone-promote/internal/translate"
)

// --- typed API errors ---

// apiError is an error with a machine-readable code and extra fields, so the
// UI can offer the right fix (#57).
type apiError struct {
	status int
	Code   string         `json:"code"`
	Msg    string         `json:"error"`
	Extra  map[string]any `json:"details,omitempty"`
}

func (e *apiError) Error() string { return e.Msg }

func apiErr(status int, code, msg string, extra map[string]any) error {
	return &apiError{status: status, Code: code, Msg: msg, Extra: extra}
}

// --- in-memory state ---

type dvInventory struct {
	ID            string
	ProfileID     string
	EnvironmentID string
	LoadedAt      time.Time
	Inv           *davinci.Inventory
	Summary       inventorySummary
}

type draftRuntime struct {
	target       *tfvars.Document
	secretsYAML  []byte
	otherSecrets []string // secret keys other environments' tfvars require
	computed     *draftComputed
}

type draftComputed struct {
	view       draftView
	tfvars     []byte
	secretsTF  []byte
	customerYM []byte
	changes    []tfvars.Change
	summary    string
}

// draftState is what persists.
type draftState struct {
	ID                  string            `json:"id"`
	Revision            int               `json:"revision"`
	CreatedAt           time.Time         `json:"createdAt"`
	UpdatedAt           time.Time         `json:"updatedAt"`
	SourceProfileID     string            `json:"sourceProfileId"`
	SourceEnvironmentID string            `json:"sourceEnvironmentId"`
	TargetProfileID     string            `json:"targetProfileId"`
	TargetEnvironmentID string            `json:"targetEnvironmentId"`
	Environment         string            `json:"environment"`
	InventoryID         string            `json:"inventoryId"`
	Flows               []string          `json:"flows"`
	Applications        []string          `json:"applications"`
	Excluded            []string          `json:"excluded"`
	KeepTarget          []string          `json:"keepTarget"`
	Parameters          map[string]string `json:"parameters"`
	SecretNames         map[string]string `json:"secretNames"`
	NewSecretFields     map[string]string `json:"newSecretFields"`
	Acknowledged        []string          `json:"acknowledged"`
	NotSecret           []string          `json:"notSecret"`
	Base                map[string]string `json:"base"`
	Submissions         []string          `json:"submissions"`
}

type submissionRecord struct {
	ID              string    `json:"id"`
	Kind            string    `json:"kind"` // submit or revert
	DraftID         string    `json:"draftId,omitempty"`
	RevertOf        string    `json:"revertOf,omitempty"`
	Environment     string    `json:"environment"`
	Mode            string    `json:"mode"`
	Branch          string    `json:"branch"`
	CommitID        string    `json:"commitId"`
	MergeRequestURL string    `json:"mergeRequestUrl,omitempty"`
	MergeRequestIID int64     `json:"mergeRequestIid,omitempty"`
	Paths           []string  `json:"paths"`
	CreatedAt       time.Time `json:"createdAt"`
}

// --- views ---

type keyName struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	Note string `json:"note,omitempty"`
}

type inventorySummary struct {
	ID            string            `json:"id"`
	ProfileID     string            `json:"profileId"`
	EnvironmentID string            `json:"environmentId"`
	LoadedAt      time.Time         `json:"loadedAt"`
	Partial       []davinci.Failure `json:"partial"`
	Flows         []keyName         `json:"flows"`
	Connections   []keyName         `json:"connections"`
	Variables     []keyName         `json:"variables"`
	Applications  []keyName         `json:"applications"`
	Decisions     []string          `json:"decisions"`
}

type requiredSecret struct {
	Key          string   `json:"key"`
	Sites        []string `json:"sites,omitempty"`
	Declared     bool     `json:"declared"`
	CommentedOut bool     `json:"commentedOut"`
	New          bool     `json:"new"`
	VaultField   string   `json:"vaultField,omitempty"`
}

type vaultEntry struct {
	EnvVar string `json:"envVar"`
	Vault  string `json:"vault"`
}

type draftView struct {
	ID                string               `json:"id"`
	Revision          int                  `json:"revision"`
	Environment       string               `json:"environment"`
	TargetPath        string               `json:"targetPath"`
	SourceEnvironment string               `json:"sourceEnvironmentId"`
	TargetEnvironment string               `json:"targetEnvironmentId"`
	NeedsRebase       bool                 `json:"needsRebase"`
	Flows             []string             `json:"flows"`
	Applications      []string             `json:"applications"`
	Excluded          []string             `json:"excluded"`
	KeepTarget        []string             `json:"keepTarget"`
	Parameters        map[string]string    `json:"parameters"`
	Dependencies      []promote.Dependency `json:"dependencies"`
	Blocks            []promote.Block      `json:"blocks"`
	Problems          []tfvars.Problem     `json:"problems"`
	Warnings          []any                `json:"warnings"`
	RequiredSecrets   []requiredSecret     `json:"requiredSecrets"`
	SecretCandidates  []string             `json:"secretCandidates"`
	NewVaultEntries   []vaultEntry         `json:"newVaultEntries"`
	MissingParameters []string             `json:"missingParameters"`
	Changes           []tfvars.Change      `json:"changes"`
	Blockers          []string             `json:"blockers"`
	ReadyToSubmit     bool                 `json:"readyToSubmit"`
	Submissions       []string             `json:"submissions"`
	UpdatedAt         time.Time            `json:"updatedAt"`
	SecretSites       []translate.Secret   `json:"secretSites"`
	Decisions         []string             `json:"decisions"`
}

// --- helpers ---

var slugUnsafe = regexp.MustCompile(`[^a-z0-9_-]+`)

func environmentSlug(name string) string {
	s := strings.Trim(slugUnsafe.ReplaceAllString(strings.ToLower(name), "-"), "-_")
	if len(s) > 63 {
		s = s[:63]
	}
	return s
}

func uniqueSorted(in []string) []string {
	m := map[string]bool{}
	for _, s := range in {
		m[s] = true
	}
	out := make([]string, 0, len(m))
	for s := range m {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func without(list []string, s string) []string {
	out := []string{}
	for _, x := range list {
		if x != s {
			out = append(out, x)
		}
	}
	return out
}

func setOf(list []string) map[string]bool {
	m := map[string]bool{}
	for _, s := range list {
		m[s] = true
	}
	return m
}

// gitlabSetup returns a GitLab client and config, or a typed error.
func (s *Server) gitlabSetup() (*gitlab.Client, gitlab.Config, error) {
	c, err := gitlab.LoadConfig(s.store.Directory())
	if err != nil {
		return nil, c, err
	}
	c = defaultGitLab(c)
	if c.BaseURL == "" {
		return nil, c, apiErr(http.StatusFailedDependency, "gitlab_unconfigured", "configure the GitLab project first", nil)
	}
	token := s.gitlabToken(c)
	if token == "" {
		return nil, c, apiErr(http.StatusFailedDependency, "gitlab_token_missing", missingGitLabToken(c).Error(), nil)
	}
	client, err := s.gitlabFactory(c, token)
	return client, c, err
}

func (s *Server) submitter(c *gitlab.Client, cfg gitlab.Config) *gitlab.Submitter {
	return &gitlab.Submitter{Client: c, Paths: cfg.Paths(), Formatter: cfg.TerraformBin}
}

// --- routing ---

func isDraftRoute(path string) bool {
	for _, p := range []string{"/api/environments", "/api/inventories", "/api/drafts", "/api/submissions"} {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}

func (s *Server) draftRoute(w http.ResponseWriter, r *http.Request) error {
	path, m := r.URL.Path, r.Method
	switch {
	case path == "/api/environments" && m == http.MethodGet:
		return s.listEnvironments(w)
	case path == "/api/inventories" && m == http.MethodGet:
		out := []inventorySummary{}
		for _, inv := range s.dvInventories {
			out = append(out, inv.Summary)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].LoadedAt.Before(out[j].LoadedAt) })
		writeJSON(w, out)
		return nil
	case path == "/api/drafts" && m == http.MethodPost:
		return s.createDraft(w, r)
	case path == "/api/drafts" && m == http.MethodGet:
		return s.listDrafts(w)
	case path == "/api/submissions" && m == http.MethodGet:
		return s.listSubmissions(w)
	case strings.HasPrefix(path, "/api/submissions/"):
		parts := strings.Split(strings.TrimPrefix(path, "/api/submissions/"), "/")
		if len(parts) == 2 && parts[1] == "revert" && m == http.MethodPost {
			return s.revertSubmission(w, r, parts[0])
		}
	case strings.HasPrefix(path, "/api/drafts/"):
		parts := strings.Split(strings.TrimPrefix(path, "/api/drafts/"), "/")
		st, rt, err := s.loadDraft(parts[0])
		if err != nil {
			return err
		}
		switch {
		case len(parts) == 1 && m == http.MethodGet:
			writeJSON(w, s.view(st, rt))
			return nil
		case len(parts) == 1 && m == http.MethodPatch:
			return s.patchDraft(w, r, st, rt)
		case len(parts) == 2 && parts[1] == "diff" && m == http.MethodGet:
			if rt == nil || rt.computed == nil {
				return apiErr(http.StatusConflict, "needs_rebase", "rebase the draft first", nil)
			}
			writeJSON(w, map[string]any{"revision": st.Revision, "changes": rt.computed.changes, "summary": rt.computed.summary})
			return nil
		case len(parts) == 2 && parts[1] == "file" && m == http.MethodGet:
			return s.draftFiles(w, st, rt)
		case len(parts) == 2 && parts[1] == "rebase" && m == http.MethodPost:
			return s.rebaseDraft(w, r, st)
		case len(parts) == 2 && parts[1] == "submit" && m == http.MethodPost:
			return s.submitDraft(w, r, st, rt)
		}
	}
	return problem(404, errors.New("unknown application endpoint"))
}

func (s *Server) listEnvironments(w http.ResponseWriter) error {
	profiles, err := s.store.ListProfiles()
	if err != nil {
		return problem(http.StatusInternalServerError, errors.New("Could not read saved profiles; check local app storage and restart the app."))
	}
	cfg, _ := gitlab.LoadConfig(s.store.Directory())
	paths := cfg.Paths()
	out := []map[string]any{}
	for _, p := range profiles {
		slug := environmentSlug(p.Name)
		path, _ := paths.EnvironmentPath(slug)
		out = append(out, map[string]any{"profileId": p.ID, "name": p.Name, "environmentId": p.EnvironmentID, "region": p.Region,
			"environment": slug, "tfvarsPath": path})
	}
	writeJSON(w, out)
	return nil
}

// loadDavinciInventory runs outside the server lock (like loadInventory).
func (s *Server) loadDavinciInventory(w http.ResponseWriter, r *http.Request) error {
	var input struct {
		ProfileID string `json:"profileId"`
	}
	if err := decodeBody(w, r, &input); err != nil {
		return err
	}
	s.mu.Lock()
	gen := s.profileGen
	p, err := s.profile(input.ProfileID)
	var c *pingone.Client
	if err == nil {
		c, err = s.client(p)
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	inv := davinci.Read(r.Context(), c, p.EnvironmentID)
	rec := &dvInventory{ID: RandomToken()[:24], ProfileID: p.ID, EnvironmentID: p.EnvironmentID, LoadedAt: time.Now().UTC(), Inv: inv}
	rec.Summary = summarize(rec)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.profileGen != gen {
		return problem(409, errors.New("environment profiles changed while resources were loading; load resources again"))
	}
	s.dvInventories[rec.ID] = rec
	writeJSON(w, rec.Summary)
	return nil
}

// summarize lists keys and names only, never configuration.
func summarize(rec *dvInventory) inventorySummary {
	sum := inventorySummary{ID: rec.ID, ProfileID: rec.ProfileID, EnvironmentID: rec.EnvironmentID, LoadedAt: rec.LoadedAt,
		Partial: append([]davinci.Failure{}, rec.Inv.Partial...), Flows: []keyName{}, Connections: []keyName{}, Variables: []keyName{}, Applications: []keyName{}, Decisions: []string{}}
	res, err := translate.Translate(rec.Inv, translate.Options{})
	var te *translate.Error
	if errors.As(err, &te) {
		sum.Decisions = te.Problems
	}
	if res == nil {
		return sum
	}
	for k, f := range res.Doc.Flows {
		sum.Flows = append(sum.Flows, keyName{Key: k, Name: f.Name, Note: "tier " + strconv.Itoa(f.Tier)})
	}
	for k, c := range res.Doc.Connections {
		sum.Connections = append(sum.Connections, keyName{Key: k, Name: c.Name, Note: c.Connector})
	}
	for k, v := range res.Doc.Variables {
		sum.Variables = append(sum.Variables, keyName{Key: k, Name: v.Name, Note: v.Context})
	}
	for k, a := range res.Doc.Applications {
		sum.Applications = append(sum.Applications, keyName{Key: k, Name: a.Name})
	}
	for _, l := range [][]keyName{sum.Flows, sum.Connections, sum.Variables, sum.Applications} {
		sort.Slice(l, func(i, j int) bool { return l[i].Key < l[j].Key })
	}
	return sum
}

// --- drafts ---

func (s *Server) createDraft(w http.ResponseWriter, r *http.Request) error {
	var input struct {
		InventoryID     string `json:"inventoryId"`
		TargetProfileID string `json:"targetProfileId"`
		Environment     string `json:"environment"`
	}
	if err := decodeBody(w, r, &input); err != nil {
		return err
	}
	inv := s.dvInventories[input.InventoryID]
	if inv == nil {
		return problem(http.StatusNotFound, errors.New("inventory not found; load the source environment again"))
	}
	target, err := s.profile(input.TargetProfileID)
	if err != nil {
		return err
	}
	if target.EnvironmentID == inv.EnvironmentID {
		return problem(http.StatusBadRequest, errors.New("source and target environments must differ"))
	}
	env := input.Environment
	if env == "" {
		env = environmentSlug(target.Name)
	}
	now := time.Now().UTC()
	st := &draftState{ID: "d" + RandomToken()[:23], Revision: 1, CreatedAt: now, UpdatedAt: now,
		SourceProfileID: inv.ProfileID, SourceEnvironmentID: inv.EnvironmentID, TargetProfileID: target.ID,
		TargetEnvironmentID: target.EnvironmentID, Environment: env, InventoryID: inv.ID,
		Flows: []string{}, Applications: []string{}, Excluded: []string{}, KeepTarget: []string{}, Parameters: map[string]string{},
		SecretNames: map[string]string{}, NewSecretFields: map[string]string{}, Acknowledged: []string{}, NotSecret: []string{},
		Base: map[string]string{}, Submissions: []string{}}
	rt, err := s.fetchTarget(r.Context(), st)
	if err != nil {
		return err
	}
	s.recompute(st, rt)
	if err := s.saveDraft(st, rt); err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(s.view(st, rt))
	return nil
}

// fetchTarget reads the target tfvars, customer_secrets.yml, secrets.tf base
// and the other environments' tfvars from the default branch.
func (s *Server) fetchTarget(ctx context.Context, st *draftState) (*draftRuntime, error) {
	client, cfg, err := s.gitlabSetup()
	if err != nil {
		return nil, err
	}
	paths := cfg.Paths()
	envPath, err := paths.EnvironmentPath(st.Environment)
	if err != nil {
		return nil, problem(http.StatusBadRequest, errors.New("environment name must match ^[A-Za-z0-9][A-Za-z0-9_-]{0,62}$"))
	}
	project, err := client.VerifyProject(ctx)
	if err != nil {
		return nil, apiErr(http.StatusBadGateway, "preflight", err.Error(), map[string]any{"retryable": true})
	}
	rt := &draftRuntime{}
	base := map[string]string{}
	for _, p := range []string{envPath, paths.SecretsTFPath, paths.CustomerSecretsPath} {
		f, err := client.GetFile(ctx, project.DefaultBranch, p)
		if err != nil {
			return nil, problem(http.StatusBadGateway, err)
		}
		base[p] = f.LastCommitID
		switch p {
		case envPath:
			if !f.Exists {
				rt.target = promote.NewTarget(st.TargetEnvironmentID)
				continue
			}
			doc, err := tfvars.Parse(f.Content, envPath)
			if err != nil {
				return nil, apiErr(http.StatusUnprocessableEntity, "target_invalid", envPath+" is not a valid tfvars file: "+err.Error(), nil)
			}
			if doc.EnvironmentID != st.TargetEnvironmentID {
				return nil, apiErr(http.StatusUnprocessableEntity, "target_mismatch", envPath+" belongs to a different PingOne environment", nil)
			}
			rt.target = doc
		case paths.CustomerSecretsPath:
			if !f.Exists {
				return nil, apiErr(http.StatusUnprocessableEntity, "customer_secrets_missing", paths.CustomerSecretsPath+" does not exist on the default branch", nil)
			}
			if _, err := pipelinesecrets.Parse(f.Content); err != nil {
				return nil, apiErr(http.StatusUnprocessableEntity, "customer_secrets_invalid", err.Error(), nil)
			}
			rt.secretsYAML = f.Content
		}
	}
	// secrets.tf is shared by every environment, so it declares the union
	// of their secret keys.
	entries, err := client.ListTree(ctx, project.DefaultBranch, paths.EnvironmentsDir)
	if err != nil {
		return nil, problem(http.StatusBadGateway, err)
	}
	for _, e := range entries {
		if e.Type != "blob" || e.Path == envPath || !strings.HasSuffix(e.Path, ".tfvars") {
			continue
		}
		if _, err := paths.Classify(e.Path); err != nil {
			continue
		}
		f, err := client.GetFile(ctx, project.DefaultBranch, e.Path)
		if err != nil {
			return nil, problem(http.StatusBadGateway, err)
		}
		if doc, err := tfvars.Parse(f.Content, e.Path); err == nil {
			rt.otherSecrets = append(rt.otherSecrets, tfvars.Validate(doc).RequiredSecrets...)
		}
	}
	rt.otherSecrets = uniqueSorted(rt.otherSecrets)
	st.Base = base
	return rt, nil
}

func (s *Server) saveDraft(st *draftState, rt *draftRuntime) error {
	st.UpdatedAt = time.Now().UTC()
	if err := s.store.SaveDocument(store.KindDrafts, st.ID, st); err != nil {
		return err
	}
	s.draftRuntimes[st.ID] = rt
	return nil
}

func (s *Server) loadDraft(id string) (*draftState, *draftRuntime, error) {
	var st draftState
	if err := s.store.LoadDocument(store.KindDrafts, id, &st); err != nil {
		if errors.Is(err, store.ErrRecordNotFound) || err.Error() == "invalid document ID" {
			return nil, nil, problem(http.StatusNotFound, errors.New("draft not found"))
		}
		return nil, nil, err
	}
	return &st, s.draftRuntimes[id], nil
}

func (s *Server) listDrafts(w http.ResponseWriter) error {
	ids, err := s.store.ListDocuments(store.KindDrafts)
	if err != nil {
		return err
	}
	out := []map[string]any{}
	for _, id := range ids {
		var st draftState
		if s.store.LoadDocument(store.KindDrafts, id, &st) == nil {
			out = append(out, map[string]any{"id": st.ID, "revision": st.Revision, "environment": st.Environment, "updatedAt": st.UpdatedAt,
				"needsRebase": s.draftRuntimes[id] == nil || s.dvInventories[st.InventoryID] == nil})
		}
	}
	writeJSON(w, out)
	return nil
}

func (s *Server) view(st *draftState, rt *draftRuntime) draftView {
	if rt != nil && rt.computed != nil && s.dvInventories[st.InventoryID] != nil {
		v := rt.computed.view
		v.Revision, v.UpdatedAt, v.Submissions = st.Revision, st.UpdatedAt, append([]string{}, st.Submissions...)
		return v
	}
	cfg, _ := gitlab.LoadConfig(s.store.Directory())
	path, _ := cfg.Paths().EnvironmentPath(st.Environment)
	return draftView{ID: st.ID, Revision: st.Revision, Environment: st.Environment, TargetPath: path, NeedsRebase: true,
		SourceEnvironment: st.SourceEnvironmentID, TargetEnvironment: st.TargetEnvironmentID,
		Flows: st.Flows, Applications: st.Applications, Excluded: st.Excluded, KeepTarget: st.KeepTarget, Parameters: st.Parameters,
		Blockers: []string{"rebase the draft: its inventory or target files are not loaded"}, Submissions: st.Submissions, UpdatedAt: st.UpdatedAt}
}

// recompute rebuilds the promoted files and the view from state.
func (s *Server) recompute(st *draftState, rt *draftRuntime) {
	inv := s.dvInventories[st.InventoryID]
	cfg, _ := gitlab.LoadConfig(s.store.Directory())
	paths := cfg.Paths()
	envPath, _ := paths.EnvironmentPath(st.Environment)
	v := draftView{ID: st.ID, Revision: st.Revision, Environment: st.Environment, TargetPath: envPath,
		SourceEnvironment: st.SourceEnvironmentID, TargetEnvironment: st.TargetEnvironmentID,
		Flows: st.Flows, Applications: st.Applications, Excluded: st.Excluded, KeepTarget: st.KeepTarget, Parameters: st.Parameters,
		Dependencies: []promote.Dependency{}, Blocks: []promote.Block{}, Problems: []tfvars.Problem{}, Warnings: []any{},
		RequiredSecrets: []requiredSecret{}, SecretCandidates: []string{}, NewVaultEntries: []vaultEntry{}, MissingParameters: []string{},
		Changes: []tfvars.Change{}, Blockers: []string{}, SecretSites: []translate.Secret{}, Decisions: []string{}}
	comp := &draftComputed{}
	rt.computed = comp
	defer func() {
		comp.view.ReadyToSubmit = len(comp.view.Blockers) == 0 && comp.tfvars != nil && comp.customerYM != nil && comp.secretsTF != nil
	}()
	if inv == nil || rt.target == nil {
		v.NeedsRebase = true
		v.Blockers = append(v.Blockers, "rebase the draft: its inventory or target files are not loaded")
		comp.view = v
		return
	}
	opts := translate.Options{SecretNames: st.SecretNames, NotSecret: setOf(st.NotSecret), Acknowledged: setOf(st.Acknowledged)}
	src, err := promote.TranslateFor(inv.Inv, rt.target, opts)
	var te *translate.Error
	if errors.As(err, &te) {
		v.Decisions = te.Problems
		v.Blockers = append(v.Blockers, te.Problems...)
	} else if err != nil {
		v.Blockers = append(v.Blockers, err.Error())
	}
	if src == nil {
		comp.view = v
		return
	}
	v.SecretSites = append(v.SecretSites, src.Secrets...)
	for _, w := range src.Warnings {
		v.Warnings = append(v.Warnings, w)
	}
	if len(st.Flows) == 0 && len(st.Applications) == 0 {
		v.Blockers = append(v.Blockers, "select at least one flow or application")
	}
	plan, err := promote.Plan(src, rt.target, promote.Selection{Flows: st.Flows, Applications: st.Applications, Exclude: setOf(st.Excluded)})
	if err != nil {
		v.Blockers = append(v.Blockers, err.Error())
		comp.view = v
		return
	}
	v.Dependencies, v.Blocks = plan.Dependencies, plan.Blocks
	for _, w := range plan.Warnings {
		v.Warnings = append(v.Warnings, w)
	}
	for _, b := range plan.Blocks {
		v.Blockers = append(v.Blockers, b.Message)
	}
	if plan.Doc == nil {
		comp.view = v
		return
	}
	doc := plan.Doc
	for _, item := range st.KeepTarget {
		kind, key, _ := strings.Cut(item, ":")
		keepTargetEntry(doc, rt.target, kind, key)
	}
	for k, val := range st.Parameters {
		doc.Parameters[k] = val
	}
	report := tfvars.Validate(doc)
	v.Problems = report.Problems
	// Heuristic secret matches block only when the promoted file uses them.
	needed := setOf(report.RequiredSecrets)
	siteKey := map[string]string{}
	for _, sec := range src.Secrets {
		siteKey[sec.Site] = sec.Key
	}
	for _, w := range src.Warnings {
		if w.NeedsAck && w.Site != "" && needed[siteKey[w.Site]] {
			v.Blockers = append(v.Blockers, "acknowledge or mark not secret: "+w.Site)
		}
	}
	for _, p := range report.Problems {
		v.Blockers = append(v.Blockers, p.Path+": "+p.Message)
	}
	for _, k := range report.RequiredParameters {
		if _, ok := doc.Parameters[k]; !ok {
			v.MissingParameters = append(v.MissingParameters, k)
		}
	}
	out, err := tfvars.Write(doc)
	if err != nil {
		v.Blockers = append(v.Blockers, err.Error())
		comp.view = v
		return
	}
	comp.tfvars = out
	comp.changes = tfvars.Diff(rt.target, doc)
	if comp.changes == nil {
		comp.changes = []tfvars.Change{}
	}
	v.Changes = comp.changes

	// Secrets: secrets.tf declares every environment's keys; new pipeline
	// entries are added for keys customer_secrets.yml does not declare.
	if comp.secretsTF, err = tfvars.WriteSecretsTF(append(append([]string{}, rt.otherSecrets...), report.RequiredSecrets...)); err != nil {
		v.Blockers = append(v.Blockers, err.Error())
	}
	ps, err := pipelinesecrets.Parse(rt.secretsYAML)
	if err != nil {
		v.Blockers = append(v.Blockers, err.Error())
		comp.view = v
		return
	}
	check := ps.Check(pipelinesecrets.DefaultConfig, report.RequiredSecrets)
	v.SecretCandidates = check.Candidates
	sites := map[string][]string{}
	for _, sec := range src.Secrets {
		sites[sec.Key] = append(sites[sec.Key], sec.Site)
	}
	var add []pipelinesecrets.NewEntry
	undeclared := setOf(check.Undeclared)
	commented := setOf(check.CommentedOut)
	for _, k := range report.RequiredSecrets {
		rs := requiredSecret{Key: k, Sites: sites[k], Declared: !undeclared[k] && !commented[k], CommentedOut: commented[k]}
		if undeclared[k] {
			field := st.NewSecretFields[k]
			if field == "" {
				field = k
			}
			rs.New, rs.VaultField = true, field
			add = append(add, pipelinesecrets.NewEntry{VarName: k, Field: field})
			v.NewVaultEntries = append(v.NewVaultEntries, vaultEntry{EnvVar: pipelinesecrets.EnvPrefix + k,
				Vault: pipelinesecrets.DefaultConfig.PathPrefix + "/" + field + "@" + pipelinesecrets.DefaultConfig.Mount})
		}
		if commented[k] {
			v.Blockers = append(v.Blockers, "uncomment TF_VAR_"+k+" in "+paths.CustomerSecretsPath+" (pingone-promote never edits commented entries)")
		}
		v.RequiredSecrets = append(v.RequiredSecrets, rs)
	}
	if comp.customerYM, err = ps.Add(pipelinesecrets.DefaultConfig, add); err != nil {
		v.Blockers = append(v.Blockers, err.Error())
	}
	comp.summary = describe(st, v)
	comp.view = v
}

func keepTargetEntry(doc, target *tfvars.Document, kind, key string) {
	switch kind {
	case promote.KindConnection:
		if e, ok := target.Connections[key]; ok {
			doc.Connections[key] = e
		}
	case promote.KindVariable:
		if e, ok := target.Variables[key]; ok {
			doc.Variables[key] = e
		}
	case promote.KindFlow:
		if e, ok := target.Flows[key]; ok {
			doc.Flows[key] = e
		}
	case promote.KindApplication:
		if e, ok := target.Applications[key]; ok {
			doc.Applications[key] = e
		}
	}
}

// describe renders the merge request / commit description: the per-key
// change summary (#4) and the Vault entries to create. It never includes
// values.
func describe(st *draftState, v draftView) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## pingone-promote: %s\n\n", st.Environment)
	fmt.Fprintf(&b, "Source PingOne environment `%s`, target `%s`. Draft `%s` revision %d.\n\n", st.SourceEnvironmentID, st.TargetEnvironmentID, st.ID, st.Revision)
	b.WriteString("### Changes\n\n")
	if len(v.Changes) == 0 {
		b.WriteString("No tfvars changes.\n\n")
	} else {
		b.WriteString("| Collection | Key | Change | Fields |\n| --- | --- | --- | --- |\n")
		for _, c := range v.Changes {
			fields := strings.Join(c.Fields, ", ")
			if len(fields) > 300 {
				fields = fields[:300] + "..."
			}
			fmt.Fprintf(&b, "| %s | `%s` | %s | %s |\n", c.Collection, c.Key, c.Kind, strings.ReplaceAll(fields, "|", "\\|"))
		}
		b.WriteString("\n")
	}
	b.WriteString("### New Vault entries to create\n\n")
	if len(v.NewVaultEntries) == 0 {
		b.WriteString("None.\n\n")
	} else {
		b.WriteString("Create these fields in Vault for every environment before running the pipeline:\n\n")
		for _, e := range v.NewVaultEntries {
			fmt.Fprintf(&b, "- `%s` at `%s`\n", e.EnvVar, e.Vault)
		}
		b.WriteString("\n")
	}
	if len(v.RequiredSecrets) > 0 {
		b.WriteString("### Required secrets\n\n")
		for _, r := range v.RequiredSecrets {
			state := "declared"
			if r.New {
				state = "new"
			}
			fmt.Fprintf(&b, "- `%s` (%s)\n", r.Key, state)
		}
		b.WriteString("\n")
	}
	b.WriteString("Terraform plan in the pipeline is the authority for live effects.\n")
	return b.String()
}

// --- patch ---

type draftOp struct {
	Op    string `json:"op"`
	Kind  string `json:"kind,omitempty"`
	Key   string `json:"key,omitempty"`
	Value string `json:"value,omitempty"`
	Site  string `json:"site,omitempty"`
	// mapSecret: Existing names a declared TF_VAR_ entry (without TF_VAR_);
	// otherwise Name (and optionally Field) creates a new entry.
	Existing string `json:"existing,omitempty"`
	Name     string `json:"name,omitempty"`
	Field    string `json:"field,omitempty"`
	Keep     *bool  `json:"keep,omitempty"`
}

var fieldRE = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

func validKind(k string) bool {
	return k == promote.KindFlow || k == promote.KindApplication || k == promote.KindConnection || k == promote.KindVariable
}

func applyOp(st *draftState, op draftOp) error {
	bad := func(msg string) error { return apiErr(http.StatusBadRequest, "invalid_op", op.Op+": "+msg, nil) }
	item := op.Kind + ":" + op.Key
	switch op.Op {
	case "select", "deselect":
		if op.Kind != promote.KindFlow && op.Kind != promote.KindApplication || !tfvars.IsLogicalKey(op.Key) {
			return bad("kind must be flow or application with a valid key")
		}
		list := &st.Flows
		if op.Kind == promote.KindApplication {
			list = &st.Applications
		}
		if op.Op == "select" {
			*list = uniqueSorted(append(*list, op.Key))
		} else {
			*list = without(*list, op.Key)
		}
	case "excludeDependency", "includeDependency":
		if !validKind(op.Kind) || !tfvars.IsLogicalKey(op.Key) {
			return bad("needs a kind and key")
		}
		if op.Op == "excludeDependency" {
			st.Excluded = uniqueSorted(append(st.Excluded, item))
		} else {
			st.Excluded = without(st.Excluded, item)
		}
	case "keepTargetValue":
		if !validKind(op.Kind) || !tfvars.IsLogicalKey(op.Key) {
			return bad("needs a kind and key")
		}
		if op.Keep != nil && !*op.Keep {
			st.KeepTarget = without(st.KeepTarget, item)
		} else {
			st.KeepTarget = uniqueSorted(append(st.KeepTarget, item))
		}
	case "setParameter":
		if !tfvars.IsValueKey(op.Key) || len(op.Value) > 4096 {
			return bad("parameter keys must match ^[a-z0-9_]{1,128}$ and values be at most 4096 bytes")
		}
		st.Parameters[op.Key] = op.Value
	case "mapSecret":
		if op.Site == "" || len(op.Site) > 512 {
			return bad("needs the secret site")
		}
		name := op.Existing
		if name == "" {
			name = op.Name
		}
		if !tfvars.IsSecretKey(name) {
			return bad("secret name must match " + tfvars.SecretKeyPattern)
		}
		st.SecretNames[op.Site] = name
		if op.Existing == "" {
			field := op.Field
			if field == "" {
				field = name
			}
			if !fieldRE.MatchString(field) {
				return bad("Vault field must match " + fieldRE.String())
			}
			st.NewSecretFields[name] = field
		} else {
			delete(st.NewSecretFields, name)
		}
	case "acknowledgeSecret":
		if op.Site == "" {
			return bad("needs the secret site")
		}
		st.Acknowledged = uniqueSorted(append(st.Acknowledged, op.Site))
		st.NotSecret = without(st.NotSecret, op.Site)
	case "markNotSecret":
		if op.Site == "" {
			return bad("needs the secret site")
		}
		st.NotSecret = uniqueSorted(append(st.NotSecret, op.Site))
		st.Acknowledged = without(st.Acknowledged, op.Site)
	default:
		return bad("unknown operation")
	}
	return nil
}

func staleRevision(st *draftState, got int) error {
	if got != st.Revision {
		return apiErr(http.StatusConflict, "stale_revision", "the draft changed; reload it and apply your change again",
			map[string]any{"revision": st.Revision})
	}
	return nil
}

func (s *Server) patchDraft(w http.ResponseWriter, r *http.Request, st *draftState, rt *draftRuntime) error {
	var input struct {
		Revision int       `json:"revision"`
		Ops      []draftOp `json:"ops"`
	}
	if err := decodeBody(w, r, &input); err != nil {
		return err
	}
	if err := staleRevision(st, input.Revision); err != nil {
		return err
	}
	if len(input.Ops) == 0 || len(input.Ops) > 200 {
		return apiErr(http.StatusBadRequest, "invalid_op", "send between 1 and 200 operations", nil)
	}
	for _, op := range input.Ops {
		if err := applyOp(st, op); err != nil {
			return err
		}
	}
	st.Revision++
	if rt == nil {
		rt = &draftRuntime{}
	}
	s.recompute(st, rt)
	if err := s.saveDraft(st, rt); err != nil {
		return err
	}
	writeJSON(w, s.view(st, rt))
	return nil
}

func (s *Server) draftFiles(w http.ResponseWriter, st *draftState, rt *draftRuntime) error {
	if rt == nil || rt.computed == nil || rt.computed.tfvars == nil {
		return apiErr(http.StatusConflict, "not_ready", "the draft has no files yet; resolve its blockers or rebase it", nil)
	}
	cfg, _ := gitlab.LoadConfig(s.store.Directory())
	paths := cfg.Paths()
	envPath, _ := paths.EnvironmentPath(st.Environment)
	writeJSON(w, map[string]any{"revision": st.Revision,
		"tfvars":          map[string]string{"path": envPath, "content": string(rt.computed.tfvars)},
		"secretsTf":       map[string]string{"path": paths.SecretsTFPath, "content": string(rt.computed.secretsTF)},
		"customerSecrets": map[string]string{"path": paths.CustomerSecretsPath, "content": string(rt.computed.customerYM)}})
	return nil
}

func (s *Server) rebaseDraft(w http.ResponseWriter, r *http.Request, st *draftState) error {
	var input struct {
		Revision         int  `json:"revision"`
		RefreshInventory bool `json:"refreshInventory"`
	}
	if err := decodeBody(w, r, &input); err != nil {
		return err
	}
	if err := staleRevision(st, input.Revision); err != nil {
		return err
	}
	if input.RefreshInventory || s.dvInventories[st.InventoryID] == nil {
		p, err := s.profile(st.SourceProfileID)
		if err != nil {
			return err
		}
		if p.EnvironmentID != st.SourceEnvironmentID {
			return problem(http.StatusConflict, errors.New("the source profile now points at another environment"))
		}
		c, err := s.client(p)
		if err != nil {
			return err
		}
		inv := davinci.Read(r.Context(), c, p.EnvironmentID)
		rec := &dvInventory{ID: RandomToken()[:24], ProfileID: p.ID, EnvironmentID: p.EnvironmentID, LoadedAt: time.Now().UTC(), Inv: inv}
		rec.Summary = summarize(rec)
		s.dvInventories[rec.ID] = rec
		st.InventoryID = rec.ID
	}
	rt, err := s.fetchTarget(r.Context(), st)
	if err != nil {
		return err
	}
	st.Revision++
	s.recompute(st, rt)
	if err := s.saveDraft(st, rt); err != nil {
		return err
	}
	writeJSON(w, s.view(st, rt))
	return nil
}

// --- submit and revert ---

func submissionError(err error) error {
	var conflict *gitlab.ConflictError
	var pre *gitlab.PreflightError
	var partial *gitlab.PartialError
	switch {
	case errors.As(err, &conflict):
		return apiErr(http.StatusConflict, "conflict", conflict.Error(), map[string]any{"paths": conflict.Paths, "fix": "rebase"})
	case errors.As(err, &partial):
		return apiErr(http.StatusConflict, "partial", partial.Error(), map[string]any{"branch": partial.Branch, "commitId": partial.CommitID,
			"mergeRequest": partial.MR, "stage": partial.Stage, "resumable": partial.Resumable})
	case errors.As(err, &pre):
		return apiErr(http.StatusFailedDependency, "preflight", pre.Error(), map[string]any{"retryable": true})
	}
	return apiErr(http.StatusBadGateway, "gitlab_failed", err.Error(), nil)
}

func (s *Server) submitDraft(w http.ResponseWriter, r *http.Request, st *draftState, rt *draftRuntime) error {
	var input struct {
		Revision      int    `json:"revision"`
		Mode          string `json:"mode"`
		BranchName    string `json:"branchName"`
		CommitMessage string `json:"commitMessage"`
		Title         string `json:"title"`
		Description   string `json:"description"`
		Resume        bool   `json:"resume"`
	}
	if err := decodeBody(w, r, &input); err != nil {
		return err
	}
	if err := staleRevision(st, input.Revision); err != nil {
		return err
	}
	if rt == nil || rt.computed == nil || s.dvInventories[st.InventoryID] == nil {
		return apiErr(http.StatusConflict, "needs_rebase", "rebase the draft before submitting", nil)
	}
	v := rt.computed.view
	if len(v.Blockers) > 0 || rt.computed.tfvars == nil {
		return apiErr(http.StatusUnprocessableEntity, "blocked", "the draft has unresolved blockers", map[string]any{"blockers": v.Blockers})
	}
	mode := gitlab.Mode(input.Mode)
	if mode == "" {
		mode = gitlab.ModeMR
	}
	id := st.ID + "-r" + strconv.Itoa(st.Revision)
	branch := input.BranchName
	if branch == "" {
		branch = "pingone-promote/" + st.Environment + "-" + id
	}
	msg := input.CommitMessage
	if strings.TrimSpace(msg) == "" {
		msg = "Promote DaVinci configuration to " + st.Environment
	}
	description := rt.computed.summary
	if strings.TrimSpace(input.Description) != "" {
		description = strings.TrimSpace(input.Description) + "\n\n" + description
	}
	client, cfg, err := s.gitlabSetup()
	if err != nil {
		return err
	}
	paths := cfg.Paths()
	envPath, _ := paths.EnvironmentPath(st.Environment)
	cs := gitlab.ChangeSet{ID: id, Mode: mode, Branch: branch, CommitMessage: msg + "\n\n" + changeLines(rt.computed.changes, v.NewVaultEntries),
		Title: input.Title, Description: description, Resume: input.Resume, Base: st.Base,
		Files: []gitlab.File{{Path: envPath, Content: rt.computed.tfvars}, {Path: paths.SecretsTFPath, Content: rt.computed.secretsTF},
			{Path: paths.CustomerSecretsPath, Content: rt.computed.customerYM}}}
	res, err := s.submitter(client, cfg).Submit(r.Context(), cs)
	if err != nil {
		return submissionError(err)
	}
	rec := submissionRecord{ID: id, Kind: "submit", DraftID: st.ID, Environment: st.Environment, Mode: string(mode), Branch: res.Branch,
		CommitID: res.CommitID, Paths: []string{envPath, paths.SecretsTFPath, paths.CustomerSecretsPath}, CreatedAt: time.Now().UTC()}
	if res.MR != nil {
		rec.MergeRequestURL, rec.MergeRequestIID = res.MR.WebURL, res.MR.IID
	}
	if err := s.store.SaveDocument(store.KindSubmissions, rec.ID, rec); err != nil {
		return err
	}
	st.Submissions = uniqueSorted(append(st.Submissions, rec.ID))
	if err := s.saveDraft(st, rt); err != nil {
		return err
	}
	writeJSON(w, map[string]any{"submission": rec, "resumed": res.Resumed})
	return nil
}

// changeLines is the compact commit-message form of the change summary.
func changeLines(changes []tfvars.Change, vault []vaultEntry) string {
	var b strings.Builder
	for i, c := range changes {
		if i == 50 {
			fmt.Fprintf(&b, "... and %d more changes\n", len(changes)-50)
			break
		}
		fmt.Fprintf(&b, "%s %s[%q]\n", c.Kind, c.Collection, c.Key)
	}
	for _, e := range vault {
		fmt.Fprintf(&b, "new Vault entry %s\n", e.EnvVar)
	}
	return strings.TrimRight(b.String(), "\n")
}

func (s *Server) listSubmissions(w http.ResponseWriter) error {
	ids, err := s.store.ListDocuments(store.KindSubmissions)
	if err != nil {
		return err
	}
	out := []submissionRecord{}
	for _, id := range ids {
		var rec submissionRecord
		if s.store.LoadDocument(store.KindSubmissions, id, &rec) == nil {
			out = append(out, rec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	writeJSON(w, out)
	return nil
}

func (s *Server) revertSubmission(w http.ResponseWriter, r *http.Request, id string) error {
	var input struct {
		Mode        string `json:"mode"`
		BranchName  string `json:"branchName"`
		Confirm     bool   `json:"confirm"`
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	if err := decodeBody(w, r, &input); err != nil {
		return err
	}
	var orig submissionRecord
	if err := s.store.LoadDocument(store.KindSubmissions, id, &orig); err != nil {
		return problem(http.StatusNotFound, errors.New("submission not found"))
	}
	client, cfg, err := s.gitlabSetup()
	if err != nil {
		return err
	}
	revID := orig.ID + "-revert"
	if len(revID) > 80 {
		revID = revID[:80]
	}
	branch := input.BranchName
	if branch == "" {
		branch = "pingone-promote/revert-" + orig.ID
	}
	desc := strings.TrimSpace(input.Description)
	if desc != "" {
		desc += "\n\n"
	}
	desc += fmt.Sprintf("Reverts pingone-promote submission `%s` (commit `%s`) for environment %s.\n\nFiles: %s\n", orig.ID, orig.CommitID, orig.Environment, strings.Join(orig.Paths, ", "))
	res, err := s.submitter(client, cfg).Revert(r.Context(), gitlab.RevertRequest{ID: revID, CommitID: orig.CommitID, Paths: orig.Paths,
		Mode: gitlab.Mode(input.Mode), Branch: branch, Title: input.Title, Description: desc, Confirm: input.Confirm})
	if err != nil {
		return submissionError(err)
	}
	mode := input.Mode
	if mode == "" {
		mode = string(gitlab.ModeMR)
	}
	rec := submissionRecord{ID: revID, Kind: "revert", RevertOf: orig.ID, Environment: orig.Environment, Mode: mode, Branch: res.Branch,
		CommitID: res.CommitID, Paths: orig.Paths, CreatedAt: time.Now().UTC()}
	if res.MR != nil {
		rec.MergeRequestURL, rec.MergeRequestIID = res.MR.WebURL, res.MR.IID
	}
	if err := s.store.SaveDocument(store.KindSubmissions, rec.ID, rec); err != nil {
		return err
	}
	writeJSON(w, map[string]any{"submission": rec})
	return nil
}
