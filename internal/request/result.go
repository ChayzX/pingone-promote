package request

import (
	"encoding/json"
	"os"
	"sort"
	"strings"

	"pingone-promote/internal/promote"
)

// ResultVersion is the result.json schema version.
const ResultVersion = 1

// Statuses.
const (
	StatusPreviewed        = "previewed"         // dryRun: nothing committed
	StatusSubmitted        = "submitted"         // one commit made (and an MR in mr mode)
	StatusNoChanges        = "no_changes"        // the repository already matches; request file deleted
	StatusAlreadySubmitted = "already_submitted" // this requestId was committed before; nothing new
	StatusBlocked          = "blocked"           // blockers; nothing committed
	StatusFailed           = "failed"            // typed error; nothing committed (except partial)
)

// Error codes the DaVinci app knows (contract.ERROR_CODES). Type refines
// the cause: validation, export, merge, gitlab, internal.
const (
	ErrConflict  = "conflict"
	ErrPreflight = "preflight"
	ErrPartial   = "partial"
	ErrBlocked   = "blocked"
)

// RequestBlockers is the blockers key for problems that belong to no
// single environment (environment names cannot start with "_").
const RequestBlockers = "_request"

// Selected is one resolved selection.
type Selected struct {
	Query   string `json:"query"`
	Address string `json:"address"`
	Name    string `json:"name"`
	ID      string `json:"id,omitempty"`
}

// ChangeOut is one address in result.changes.
type ChangeOut struct {
	Address string `json:"address"`
	Role    string `json:"role"`   // selected | dependency | environment | excluded
	Action  string `json:"action"` // added | updated | kept_differs | unchanged | moved | excluded
	Status  string `json:"status"` // new | changed-in-source | matches | git-newer | changed-in-both | not-compared
	File    string `json:"file,omitempty"`
	From    string `json:"from,omitempty"`
	Reason  string `json:"reason,omitempty"`
	// Var and SourceID: for an excluded resource referenced by ID, the
	// per-environment variable and its value in the source environment.
	Var      string `json:"var,omitempty"`
	SourceID string `json:"sourceId,omitempty"`
}

// SecretOut is one required secret.
type SecretOut struct {
	// Key is terraformer's variable name, the key of secretMappings.
	Key string `json:"key"`
	// VarName is the Terraform root variable after mapping; the pipeline
	// sets TF_VAR_<varName>.
	VarName string `json:"varName"`
	// Field is the Vault field under the configured path prefix.
	Field string `json:"field"`
	// Declared is true when customer_secrets.yml already declares it;
	// otherwise this request adds the entry and the value must be created
	// in Vault for every environment.
	Declared bool   `json:"declared"`
	Vault    string `json:"vault"`
}

// BlockerOut is one blocker.
type BlockerOut struct {
	Code    string `json:"code"`
	Var     string `json:"var,omitempty"`
	Address string `json:"address,omitempty"`
	Message string `json:"message"`
}

// FileOut is one file of the commit.
type FileOut struct {
	Path   string `json:"path"`
	Action string `json:"action"` // create | update | delete
}

// ErrorOut is a typed error.
type ErrorOut struct {
	Code       string     `json:"code"` // conflict | preflight | partial | blocked
	Type       string     `json:"type"` // validation | export | merge | gitlab | internal
	Message    string     `json:"message"`
	Retryable  bool       `json:"retryable"`
	Field      string     `json:"field,omitempty"`
	Candidates []Selected `json:"candidates,omitempty"`
}

// Counts are changes by action.
type Counts struct {
	Added       int `json:"added"`
	Updated     int `json:"updated"`
	KeptDiffers int `json:"kept_differs"`
	Unchanged   int `json:"unchanged"`
	Moved       int `json:"moved"`
	Excluded    int `json:"excluded"`
}

func (c *Counts) add(action promote.Action) {
	switch action {
	case promote.ActionAdded:
		c.Added++
	case promote.ActionUpdated:
		c.Updated++
	case promote.ActionKeptDiffers:
		c.KeptDiffers++
	case promote.ActionUnchanged:
		c.Unchanged++
	case promote.ActionMoved:
		c.Moved++
	case promote.ActionExcluded:
		c.Excluded++
	}
}

// TypeCounts are the changes of one resource type.
type TypeCounts struct {
	Total int `json:"total"`
	Counts
}

// Summary counts changes by action, overall and by resource type. ByType
// is complete even where Changes is long, so a screen can lead with it.
type Summary struct {
	Counts
	ByType map[string]TypeCounts `json:"byType"`
}

// Result is result.json, version 1.
type Result struct {
	ResultVersion   int                     `json:"resultVersion"`
	RequestID       string                  `json:"requestId"`
	RequestedBy     string                  `json:"requestedBy"`
	Status          string                  `json:"status"`
	DryRun          bool                    `json:"dryRun"`
	Mode            string                  `json:"mode"`
	Source          string                  `json:"source"`
	Scope           string                  `json:"scope"`
	Branch          string                  `json:"branch"`
	CommitID        string                  `json:"commitId"`
	MRUrl           string                  `json:"mrUrl"`
	MRIid           int64                   `json:"mrIid"`
	BranchURL       string                  `json:"branchUrl"`
	DiffURL         string                  `json:"diffUrl"`
	Selected        []Selected              `json:"selected"`
	Changes         []ChangeOut             `json:"changes"`
	Summary         Summary                 `json:"summary"`
	Files           []FileOut               `json:"files"`
	RequiredSecrets []SecretOut             `json:"requiredSecrets"`
	Blockers        map[string][]BlockerOut `json:"blockers"`
	Warnings        []string                `json:"warnings"`
	Diff            string                  `json:"diff"`
	DiffTruncated   bool                    `json:"diffTruncated"`
	Error           *ErrorOut               `json:"error"`
	// Variables describes the per-environment values of this change, so a
	// screen can explain each missing value in plain language. Additive
	// (resultVersion stays 1); secrets are never listed.
	Variables map[string]VariableOut `json:"variables,omitempty"`
}

// VariableOut describes one per-environment value.
type VariableOut struct {
	// Kind is env_local (an environment-specific attribute or value) or
	// excluded_ref (the ID of an item that was not copied).
	Kind string `json:"kind"`
	// Resource is the address the value belongs to, or the address of the
	// item whose ID it holds.
	Resource string `json:"resource,omitempty"`
	// Attribute is the attribute path for a lifted value ("email.from.address").
	Attribute   string `json:"attribute,omitempty"`
	Description string `json:"description,omitempty"`
	// SourceValue is the source environment's value, shown as an example
	// (never a secret: secrets are not per-environment values).
	SourceValue string `json:"sourceValue,omitempty"`
}

// MaxDiff bounds result.diff. A whole-environment diff is cut at
// MaxDiffAll instead, so result.json (which also lists every change) stays
// about the size of a selected request's.
const (
	MaxDiff    = 1 << 20
	MaxDiffAll = 512 << 10
)

// truncateDiff cuts a diff to limit bytes at a line boundary.
func truncateDiff(diff string, limit int) (string, bool) {
	if len(diff) <= limit {
		return diff, false
	}
	cut := diff[:limit]
	if i := strings.LastIndexByte(cut, '\n'); i > 0 {
		cut = cut[:i+1]
	}
	return cut, true
}

func newResult() *Result {
	return &Result{ResultVersion: ResultVersion, Scope: ScopeSelected, Summary: Summary{ByType: map[string]TypeCounts{}}, Selected: []Selected{}, Changes: []ChangeOut{}, Files: []FileOut{},
		RequiredSecrets: []SecretOut{}, Blockers: map[string][]BlockerOut{}, Warnings: []string{}}
}

func (r *Result) fail(code, typ, msg string, retryable bool) *Result {
	r.Status = StatusFailed
	r.Error = &ErrorOut{Code: code, Type: typ, Message: msg, Retryable: retryable}
	return r
}

func (r *Result) setChanges(changes []promote.Change) {
	for _, c := range changes {
		r.Changes = append(r.Changes, ChangeOut{Address: c.Address, Role: string(c.Role), Action: string(c.Action), Status: string(c.Status),
			File: c.File, From: c.From, Reason: c.Reason, Var: c.Var, SourceID: c.SourceID})
		r.Summary.add(c.Action)
		typ, _, _ := strings.Cut(c.Address, ".")
		tc := r.Summary.ByType[typ]
		tc.Total++
		tc.add(c.Action)
		r.Summary.ByType[typ] = tc
	}
}

func (r *Result) setBlockers(bs []promote.Blocker) {
	for _, b := range bs {
		env := b.Env
		if env == "" {
			env = RequestBlockers
		}
		r.Blockers[env] = append(r.Blockers[env], BlockerOut{Code: b.Code, Var: b.Variable, Address: b.Address, Message: b.Message})
	}
}

func (r *Result) blocked() bool { return len(r.Blockers) > 0 }

// Write stores result.json (0600: it can name environment IDs).
func (r *Result) Write(path string) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

// LogPrefix starts the final log line.
const LogPrefix = "PINGONE_PROMOTE_RESULT "

// LogLine is the compact one-line form for the job log: the result without
// diff, changes and files (those are in result.json).
func (r *Result) LogLine() string {
	type line struct {
		ResultVersion int       `json:"resultVersion"`
		RequestID     string    `json:"requestId"`
		Status        string    `json:"status"`
		MRUrl         string    `json:"mrUrl,omitempty"`
		BranchURL     string    `json:"branchUrl,omitempty"`
		CommitID      string    `json:"commitId,omitempty"`
		Scope         string    `json:"scope"`
		Summary       Counts    `json:"summary"`
		BlockedEnvs   []string  `json:"blockedEnvs,omitempty"`
		Error         *ErrorOut `json:"error,omitempty"`
	}
	l := line{ResultVersion: r.ResultVersion, RequestID: r.RequestID, Status: r.Status, MRUrl: r.MRUrl, BranchURL: r.BranchURL,
		CommitID: r.CommitID, Scope: r.Scope, Summary: r.Summary.Counts, Error: r.Error}
	for env := range r.Blockers {
		l.BlockedEnvs = append(l.BlockedEnvs, env)
	}
	sort.Strings(l.BlockedEnvs)
	b, _ := json.Marshal(l)
	return LogPrefix + string(b)
}
