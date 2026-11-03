package tfvars

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"pingone-promote/internal/keys"
)

// Problem codes reported by Validate.
const (
	CodeSchemaVersion    = "schema_version"
	CodeEnvironmentID    = "environment_id"
	CodeKeyFormat        = "key_format"
	CodeEnum             = "enum"
	CodeJSON             = "json_invalid"
	CodeUTF8             = "utf8_invalid"
	CodeRefUnbalanced    = "reference_unbalanced"
	CodeRefMalformed     = "reference_malformed"
	CodeRefKind          = "reference_kind_not_allowed"
	CodeRefUnknown       = "reference_unknown"
	CodeRefPlainString   = "reference_in_plain_string"
	CodeTierOrder        = "tier_order"
	CodeVariableRules    = "variable_rules"
	CodeVariableIdentity = "variable_identity_duplicate"
	CodeVariableKey      = "variable_key"
	CodeGraph            = "graph_integrity"
	CodeImport           = "import_ids"
	CodeMaskedSecret     = "masked_secret"
	CodeSecretSuspect    = "secret_suspect"
)

// Problem is one validation failure. Message never contains attribute values.
type Problem struct {
	Code    string `json:"code"`
	Path    string `json:"path"`
	Message string `json:"message"`
}

// Report is the result of Validate. RequiredSecrets lists the pingone_secrets
// keys the pipeline must supply; RequiredParameters lists referenced
// parameters.
type Report struct {
	Problems           []Problem `json:"problems"`
	RequiredSecrets    []string  `json:"required_secrets"`
	RequiredParameters []string  `json:"required_parameters"`
}

// OK reports whether validation found no problems.
func (r Report) OK() bool { return len(r.Problems) == 0 }

// Allowed variable contexts and data types (provider 1.23 enums).
var (
	VariableContexts  = keys.VariableContexts
	VariableDataTypes = []string{"boolean", "number", "object", "secret", "string"}
	policyStatuses    = []string{"disabled", "enabled"}
	// MaxTier is the highest flow tier (tiers are 0..MaxTier).
	MaxTier = 3
)

type validator struct {
	doc      *Document
	problems []Problem
	secrets  map[string]bool
	params   map[string]bool
}

func (v *validator) add(code, path, format string, args ...any) {
	v.problems = append(v.problems, Problem{Code: code, Path: path, Message: fmt.Sprintf(format, args...)})
}

// Validate checks a document offline: schema version, key formats, enums,
// JSON fields, reference closure and kinds, tier ordering, variable rules
// and identity uniqueness, graph integrity, import IDs, and a no-secrets scan.
// Problems are sorted by path, then code.
func Validate(doc *Document) Report {
	v := &validator{doc: doc, secrets: map[string]bool{}, params: map[string]bool{}}
	if doc.SchemaVersion != SchemaVersion {
		v.add(CodeSchemaVersion, "tfvars_schema_version", "must be %d", SchemaVersion)
	}
	if !uuidRE.MatchString(doc.EnvironmentID) {
		v.add(CodeEnvironmentID, "pingone_environment_id", "must be a lowercase UUID")
	}
	for k := range doc.Parameters {
		if !IsValueKey(k) {
			v.add(CodeKeyFormat, "parameters["+strconv.Quote(k)+"]", "parameter keys must match %s", valueKeyRE)
		}
	}
	v.connections()
	v.variables()
	v.flows()
	v.applications()
	v.imports()
	v.scanSecrets()
	sort.SliceStable(v.problems, func(i, j int) bool {
		a, b := v.problems[i], v.problems[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		return a.Message < b.Message
	})
	return Report{Problems: v.problems, RequiredSecrets: sortedKeys(v.secrets), RequiredParameters: sortedKeys(v.params)}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func q(k string) string { return "[" + strconv.Quote(k) + "]" }

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (v *validator) logicalKey(path, k string) {
	if !IsLogicalKey(k) {
		v.add(CodeKeyFormat, path, "keys must match %s", logicalKeyRE)
	}
}

// refContext describes where a JSON field sits and which kinds it may use.
type refContext struct {
	allowed  []string
	flowTier int // tier of the owning flow, or -1
}

var (
	staticKinds = []string{kindEscape, KindEnv, KindParam}
	connKinds   = []string{kindEscape, KindEnv, KindParam, KindSecret}
	nodeKinds   = []string{kindEscape, KindEnv, KindParam, KindSecret, KindConnection, KindVariable, KindFlow}
)

func kindName(k string) string {
	if k == kindEscape {
		return "@@@@"
	}
	return k
}

// jsonField validates a JSON heredoc and its placeholders.
func (v *validator) jsonField(path string, s *string, rc refContext) {
	if s == nil {
		return
	}
	if _, err := CanonicalJSON(*s); err != nil {
		v.add(CodeJSON, path, "must be a JSON object with unique keys")
		return
	}
	tokens, balanced := refTokens(*s)
	if !balanced {
		v.add(CodeRefUnbalanced, path, "has an unpaired @@; write a literal @@ as @@@@")
		return
	}
	for _, tok := range tokens {
		ref, ok := parseRef(tok)
		if !ok {
			v.add(CodeRefMalformed, path, "has a malformed @@kind:key@@ placeholder")
			continue
		}
		if !contains(rc.allowed, ref.Kind) {
			v.add(CodeRefKind, path, "placeholder kind %q is not allowed here", kindName(ref.Kind))
			continue
		}
		switch ref.Kind {
		case KindSecret:
			v.secrets[ref.Key] = true
		case KindParam:
			v.params[ref.Key] = true
			if _, ok := v.doc.Parameters[ref.Key]; !ok {
				v.add(CodeRefUnknown, path, "parameter %q is not defined in parameters", ref.Key)
			}
		case KindConnection:
			if _, ok := v.doc.Connections[ref.Key]; !ok {
				v.add(CodeRefUnknown, path, "connection %q is not defined", ref.Key)
			}
		case KindVariable:
			dv, ok := v.doc.Variables[ref.Key]
			if !ok {
				v.add(CodeRefUnknown, path, "variable %q is not defined", ref.Key)
			} else if dv.Context == "flow" {
				v.add(CodeRefKind, path, "variable %q is flow-scoped and cannot be referenced by ID", ref.Key)
			}
		case KindFlow:
			target, ok := v.doc.Flows[ref.Key]
			if !ok {
				v.add(CodeRefUnknown, path, "flow %q is not defined", ref.Key)
			} else if target.Tier >= rc.flowTier {
				v.add(CodeTierOrder, path, "flow %q is in tier %d; a tier %d flow may reference only lower tiers", ref.Key, target.Tier, rc.flowTier)
			}
		}
	}
}

func (v *validator) connections() {
	for k, c := range v.doc.Connections {
		p := "davinci_connections" + q(k)
		v.logicalKey(p, k)
		v.jsonField(p+".properties", c.Properties, refContext{allowed: connKinds, flowTier: -1})
	}
}

type variableIdentity struct{ name, context, flow string }

func (v *validator) variables() {
	seen := map[variableIdentity]string{}
	varKeys := make([]string, 0, len(v.doc.Variables))
	for k := range v.doc.Variables {
		varKeys = append(varKeys, k)
	}
	sort.Strings(varKeys)
	for _, k := range varKeys {
		dv := v.doc.Variables[k]
		p := "davinci_variables" + q(k)
		v.logicalKey(p, k)
		if !contains(VariableContexts, dv.Context) {
			v.add(CodeEnum, p+".context", "must be one of %s", strings.Join(VariableContexts, ", "))
		}
		if !contains(VariableDataTypes, dv.DataType) {
			v.add(CodeEnum, p+".data_type", "must be one of %s", strings.Join(VariableDataTypes, ", "))
		}
		flowKey := ""
		if dv.Flow != nil {
			flowKey = *dv.Flow
		}
		if want, err := keys.VariableKey(dv.Name, dv.Context, flowKey); err == nil && want != k {
			v.add(CodeVariableKey, p, "key must be %q, derived from name, context and flow", want)
		}
		id := variableIdentity{dv.Name, dv.Context, flowKey}
		if other, dup := seen[id]; dup {
			v.add(CodeVariableIdentity, p, "has the same name, context and flow as davinci_variables[%q]", other)
		} else {
			seen[id] = k
		}
		switch {
		case dv.Context == "flow" && dv.Flow == nil:
			v.add(CodeVariableRules, p+".flow", "is required when context is \"flow\"")
		case dv.Context != "flow" && dv.Flow != nil:
			v.add(CodeVariableRules, p+".flow", "is allowed only when context is \"flow\"")
		case dv.Flow != nil:
			if _, ok := v.doc.Flows[*dv.Flow]; !ok {
				v.add(CodeRefUnknown, p+".flow", "flow %q is not defined", *dv.Flow)
			}
		}
		if dv.DataType == "secret" {
			if dv.Secret == nil {
				v.add(CodeVariableRules, p+".secret", "is required when data_type is \"secret\"")
			} else if !IsValueKey(*dv.Secret) {
				v.add(CodeKeyFormat, p+".secret", "secret keys must match %s", valueKeyRE)
			} else {
				v.secrets[*dv.Secret] = true
			}
			if dv.Value != nil {
				v.add(CodeVariableRules, p+".value", "must be absent when data_type is \"secret\"; the value comes from pingone_secrets")
			}
			continue
		}
		if dv.Secret != nil {
			v.add(CodeVariableRules, p+".secret", "is allowed only when data_type is \"secret\"")
		}
		if dv.Value == nil {
			continue
		}
		set := 0
		want := map[string]bool{
			"string":  dv.Value.String != nil,
			"boolean": dv.Value.Bool != nil,
			"number":  dv.Value.Float32 != nil,
			"object":  dv.Value.JSONObject != nil,
		}
		for _, isSet := range want {
			if isSet {
				set++
			}
		}
		if set > 1 {
			v.add(CodeVariableRules, p+".value", "must set at most one of string, bool, float32, json_object")
		} else if set == 1 && !want[dv.DataType] {
			v.add(CodeVariableRules, p+".value", "does not match data_type %q", dv.DataType)
		}
		v.jsonField(p+".value.json_object", dv.Value.JSONObject, refContext{allowed: staticKinds, flowTier: -1})
	}
}

func (v *validator) flows() {
	for k, f := range v.doc.Flows {
		p := "davinci_flows" + q(k)
		v.logicalKey(p, k)
		if f.Tier < 0 || f.Tier > MaxTier {
			v.add(CodeEnum, p+".tier", "must be between 0 and %d", MaxTier)
		}
		if f.OutputSchema != nil {
			v.jsonField(p+".output_schema.output.properties", f.OutputSchema.Output.Properties, refContext{allowed: staticKinds, flowTier: -1})
		}
		for nid, n := range f.Graph.Nodes {
			np := p + ".graph.nodes" + q(nid)
			v.logicalKey(np, nid)
			if n.NodeType == "" {
				v.add(CodeGraph, np+".node_type", "must not be empty")
			}
			if n.Connection != nil {
				if _, ok := v.doc.Connections[*n.Connection]; !ok {
					v.add(CodeRefUnknown, np+".connection", "connection %q is not defined", *n.Connection)
				}
			}
			v.jsonField(np+".properties", n.Properties, refContext{allowed: nodeKinds, flowTier: f.Tier})
		}
		for eid, e := range f.Graph.Edges {
			ep := p + ".graph.edges" + q(eid)
			v.logicalKey(ep, eid)
			if _, ok := f.Graph.Nodes[e.Source]; !ok {
				v.add(CodeGraph, ep+".source", "is not a node in this flow")
			}
			if _, ok := f.Graph.Nodes[e.Target]; !ok {
				v.add(CodeGraph, ep+".target", "is not a node in this flow")
			}
		}
	}
}

func (v *validator) applications() {
	for k, a := range v.doc.Applications {
		p := "davinci_applications" + q(k)
		v.logicalKey(p, k)
		for pk, pol := range a.FlowPolicies {
			pp := p + ".flow_policies" + q(pk)
			v.logicalKey(pp, pk)
			if pol.Status != nil && !contains(policyStatuses, *pol.Status) {
				v.add(CodeEnum, pp+".status", "must be one of %s", strings.Join(policyStatuses, ", "))
			}
			if len(pol.Distributions) == 0 {
				v.add(CodeRefUnknown, pp+".distributions", "must contain at least one distribution")
			}
			for i, d := range pol.Distributions {
				if _, ok := v.doc.Flows[d.Flow]; !ok {
					v.add(CodeRefUnknown, fmt.Sprintf("%s.distributions[%d].flow", pp, i), "flow %q is not defined", d.Flow)
				}
			}
		}
	}
}

func (v *validator) imports() {
	check := func(name string, ids map[string]string, exists func(string) bool, idRE *regexp.Regexp) {
		for k, id := range ids {
			p := "import_ids." + name + q(k)
			if !exists(k) {
				v.add(CodeImport, p, "has no matching %s entry", name)
			}
			if !idRE.MatchString(id) {
				v.add(CodeImport, p, "import ID must match %s", idRE)
			}
		}
	}
	d := v.doc
	check("davinci_connections", d.ImportIDs.Connections, func(k string) bool { _, ok := d.Connections[k]; return ok }, importIDRE)
	check("davinci_flows", d.ImportIDs.Flows, func(k string) bool { _, ok := d.Flows[k]; return ok }, importIDRE)
	check("davinci_variables", d.ImportIDs.Variables, func(k string) bool { _, ok := d.Variables[k]; return ok }, importIDRE)
	check("davinci_applications", d.ImportIDs.Applications, func(k string) bool { _, ok := d.Applications[k]; return ok }, importIDRE)
	check("davinci_flow_policies", d.ImportIDs.FlowPolicies, func(k string) bool {
		app, pol, ok := strings.Cut(k, "/")
		if !ok {
			return false
		}
		_, ok = d.Applications[app].FlowPolicies[pol]
		return ok
	}, policyIDRE)
}

// --- no-secrets scan ---

var (
	maskedRE = regexp.MustCompile(`\*{6,}`)
	pemRE    = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)
	jwtRE    = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.`)
	// A value that is wholly a DaVinci expression is a reference, not a secret.
	expressionRE = regexp.MustCompile(`^(\{\{[^{}]*\}\}|\$\{[^{}]*\})$`)
	nonAlnumRE   = regexp.MustCompile(`[^a-z0-9]`)
	secretSuffix = []string{"secret", "password", "passwd", "passphrase", "privatekey", "apikey", "token", "credential", "credentials", "sharedkey", "signingkey"}
)

func secretLookingName(name string) bool {
	n := nonAlnumRE.ReplaceAllString(strings.ToLower(name), "")
	for _, s := range secretSuffix {
		if strings.HasSuffix(n, s) {
			return true
		}
	}
	return false
}

func (v *validator) scanValue(path, s string) {
	if !utf8.ValidString(s) {
		v.add(CodeUTF8, path, "is not valid UTF-8")
		return
	}
	if maskedRE.MatchString(s) {
		v.add(CodeMaskedSecret, path, "contains a masked value (******); reference a secret with @@secret:<key>@@ instead")
	}
	if pemRE.MatchString(s) || jwtRE.MatchString(s) {
		v.add(CodeSecretSuspect, path, "looks like a private key or token; secrets must come from pingone_secrets")
	}
}

func (v *validator) suspectNamed(path, name, value string) {
	if value == "" || !secretLookingName(name) || strings.Contains(value, "@@secret:") || expressionRE.MatchString(strings.TrimSpace(value)) || maskedRE.MatchString(value) {
		return
	}
	v.add(CodeSecretSuspect, path, "has a secret-looking name with a literal value; use @@secret:<key>@@")
}

func (v *validator) scanSecrets() {
	for k, val := range v.doc.Parameters {
		p := "parameters" + q(k)
		v.suspectNamed(p, k, val)
	}
	for k, dv := range v.doc.Variables {
		if dv.Value != nil && dv.Value.String != nil {
			v.suspectNamed("davinci_variables"+q(k)+".value.string", dv.Name, *dv.Value.String)
		}
	}
	v.walk(reflect.ValueOf(v.doc).Elem(), "", false)
}

// walk visits every string in the document; JSON fields are scanned per value.
func (v *validator) walk(rv reflect.Value, path string, isJSON bool) {
	switch rv.Kind() {
	case reflect.Pointer:
		if !rv.IsNil() {
			v.walk(rv.Elem(), path, isJSON)
		}
	case reflect.String:
		s := rv.String()
		if isJSON {
			var tree any
			if err := json.Unmarshal([]byte(s), &tree); err == nil {
				v.walkJSON(path, "", tree)
				return
			}
		} else if looksLikeRefRE.MatchString(s) {
			v.add(CodeRefPlainString, path, "placeholders are resolved only inside JSON heredoc fields")
		}
		v.scanValue(path, s)
	case reflect.Slice:
		for i := 0; i < rv.Len(); i++ {
			v.walk(rv.Index(i), fmt.Sprintf("%s[%d]", path, i), isJSON)
		}
	case reflect.Map:
		for _, k := range rv.MapKeys() {
			v.walk(rv.MapIndex(k), path+q(k.String()), isJSON)
		}
	case reflect.Struct:
		for _, f := range fieldsOf(rv.Type()) {
			v.walk(rv.Field(f.index), attrPath(path, f.name), f.json)
		}
	}
}

func (v *validator) walkJSON(path, name string, node any) {
	switch x := node.(type) {
	case string:
		v.scanValue(path, x)
		v.suspectNamed(path, name, x)
	case []any:
		for i, e := range x {
			v.walkJSON(fmt.Sprintf("%s/%d", path, i), name, e)
		}
	case map[string]any:
		for k, e := range x {
			child := name
			// DaVinci properties nest the value: {"clientSecret": {"value": "..."}}.
			if k != "value" {
				child = k
			}
			v.walkJSON(path+"/"+k, child, e)
		}
	}
}
