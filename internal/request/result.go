package request

import (
	"encoding/json"
	"os"
	"sort"

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
	Role    string `json:"role"`   // selected | dependency
	Action  string `json:"action"` // added | updated | kept_differs | unchanged | removed | moved
	Status  string `json:"status"` // new | changed-in-source | matches | git-newer | changed-in-both
	File    string `json:"file,omitempty"`
	From    string `json:"from,omitempty"`
	Reason  string `json:"reason,omitempty"`
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

// Summary counts changes by action.
type Summary struct {
	Added       int `json:"added"`
	Updated     int `json:"updated"`
	KeptDiffers int `json:"kept_differs"`
	Unchanged   int `json:"unchanged"`
	Removed     int `json:"removed"`
	Moved       int `json:"moved"`
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
}

// MaxDiff bounds result.diff.
const MaxDiff = 1 << 20

func newResult() *Result {
	return &Result{ResultVersion: ResultVersion, Selected: []Selected{}, Changes: []ChangeOut{}, Files: []FileOut{},
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
			File: c.File, From: c.From, Reason: c.Reason})
		switch c.Action {
		case promote.ActionAdded:
			r.Summary.Added++
		case promote.ActionUpdated:
			r.Summary.Updated++
		case promote.ActionKeptDiffers:
			r.Summary.KeptDiffers++
		case promote.ActionUnchanged:
			r.Summary.Unchanged++
		case promote.ActionRemoved:
			r.Summary.Removed++
		case promote.ActionMoved:
			r.Summary.Moved++
		}
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
		Summary       Summary   `json:"summary"`
		BlockedEnvs   []string  `json:"blockedEnvs,omitempty"`
		Error         *ErrorOut `json:"error,omitempty"`
	}
	l := line{ResultVersion: r.ResultVersion, RequestID: r.RequestID, Status: r.Status, MRUrl: r.MRUrl, BranchURL: r.BranchURL,
		CommitID: r.CommitID, Summary: r.Summary, Error: r.Error}
	for env := range r.Blockers {
		l.BlockedEnvs = append(l.BlockedEnvs, env)
	}
	sort.Strings(l.BlockedEnvs)
	b, _ := json.Marshal(l)
	return LogPrefix + string(b)
}
