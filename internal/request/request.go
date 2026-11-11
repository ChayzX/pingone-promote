// Package request is the headless mode run by the GitLab CI job that a
// DaVinci front end triggers (ADR 0017). DaVinci creates the branch
// pingone-promote/<requestId> with .pingone-promote/request.json; the job
// reads it, exports the source environment with the pinned terraformer,
// neutralizes, merges additively into the branch's definitions and commits
// ONE commit to the same branch (or only previews, with dryRun).
package request

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/zclconf/go-cty/cty"
	ctyjson "github.com/zclconf/go-cty/cty/json"

	"pingone-promote/internal/gitlab"
	"pingone-promote/internal/secretscan"
)

// BranchPrefix is the default request branch namespace (Env.BranchPrefix
// overrides it, for example "CIAM-").
const BranchPrefix = gitlab.RequestBranchPrefix

// Version is the only supported requestVersion.
const Version = 1

// MaxSize bounds a request document.
const MaxSize = 256 << 10

// FlowRef selects a flow by DaVinci ID or by name (exact, case-insensitive,
// or a unique substring). At least one is set; when both are, the ID
// selects and the name is only checked (a mismatch is a warning).
type FlowRef struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

// Scopes of a request.
const (
	// ScopeSelected exports the listed items and their dependencies.
	ScopeSelected = "selected"
	// ScopeAll exports every supported resource of the source environment.
	ScopeAll = "all"
)

// WholeEnvironmentTitle prefixes the merge request title of a whole-
// environment request.
const WholeEnvironmentTitle = "[whole environment]"

// MaxExcludes bounds the exclude list.
const MaxExcludes = 200

// Exclude types, by the name the request uses.
const (
	ExcludeFlow        = "flow"
	ExcludeApplication = "application"
	ExcludeFlowPolicy  = "flowPolicy"
	ExcludeConnection  = "connection"
	ExcludeVariable    = "variable"
)

// ExcludeTypes maps an exclude type to the terraform resource type.
var ExcludeTypes = map[string]string{
	ExcludeFlow:        "pingone_davinci_flow",
	ExcludeApplication: "pingone_davinci_application",
	ExcludeFlowPolicy:  "pingone_davinci_application_flow_policy",
	ExcludeConnection:  "pingone_davinci_connector_instance",
	ExcludeVariable:    "pingone_davinci_variable",
}

// ExcludeRef selects a resource to leave out, the way an include selects
// one: by exact ID, then exact name, then a unique substring; or by its full
// terraform Address. Type is one of flow, application, flowPolicy,
// connection and variable, and is required unless Address is set.
type ExcludeRef struct {
	Type    string `json:"type,omitempty"`
	ID      string `json:"id,omitempty"`
	Name    string `json:"name,omitempty"`
	Address string `json:"address,omitempty"`
}

// Request is requestVersion 1.
type Request struct {
	RequestVersion int    `json:"requestVersion"`
	RequestID      string `json:"requestId"`
	RequestedBy    string `json:"requestedBy"`
	// Source is the environment to export (a name, e.g. "dev").
	Source string `json:"source"`
	// Scope is "selected" (default) or "all". all exports every supported
	// resource of the source environment and requires ConfirmAll.
	Scope      string `json:"scope,omitempty"`
	ConfirmAll bool   `json:"confirmAll,omitempty"`
	// Flows may be empty when Scope is all.
	Flows []FlowRef `json:"flows"`
	// Applications and FlowPolicies are included only when listed, by
	// name (or terraformer label).
	Applications []FlowRef `json:"applications,omitempty"`
	FlowPolicies []FlowRef `json:"flowPolicies,omitempty"`
	// Exclude lists resources to leave out of the export, in either scope.
	Exclude []ExcludeRef `json:"exclude,omitempty"`
	// DependencyChoices: "<type>.<label>" -> "update" | "keep" | "exclude".
	DependencyChoices map[string]string `json:"dependencyChoices,omitempty"`
	// EnvValues: env -> root variable -> JSON value. Never secrets.
	EnvValues map[string]map[string]json.RawMessage `json:"envValues,omitempty"`
	// SecretMappings: terraformer secret variable -> customer_secrets.yml name.
	SecretMappings map[string]string `json:"secretMappings,omitempty"`
	// ConnectorEnvLocal: connector label -> property names kept per env.
	ConnectorEnvLocal map[string][]string `json:"connectorEnvLocal,omitempty"`
	// InlineValues: DaVinci variable value variables that are the same in
	// every environment.
	InlineValues []string `json:"inlineValues,omitempty"`
	// BaseCommit enables three-way status (the commit last rolled out to
	// the source environment).
	BaseCommit string `json:"baseCommit,omitempty"`
	Mode       string `json:"mode"` // "mr" (default) or "branch"
	// BranchName must be <branch prefix><requestId> when set; the engine
	// checks the prefix against its configuration.
	BranchName    string `json:"branchName,omitempty"`
	CommitMessage string `json:"commitMessage,omitempty"`
	Title         string `json:"title,omitempty"`
	Description   string `json:"description,omitempty"`
	DryRun        bool   `json:"dryRun"`
}

// ValidationError is a malformed request. Field names the JSON path.
type ValidationError struct {
	Code  string `json:"code"`
	Field string `json:"field,omitempty"`
	Msg   string `json:"message"`
}

func (e *ValidationError) Error() string {
	if e.Field != "" {
		return "request: " + e.Field + ": " + e.Msg
	}
	return "request: " + e.Msg
}

// Validation codes.
const (
	CodeMalformed   = "malformed"
	CodeVersion     = "unsupported_version"
	CodeInvalid     = "invalid_field"
	CodeSecretValue = "secret_value"
)

var (
	RequestIDRE  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{2,63}$`)
	envRE        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,62}$`)
	identRE      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
	secretNameRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,127}$`)
	addressRE    = regexp.MustCompile(`^pingone_[a-z0-9_]+\.[A-Za-z0-9_\-]{1,512}$`)
	davinciIDRE  = regexp.MustCompile(`^[0-9a-fA-F]{32}$|^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	shaRE        = regexp.MustCompile(`^[0-9a-f]{7,64}$`)
	labelRE      = regexp.MustCompile(`^[A-Za-z0-9_\-]{1,512}$`)
)

// Parse decodes and validates a request strictly: unknown fields, trailing
// data, a wrong version and invalid values are errors.
func Parse(data []byte) (*Request, error) {
	if len(data) > MaxSize {
		return nil, &ValidationError{Code: CodeMalformed, Msg: fmt.Sprintf("request exceeds %d bytes", MaxSize)}
	}
	if !utf8.Valid(data) {
		return nil, &ValidationError{Code: CodeMalformed, Msg: "request is not UTF-8"}
	}
	// Check the version first so an old or future request gets a clear
	// error rather than an unknown-field error.
	var probe struct {
		RequestVersion *int `json:"requestVersion"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, &ValidationError{Code: CodeMalformed, Msg: "request is not a JSON object"}
	}
	if probe.RequestVersion == nil || *probe.RequestVersion != Version {
		return nil, &ValidationError{Code: CodeVersion, Field: "requestVersion", Msg: fmt.Sprintf("must be %d", Version)}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var r Request
	if err := dec.Decode(&r); err != nil {
		return nil, &ValidationError{Code: CodeMalformed, Msg: strings.TrimPrefix(err.Error(), "json: ")}
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, &ValidationError{Code: CodeMalformed, Msg: "trailing data after the request object"}
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return &r, nil
}

// DecodeBase64 decodes a request passed as a base64 CI variable.
func DecodeBase64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if len(s) > base64.StdEncoding.EncodedLen(MaxSize) {
		return nil, &ValidationError{Code: CodeMalformed, Msg: "request variable exceeds the size limit"}
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, &ValidationError{Code: CodeMalformed, Msg: "request variable is not base64"}
	}
	return b, nil
}

func invalid(field, msg string) error {
	return &ValidationError{Code: CodeInvalid, Field: field, Msg: msg}
}

func printable(s string, max int) bool {
	if len(s) > max || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 && r != '\n' && r != '\t' {
			return false
		}
	}
	return true
}

// Validate checks field values.
func (r *Request) Validate() error {
	if !RequestIDRE.MatchString(r.RequestID) {
		return invalid("requestId", "must match "+RequestIDRE.String())
	}
	if !printable(r.RequestedBy, 128) || strings.ContainsAny(r.RequestedBy, "\n\t") {
		return invalid("requestedBy", "must be at most 128 printable characters")
	}
	if !envRE.MatchString(r.Source) {
		return invalid("source", "must be an environment name")
	}
	switch r.Scope {
	case "":
		r.Scope = ScopeSelected
	case ScopeSelected, ScopeAll:
	default:
		return invalid("scope", "must be selected or all")
	}
	switch {
	case r.Whole() && !r.ConfirmAll:
		return invalid("confirmAll", "a whole-environment request (scope all) requires confirmAll: true")
	case !r.Whole() && r.ConfirmAll:
		return invalid("confirmAll", "confirmAll only applies to scope all")
	case !r.Whole() && len(r.Flows)+len(r.Applications)+len(r.FlowPolicies) == 0:
		return invalid("flows", "select at least one flow, application or flow policy")
	case !r.Whole() && (len(r.Flows) > 20 || len(r.Applications) > 20 || len(r.FlowPolicies) > 20):
		return invalid("flows", "at most 20 of each kind per request")
	}
	for i, f := range r.Flows {
		if err := checkRef(fmt.Sprintf("flows[%d]", i), f, true); err != nil {
			return err
		}
	}
	for i, f := range r.Applications {
		if err := checkRef(fmt.Sprintf("applications[%d]", i), f, false); err != nil {
			return err
		}
	}
	for i, f := range r.FlowPolicies {
		if err := checkRef(fmt.Sprintf("flowPolicies[%d]", i), f, false); err != nil {
			return err
		}
	}
	if err := r.checkExcludes(); err != nil {
		return err
	}
	for a, c := range r.DependencyChoices {
		if !addressRE.MatchString(a) {
			return invalid("dependencyChoices", a+" is not a resource address")
		}
		if c != "update" && c != "keep" && c != "exclude" {
			return invalid("dependencyChoices."+a, "must be update, keep or exclude")
		}
	}
	for env, vals := range r.EnvValues {
		if !envRE.MatchString(env) {
			return invalid("envValues", env+" is not an environment name")
		}
		for k, raw := range vals {
			field := "envValues." + env + "." + k
			if !identRE.MatchString(k) || k == "import_ids" {
				return invalid(field, "must be a root variable name (import_ids is set by adoption, not here)")
			}
			v, err := jsonValue(raw)
			if err != nil {
				return invalid(field, err.Error())
			}
			if kind := secretKind(k, v); kind != "" {
				return &ValidationError{Code: CodeSecretValue, Field: field, Msg: "looks like a secret (" + kind + "); secrets come from customer_secrets.yml"}
			}
		}
	}
	for k, v := range r.SecretMappings {
		if !identRE.MatchString(k) || !secretNameRE.MatchString(v) {
			return invalid("secretMappings."+k, "keys and values must be variable names")
		}
	}
	for label, props := range r.ConnectorEnvLocal {
		if !labelRE.MatchString(label) {
			return invalid("connectorEnvLocal", label+" is not a connector label")
		}
		for _, p := range props {
			if !labelRE.MatchString(p) {
				return invalid("connectorEnvLocal."+label, p+" is not a property name")
			}
		}
	}
	for _, v := range r.InlineValues {
		if !identRE.MatchString(v) {
			return invalid("inlineValues", v+" is not a variable name")
		}
	}
	if r.BaseCommit != "" && !shaRE.MatchString(r.BaseCommit) {
		return invalid("baseCommit", "must be a commit SHA")
	}
	if r.BranchName != "" && (!strings.HasSuffix(r.BranchName, r.RequestID) || !gitlab.ValidRequestBranchPrefix(strings.TrimSuffix(r.BranchName, r.RequestID))) {
		return invalid("branchName", "must be <branch prefix>"+r.RequestID)
	}
	switch r.Mode {
	case "":
		r.Mode = "mr"
	case "mr", "branch":
	default:
		return invalid("mode", "must be mr or branch")
	}
	if !printable(r.CommitMessage, 2000) {
		return invalid("commitMessage", "must be at most 2000 characters")
	}
	if !printable(r.Title, 255) || strings.ContainsAny(r.Title, "\n") {
		return invalid("title", "must be one line of at most 255 characters")
	}
	if !printable(r.Description, 64<<10) {
		return invalid("description", "must be at most 64 KiB")
	}
	return nil
}

// Whole reports a whole-environment request.
func (r *Request) Whole() bool { return r.Scope == ScopeAll }

var excludeIDRE = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z_\-]{0,63}(/[0-9A-Za-z][0-9A-Za-z_\-]{0,63})?$`)

// checkExcludes validates the exclude list and refuses to exclude what the
// request also includes. Only what can be told apart without the source
// environment is compared here (type with the same ID or name); the job
// compares the resolved addresses again.
func (r *Request) checkExcludes() error {
	if len(r.Exclude) > MaxExcludes {
		return invalid("exclude", fmt.Sprintf("at most %d entries", MaxExcludes))
	}
	includes := map[string][]FlowRef{ExcludeFlow: r.Flows, ExcludeApplication: r.Applications, ExcludeFlowPolicy: r.FlowPolicies}
	field := map[string]string{ExcludeFlow: "flows", ExcludeApplication: "applications", ExcludeFlowPolicy: "flowPolicies"}
	for i, e := range r.Exclude {
		f := fmt.Sprintf("exclude[%d]", i)
		if e.Address != "" {
			if e.Type != "" || e.ID != "" || e.Name != "" {
				return invalid(f, "an address entry takes no type, id or name")
			}
			if !addressRE.MatchString(e.Address) {
				return invalid(f+".address", "must be a terraform resource address")
			}
			continue
		}
		if _, ok := ExcludeTypes[e.Type]; !ok {
			return invalid(f+".type", "must be flow, application, flowPolicy, connection or variable (or set address)")
		}
		switch {
		case e.ID == "" && e.Name == "":
			return invalid(f, "set id, name or address")
		case e.ID != "" && !excludeIDRE.MatchString(e.ID):
			return invalid(f+".id", "is not an ID")
		case e.Name != "" && (!printable(e.Name, 200) || strings.TrimSpace(e.Name) == ""):
			return invalid(f+".name", "must be at most 200 printable characters")
		}
		for j, in := range includes[e.Type] {
			same := (e.ID != "" && strings.EqualFold(e.ID, in.ID)) ||
				(e.Name != "" && strings.EqualFold(strings.TrimSpace(e.Name), strings.TrimSpace(in.Name)))
			if same {
				return invalid(f, fmt.Sprintf("excludes %s, which %s[%d] includes; a resource cannot be both included and excluded", describe(e.ID, e.Name), field[e.Type], j))
			}
		}
	}
	return nil
}

func describe(id, name string) string {
	if name != "" {
		return strconv.Quote(name)
	}
	return id
}

func checkRef(field string, f FlowRef, allowID bool) error {
	switch {
	case f.ID == "" && f.Name == "":
		return invalid(field, "set id or name")
	case f.ID != "" && (!allowID || !davinciIDRE.MatchString(f.ID)):
		return invalid(field+".id", "must be a DaVinci ID (flows only)")
	case f.Name != "" && (!printable(f.Name, 200) || strings.TrimSpace(f.Name) == ""):
		return invalid(field+".name", "must be at most 200 printable characters")
	}
	return nil
}

func jsonValue(raw json.RawMessage) (cty.Value, error) {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return cty.NilVal, errors.New("must not be null")
	}
	t, err := ctyjson.ImpliedType(raw)
	if err != nil {
		return cty.NilVal, errors.New("is not a JSON value")
	}
	v, err := ctyjson.Unmarshal(raw, t)
	if err != nil {
		return cty.NilVal, errors.New("is not a JSON value")
	}
	return v, nil
}

func secretKind(name string, v cty.Value) string {
	switch {
	case v.IsNull() || !v.IsKnown():
		return ""
	case v.Type() == cty.String:
		s := v.AsString()
		if f := secretscan.ScanText("", []byte(s)); len(f) > 0 {
			return f[0].Kind
		}
		if secretscan.SuspectLiteral(name, s) {
			return secretscan.KindNamed
		}
	case v.CanIterateElements():
		for it := v.ElementIterator(); it.Next(); {
			_, e := it.Element()
			if k := secretKind(name, e); k != "" {
				return k
			}
		}
	}
	return ""
}

// Values converts EnvValues to cty values (already validated).
func (r *Request) Values() map[string]map[string]cty.Value {
	out := map[string]map[string]cty.Value{}
	for env, vals := range r.EnvValues {
		out[env] = map[string]cty.Value{}
		for k, raw := range vals {
			v, _ := jsonValue(raw)
			out[env][k] = v
		}
	}
	return out
}
