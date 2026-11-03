// Package translate turns a DaVinci inventory into tfvars schema v1 entries.
//
// The output is environment-neutral wherever the rules allow:
//   - Logical keys come from internal/keys and never from PingOne IDs.
//     Variable keys always encode the context, and the flow for flow
//     variables.
//   - Embedded IDs become placeholders. Node connectionId becomes the
//     structured connection key; a connectionId inside properties becomes
//     @@connection:<key>@@; subFlowId.value.value becomes @@flow:<key>@@; the
//     environment ID becomes @@env:id@@. form, theme and themeId values become
//     @@param:<key>@@, because forms and themes are not managed in this phase.
//     A literal "@@" is escaped as "@@@@".
//   - Secrets are never emitted. Masked values ("******"), secure properties,
//     variables with data type "secret", and values with secret-looking names
//     (the heuristics moved from promotion.Redact) become @@secret:<key>@@ or
//     secret = "<key>". Heuristic matches need operator acknowledgement.
//   - Normalization drops computed fields, sets subflow and policy versions
//     to -1 (with a warning), clears input_schema on AUTHENTICATION flows,
//     and defaults settings.log_level to 4.
//   - Tiers are assigned top-down and are sticky: existing tiers are kept,
//     and conflicts are reported instead of silently moving flows.
package translate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"pingone-promote/internal/davinci"
	"pingone-promote/internal/keys"
	"pingone-promote/internal/tfvars"
)

// DefaultLogLevel is applied when a flow has no settings.logLevel; the
// provider otherwise computes one and shows a perpetual diff.
const DefaultLogLevel = 4

// Options carries operator decisions. Sites are stable, ID-free strings:
// "connection:<key>/<prop>", "variable:<key>" and "node:<flow>/<node>/<prop>".
type Options struct {
	// ExistingTiers keeps flow tiers sticky (flow key -> tier), usually taken
	// from the target environment's tfvars.
	ExistingTiers map[string]int
	// KeyOverrides maps a PingOne ID to an operator-chosen logical key,
	// for example to resolve two flows with the same name.
	KeyOverrides map[string]string
	// SecretNames maps a secret site to an operator-chosen secret key,
	// usually an existing pipeline secret.
	SecretNames map[string]string
	// NotSecret marks heuristic sites the operator confirmed are not secret;
	// their literal values are kept.
	NotSecret map[string]bool
	// Acknowledged marks heuristic sites the operator reviewed and accepted as
	// secret; their warnings no longer need acknowledgement.
	Acknowledged map[string]bool
}

// Warning is a translation note. Warnings with NeedsAck block submission
// until acknowledged. Messages never contain values.
type Warning struct {
	Code     string `json:"code"`
	Path     string `json:"path"`
	Site     string `json:"site,omitempty"`
	Message  string `json:"message"`
	NeedsAck bool   `json:"needs_ack,omitempty"`
}

// Secret is one secret site and the key it maps to.
type Secret struct {
	Key    string `json:"key"`
	Site   string `json:"site"`
	Reason string `json:"reason"` // masked, secure, data_type, heuristic
}

// Param is a parameter the translation introduced (form/theme fallback).
type Param struct {
	Key  string `json:"key"`
	Path string `json:"path"`
}

// TierConflict is a subflow edge whose sticky tiers violate the ordering.
// Fixing it moves a flow to another tier resource, which needs a moved block.
type TierConflict struct {
	Parent     string `json:"parent"`
	ParentTier int    `json:"parent_tier"`
	Child      string `json:"child"`
	ChildTier  int    `json:"child_tier"`
}

// Result is a translated inventory. Doc.ImportIDs holds the source IDs, so
// the same document can adopt the source environment; promotion into
// another environment keeps that environment's own import_ids.
type Result struct {
	Doc           *tfvars.Document
	Secrets       []Secret
	Params        []Param
	Warnings      []Warning
	TierConflicts []TierConflict
	// SubflowEdges maps a flow key to the flow keys it references.
	SubflowEdges map[string][]string
}

// Error lists problems that need an operator decision, such as colliding
// keys. Problems name resources, never values.
type Error struct{ Problems []string }

func (e *Error) Error() string {
	return fmt.Sprintf("translation needs %d operator decision(s): %s", len(e.Problems), strings.Join(e.Problems, "; "))
}

type translator struct {
	inv  *davinci.Inventory
	opts Options
	res  *Result
	errs []string

	connKeys   map[string]string // id -> key
	connNames  map[string]string // key -> name
	flowKeys   map[string]string
	flowNames  map[string]string
	globalVars map[string]string // id -> key (non-flow variables)
	secretBy   map[string]string // secret key -> site
	paramBy    map[string]string // param key -> path
	edges      map[string]map[string]bool
}

// Translate converts inv. It returns an *Error when operator decisions are
// needed; the partial Result is still returned for display.
func Translate(inv *davinci.Inventory, opts Options) (*Result, error) {
	t := &translator{inv: inv, opts: opts,
		res:      &Result{Doc: emptyDoc(inv.EnvironmentID), SubflowEdges: map[string][]string{}},
		connKeys: map[string]string{}, connNames: map[string]string{}, flowKeys: map[string]string{}, flowNames: map[string]string{},
		globalVars: map[string]string{}, secretBy: map[string]string{}, paramBy: map[string]string{}, edges: map[string]map[string]bool{}}
	for _, f := range inv.Partial {
		t.warn("partial_inventory", string(f.Kind), "", fmt.Sprintf("%s was read only in part (%s); missing references are reported as unresolved", f.Kind, f.Reason), false)
	}
	t.assignKeys()
	t.connections()
	t.variables()
	t.flows()
	t.applications()
	t.assignTiers()
	sort.Slice(t.res.Secrets, func(i, j int) bool { return t.res.Secrets[i].Site < t.res.Secrets[j].Site })
	sort.Slice(t.res.Params, func(i, j int) bool { return t.res.Params[i].Key < t.res.Params[j].Key })
	sort.SliceStable(t.res.Warnings, func(i, j int) bool {
		a, b := t.res.Warnings[i], t.res.Warnings[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Code < b.Code
	})
	if len(t.errs) > 0 {
		sort.Strings(t.errs)
		return t.res, &Error{Problems: t.errs}
	}
	return t.res, nil
}

func emptyDoc(env string) *tfvars.Document {
	return &tfvars.Document{SchemaVersion: tfvars.SchemaVersion, EnvironmentID: env,
		Parameters: map[string]string{}, Connections: map[string]tfvars.Connection{}, Variables: map[string]tfvars.Variable{},
		Flows: map[string]tfvars.Flow{}, Applications: map[string]tfvars.Application{},
		ImportIDs: tfvars.ImportIDs{Connections: map[string]string{}, Flows: map[string]string{}, Variables: map[string]string{},
			Applications: map[string]string{}, FlowPolicies: map[string]string{}}}
}

func (t *translator) warn(code, path, site, msg string, ack bool) {
	t.res.Warnings = append(t.res.Warnings, Warning{Code: code, Path: path, Site: site, Message: msg, NeedsAck: ack})
}

func (t *translator) fail(format string, args ...any) {
	t.errs = append(t.errs, fmt.Sprintf(format, args...))
}

func q(k string) string { return "[" + strconv.Quote(k) + "]" }

// keyFor returns the logical key for a named resource, honouring overrides.
func (t *translator) keyFor(kind, id, name string, taken map[string]string) string {
	k, ok := t.opts.KeyOverrides[id]
	if !ok {
		var err error
		if k, err = keys.Encode(name); err != nil {
			t.fail("%s %q: %v; set a key override", kind, name, err)
			return ""
		}
	} else if !tfvars.IsLogicalKey(k) {
		t.fail("%s %q: key override is not a valid logical key", kind, name)
		return ""
	}
	if other, dup := taken[k]; dup {
		t.fail("%s %q and %q both map to key %q; rename one or set a key override", kind, other, name, k)
		return ""
	}
	taken[k] = name
	return k
}

func (t *translator) assignKeys() {
	taken := map[string]string{}
	for _, c := range t.inv.Connections {
		if k := t.keyFor("connection", c.ID, c.Name, taken); k != "" {
			t.connKeys[c.ID], t.connNames[k] = k, c.Name
		}
	}
	taken = map[string]string{}
	for _, f := range t.inv.Flows {
		if k := t.keyFor("flow", f.ID, f.Name, taken); k != "" {
			t.flowKeys[f.ID], t.flowNames[k] = k, f.Name
		}
	}
}

// --- secrets ---

// secretKey resolves the key for a site, enforcing uniqueness.
func (t *translator) secretKey(site, suggested, reason, path string) string {
	key := suggested
	if k, ok := t.opts.SecretNames[site]; ok {
		key = k
	}
	if !tfvars.IsSecretKey(key) {
		t.fail("secret at %s: key %q is not a valid secret name (%s); choose one", site, key, tfvars.SecretKeyPattern)
		return ""
	}
	if other, dup := t.secretBy[key]; dup && other != site {
		t.fail("secret sites %s and %s both map to %q; choose an explicit name for one", other, site, key)
		return ""
	}
	t.secretBy[key] = site
	t.res.Secrets = append(t.res.Secrets, Secret{Key: key, Site: site, Reason: reason})
	if reason == "heuristic" {
		t.warn("secret_heuristic", path, site, "value has a secret-looking name and was replaced by a secret reference; acknowledge, or mark it as not secret", !t.opts.Acknowledged[site])
	}
	return key
}

func isTrue(v any) bool {
	b, ok := v.(bool)
	return ok && b
}

// --- JSON rewriting ---

type jsonScope int

const (
	scopeStatic jsonScope = iota // env and params only
	scopeConnection
	scopeNode
)

type rewriteCtx struct {
	scope    jsonScope
	path     string
	flowKey  string
	flowName string
	nodeID   string
}

func decodeJSON(raw []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

func encodeJSON(v any) (string, error) {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		return "", err
	}
	return tfvars.CanonicalJSON(b.String())
}

// literal escapes "@@" and replaces the environment ID.
func (t *translator) literal(s string) string {
	s = strings.ReplaceAll(s, "@@", "@@@@")
	if env := t.inv.EnvironmentID; env != "" {
		s = strings.ReplaceAll(s, env, "@@env:id@@")
	}
	return s
}

// walkStrings rewrites every string in v with literal(), plus the
// connectionId rule, and warns about other embedded IDs.
func (t *translator) walkStrings(v any, c rewriteCtx, key string) any {
	switch x := v.(type) {
	case string:
		if key == "connectionId" {
			if k, ok := t.connKeys[x]; ok && c.scope == scopeNode {
				return "@@connection:" + k + "@@"
			}
		}
		if x == davinci.MaskedValue {
			// Top-level masked values are classified as secrets before this
			// walk; a nested one has no secret site, so it is dropped.
			t.warn("secret_dropped", c.path, "", "nested masked value dropped; set it in the target environment", true)
			return ""
		}
		if _, isFlow := t.flowKeys[x]; isFlow {
			t.warn("embedded_id", c.path, "", "contains a flow ID outside subFlowId; it is copied as is and will not match other environments", false)
		} else if _, isConn := t.connKeys[x]; isConn {
			t.warn("embedded_id", c.path, "", "contains a connection ID outside connectionId; it is copied as is and will not match other environments", false)
		}
		return t.literal(x)
	case []any:
		for i := range x {
			x[i] = t.walkStrings(x[i], c, "")
		}
		return x
	case map[string]any:
		for k, e := range x {
			x[k] = t.walkStrings(e, c, k)
		}
		return x
	}
	return v
}

var paramUnsafe = regexp.MustCompile(`[^a-z0-9_]`)

func paramKey(parts ...string) string {
	return paramUnsafe.ReplaceAllString(strings.ToLower(strings.Join(parts, "_")), "_")
}

// valueSlot returns the string under prop (either the string itself or its
// "value" field) and a setter for it.
func valueSlot(props map[string]any, prop string) (string, func(string), bool) {
	switch x := props[prop].(type) {
	case string:
		return x, func(s string) { props[prop] = s }, true
	case map[string]any:
		if s, ok := x["value"].(string); ok {
			return s, func(n string) { x["value"] = n }, true
		}
	}
	return "", nil, false
}

// nodeProperties applies the node rules and returns canonical JSON.
func (t *translator) nodeProperties(raw json.RawMessage, c rewriteCtx) *string {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return nil
	}
	v, err := decodeJSON(raw)
	props, ok := v.(map[string]any)
	if err != nil || !ok {
		t.warn("properties_invalid", c.path, "", "node properties are not a JSON object; dropped", false)
		return nil
	}
	names := sortedKeys(props)
	// Secrets first, on raw values.
	for _, prop := range names {
		s, set, ok := valueSlot(props, prop)
		if !ok || s == "" {
			continue
		}
		site := "node:" + c.flowKey + "/" + c.nodeID + "/" + prop
		reason := ""
		switch {
		case s == davinci.MaskedValue:
			reason = "masked"
		case secretLookingName(prop) && !templateOnly.MatchString(s) && !t.opts.NotSecret[site]:
			reason = "heuristic"
		}
		if reason == "" {
			continue
		}
		if key := t.secretKey(site, NodeSecretName(c.flowName, c.nodeID, prop), reason, c.path+"/"+prop); key != "" {
			set("@@secret:" + key + "@@")
			props[prop] = markPlaceholder(props[prop])
		} else {
			set("")
		}
	}
	for _, prop := range names {
		pc := c
		pc.path = c.path + "/" + prop
		switch prop {
		case "subFlowId":
			if m, ok := props[prop].(map[string]any); ok {
				if inner, ok := m["value"].(map[string]any); ok {
					if id, ok := inner["value"].(string); ok {
						if k, known := t.flowKeys[id]; known {
							inner["value"] = "@@flow:" + k + "@@"
							t.edge(c.flowKey, k)
						} else {
							t.warn("unresolved_flow", pc.path, "", "subflow is not in the inventory; promote will treat it as unmanaged", false)
							inner["value"] = "@@flow:" + unresolvedKey(id) + "@@"
						}
						if label, ok := inner["label"].(string); ok {
							inner["label"] = t.literal(label)
						}
						continue
					}
				}
			}
		case "subFlowVersionId":
			if m, ok := props[prop].(map[string]any); ok {
				if n, ok := m["value"].(json.Number); !ok || n.String() != "-1" {
					t.warn("version_latest", pc.path, "", "subflow version pinned to a source version; set to -1 (latest deployed)", false)
				}
				m["value"] = json.Number("-1")
				continue
			}
		case "form", "theme", "themeId":
			if s, set, ok := valueSlot(props, prop); ok && s != "" && !strings.HasPrefix(s, "@@") {
				key := paramKey(prop, c.flowKey, c.nodeID)
				if !tfvars.IsValueKey(key) {
					t.fail("parameter for %s is longer than 128 characters; shorten the flow key", pc.path)
					continue
				}
				if other, dup := t.paramBy[key]; dup && other != pc.path {
					t.fail("parameter key %q is used by %s and %s; set key overrides", key, other, pc.path)
					continue
				}
				t.paramBy[key] = pc.path
				// The source document keeps the source value so it can adopt
				// the source environment; promotion never copies parameters.
				t.res.Doc.Parameters[key] = s
				set("@@param:" + key + "@@")
				props[prop] = markPlaceholder(props[prop])
				t.res.Params = append(t.res.Params, Param{Key: key, Path: pc.path})
				t.warn("param_fallback", pc.path, "", "PingOne "+prop+" IDs differ per environment; set parameter "+key+" in each environment", false)
				continue
			}
		}
		if _, isPlaceholder := props[prop].(placeholderValue); isPlaceholder {
			continue
		}
		props[prop] = t.walkStrings(props[prop], pc, prop)
	}
	unmark(props)
	out, err := encodeJSON(props)
	if err != nil {
		t.warn("properties_invalid", c.path, "", "node properties could not be encoded; dropped", false)
		return nil
	}
	return &out
}

// placeholderValue wraps a value already rewritten to a placeholder so the
// generic walk does not escape its @@ again.
type placeholderValue struct{ v any }

func markPlaceholder(v any) any { return placeholderValue{v} }

func unmark(m map[string]any) {
	for k, v := range m {
		if p, ok := v.(placeholderValue); ok {
			m[k] = p.v
		}
	}
}

func unresolvedKey(id string) string {
	k, err := keys.Encode("unresolved " + id)
	if err != nil {
		return "unresolved"
	}
	return k
}

func (t *translator) edge(parent, child string) {
	if t.edges[parent] == nil {
		t.edges[parent] = map[string]bool{}
	}
	t.edges[parent][child] = true
}

// staticJSON rewrites env IDs and @@ in a JSON value (no other references).
func (t *translator) staticJSON(raw json.RawMessage, path string) *string {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return nil
	}
	v, err := decodeJSON(raw)
	if _, ok := v.(map[string]any); err != nil || !ok {
		t.warn("json_invalid", path, "", "value is not a JSON object; dropped", false)
		return nil
	}
	v = t.walkStrings(v, rewriteCtx{scope: scopeStatic, path: path}, "")
	out, err := encodeJSON(v)
	if err != nil {
		return nil
	}
	return &out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// --- connections ---

func (t *translator) connections() {
	for _, c := range t.inv.Connections {
		key, ok := t.connKeys[c.ID]
		if !ok {
			continue
		}
		path := "davinci_connections" + q(key)
		conn := tfvars.Connection{Name: c.Name, Connector: c.Connector.ID}
		if props := t.connectionProperties(c, key, path); props != nil {
			conn.Properties = props
		}
		t.res.Doc.Connections[key] = conn
		t.res.Doc.ImportIDs.Connections[key] = c.ID
	}
}

func (t *translator) connectionProperties(c davinci.ConnectorInstance, key, path string) *string {
	if len(bytes.TrimSpace(c.Properties)) == 0 || string(bytes.TrimSpace(c.Properties)) == "null" {
		return nil
	}
	v, err := decodeJSON(c.Properties)
	props, ok := v.(map[string]any)
	if err != nil || !ok {
		t.warn("properties_invalid", path, "", "connector properties are not a JSON object; dropped", false)
		return nil
	}
	if len(props) == 0 {
		return nil
	}
	for _, prop := range sortedKeys(props) {
		pp := path + ".properties/" + prop
		site := "connection:" + key + "/" + prop
		s, set, isString := valueSlot(props, prop)
		meta, _ := props[prop].(map[string]any)
		reason := ""
		switch {
		case isString && s == davinci.MaskedValue:
			reason = "masked"
		case meta != nil && (isTrue(meta["secure"]) || isTrue(meta["sensitive"]) || isTrue(meta["isSensitive"]) || strings.EqualFold(fmt.Sprint(meta["type"]), "secret")):
			reason = "secure"
		case isString && s != "" && secretLookingName(prop) && !templateOnly.MatchString(s) && !t.opts.NotSecret[site]:
			reason = "heuristic"
		}
		if reason != "" {
			if !isString {
				// A secure value that is not a string cannot be a placeholder;
				// drop it rather than risk emitting it.
				if meta != nil {
					delete(meta, "value")
				}
				t.warn("secret_dropped", pp, site, "secure non-string value dropped; set it in the target environment", true)
				continue
			}
			if k := t.secretKey(site, ConnectionSecretName(c.Name, prop), reason, pp); k != "" {
				set("@@secret:" + k + "@@")
			} else {
				set("")
			}
			props[prop] = markPlaceholder(props[prop])
			continue
		}
		props[prop] = t.walkStrings(props[prop], rewriteCtx{scope: scopeConnection, path: pp}, prop)
	}
	unmark(props)
	out, err := encodeJSON(props)
	if err != nil {
		return nil
	}
	return &out
}

// --- variables ---

func (t *translator) variables() {
	taken := map[string]string{}
	type pending struct {
		v   davinci.Variable
		key string
	}
	for _, v := range t.inv.Variables {
		ctx := "company"
		if v.Context != nil && *v.Context != "" {
			ctx = *v.Context
		} else {
			t.warn("variable_context_default", "variable "+strconv.Quote(v.Name), "", "variable has no context; treated as company", false)
		}
		flowKey := ""
		if ctx == "flow" {
			if v.Flow == nil || t.flowKeys[v.Flow.ID] == "" {
				t.warn("unresolved_flow", "variable "+strconv.Quote(v.Name), "", "flow-scoped variable's flow is not in the inventory; variable skipped", false)
				continue
			}
			flowKey = t.flowKeys[v.Flow.ID]
		}
		key, err := keys.VariableKey(v.Name, ctx, flowKey)
		if override, ok := t.opts.KeyOverrides[v.ID]; ok {
			key, err = override, nil
		}
		if err != nil {
			t.fail("variable %q (%s): %v", v.Name, ctx, err)
			continue
		}
		if other, dup := taken[key]; dup {
			t.fail("variables %q and %q both map to key %q; set a key override", other, v.Name, key)
			continue
		}
		taken[key] = v.Name
		path := "davinci_variables" + q(key)
		out := tfvars.Variable{Name: v.Name, Context: ctx, DataType: v.DataType, Mutable: v.Mutable != nil && *v.Mutable,
			DisplayName: v.DisplayName, Min: v.Min, Max: v.Max}
		if flowKey != "" {
			fk := flowKey
			out.Flow = &fk
		} else {
			t.globalVars[v.ID] = key
		}
		if v.DataType == "secret" {
			site := "variable:" + key
			if k := t.secretKey(site, VariableSecretName(v.Name), "data_type", path); k != "" {
				out.Secret = &k
			}
		} else {
			out.Value = t.variableValue(v, path)
		}
		t.res.Doc.Variables[key] = out
		t.res.Doc.ImportIDs.Variables[key] = v.ID
	}
}

func (t *translator) variableValue(v davinci.Variable, path string) *tfvars.VariableValue {
	raw := bytes.TrimSpace(v.Value)
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	val, err := decodeJSON(raw)
	if err != nil {
		t.warn("value_invalid", path+".value", "", "value did not decode; dropped", false)
		return nil
	}
	switch x := val.(type) {
	case string:
		if x == davinci.MaskedValue {
			t.warn("secret_dropped", path+".value", "variable:"+path, "masked value on a non-secret variable dropped", true)
			return nil
		}
		s := x
		return &tfvars.VariableValue{String: &s}
	case bool:
		b := x
		return &tfvars.VariableValue{Bool: &b}
	case json.Number:
		f, err := x.Float64()
		if err != nil || math.IsInf(f, 0) {
			return nil
		}
		return &tfvars.VariableValue{Float32: &f}
	case map[string]any:
		return &tfvars.VariableValue{JSONObject: t.staticJSON(raw, path+".value.json_object")}
	}
	t.warn("value_invalid", path+".value", "", "unsupported value type; dropped", false)
	return nil
}

// --- flows ---

func (t *translator) flows() {
	for _, f := range t.inv.Flows {
		key, ok := t.flowKeys[f.ID]
		if !ok {
			continue
		}
		path := "davinci_flows" + q(key)
		out := tfvars.Flow{Name: f.Name, Description: f.Description, Color: f.Color, Enabled: f.Enabled}
		out.Settings = t.settings(f.Settings, path+".settings")
		if f.Trigger != nil && f.Trigger.Type != nil {
			tr := &tfvars.FlowTrigger{Type: *f.Trigger.Type, Subtype: f.Trigger.Subtype}
			if c := f.Trigger.Configuration; c != nil {
				tr.Configuration = &tfvars.TriggerConfigFixed{
					MFA: tfvars.TimedToggle{Enabled: c.MFA.Enabled, Time: c.MFA.Time, TimeFormat: c.MFA.TimeFormat},
					PWD: tfvars.TimedToggle{Enabled: c.PWD.Enabled, Time: c.PWD.Time, TimeFormat: c.PWD.TimeFormat}}
			}
			out.Trigger = tr
		}
		if out.Trigger != nil && out.Trigger.Type == "AUTHENTICATION" {
			if len(f.InputSchema) > 0 {
				t.warn("input_schema_cleared", path+".input_schema", "", "AUTHENTICATION flows take no input schema; cleared", false)
			}
		} else {
			for _, in := range f.InputSchema {
				in := in
				out.InputSchema = append(out.InputSchema, tfvars.InputField{Description: in.Description, IsExpanded: in.IsExpanded,
					PreferredControlType: nonEmpty(in.PreferredControlType), PreferredDataType: nonEmpty(in.PreferredDataType),
					PropertyName: nonEmpty(in.PropertyName), Required: in.Required})
			}
		}
		if os := f.OutputSchema; os != nil {
			out.OutputSchema = &tfvars.OutputSchema{Output: tfvars.OutputSpec{Type: os.Output.Type, AdditionalProperties: os.Output.AdditionalProperties,
				Properties: t.staticJSON(os.Output.Properties, path+".output_schema.output.properties")}}
		}
		out.Graph = t.graph(f, key, path+".graph")
		t.res.Doc.Flows[key] = out
		t.res.Doc.ImportIDs.Flows[key] = f.ID
	}
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (t *translator) graph(f davinci.Flow, key, path string) tfvars.Graph {
	g := tfvars.Graph{Nodes: map[string]tfvars.Node{}, Edges: map[string]tfvars.Edge{}}
	gd := f.GraphData
	if gd == nil {
		t.warn("graph_missing", path, "", "flow has no graph", false)
		return g
	}
	g.BoxSelectionEnabled, g.PanningEnabled, g.UserPanningEnabled, g.UserZoomingEnabled, g.ZoomingEnabled =
		gd.BoxSelectionEnabled, gd.PanningEnabled, gd.UserPanningEnabled, gd.UserZoomingEnabled, gd.ZoomingEnabled
	g.Zoom, g.MaxZoom, g.MinZoom = gd.Zoom, gd.MaxZoom, gd.MinZoom
	if gd.Pan != nil {
		g.Pan = &tfvars.Position{X: gd.Pan.X, Y: gd.Pan.Y}
	}
	g.Renderer = compactNonEmpty(gd.Renderer)
	g.Data = compactNonEmpty(gd.Data)
	for _, n := range gd.Elements.Nodes {
		id := n.Data.ID
		np := path + ".nodes" + q(id)
		if !tfvars.IsLogicalKey(id) {
			t.fail("flow %q has a node ID that is not a valid key", f.Name)
			continue
		}
		node := tfvars.Node{NodeType: n.Data.NodeType, ConnectorID: n.Data.ConnectorID, CapabilityName: n.Data.CapabilityName,
			CapabilityClass: n.Data.CapabilityClass, Label: n.Data.Label, Status: n.Data.Status, Type: n.Data.Type,
			Classes: n.Classes, Group: n.Group, Grabbable: n.Grabbable, Locked: n.Locked, Pannable: n.Pannable,
			Removed: n.Removed, Selectable: n.Selectable, Selected: n.Selected}
		if n.Data.Label != nil {
			l := t.literalPlain(*n.Data.Label)
			node.Label = &l
		}
		if n.Data.ConnectionID != nil && *n.Data.ConnectionID != "" {
			if ck, ok := t.connKeys[*n.Data.ConnectionID]; ok {
				node.Connection = &ck
			} else {
				t.warn("unresolved_connection", np+".connection", "", "node's connection is not in the inventory; promote will treat it as unmanaged", false)
			}
		}
		for _, o := range n.Data.Outcomes {
			node.Outcomes = append(node.Outcomes, tfvars.Outcome{ID: o.ID, Label: o.Label, Result: o.Result})
		}
		if n.Position != nil {
			node.Position = &tfvars.Position{X: n.Position.X, Y: n.Position.Y}
		}
		node.Properties = t.nodeProperties(n.Data.Properties, rewriteCtx{scope: scopeNode, path: np + ".properties", flowKey: key, flowName: f.Name, nodeID: id})
		g.Nodes[id] = node
	}
	for _, e := range gd.Elements.Edges {
		if !tfvars.IsLogicalKey(e.Data.ID) {
			t.fail("flow %q has an edge ID that is not a valid key", f.Name)
			continue
		}
		edge := tfvars.Edge{Source: e.Data.Source, Target: e.Data.Target, MultiValueSourceID: e.Data.MultiValueSourceID,
			Classes: e.Classes, Group: e.Group, Grabbable: e.Grabbable, Locked: e.Locked, Pannable: e.Pannable,
			Removed: e.Removed, Selectable: e.Selectable, Selected: e.Selected}
		if e.Position != nil {
			edge.Position = &tfvars.Position{X: e.Position.X, Y: e.Position.Y}
		}
		g.Edges[e.Data.ID] = edge
	}
	return g
}

// literalPlain handles plain (non-JSON) strings: placeholders are not
// resolved there, so only the environment ID is reported, not replaced.
func (t *translator) literalPlain(s string) string {
	if env := t.inv.EnvironmentID; env != "" && strings.Contains(s, env) {
		t.warn("embedded_env_id", "", "", "a plain string contains the source environment ID; it is copied as is", false)
	}
	return s
}

func compactNonEmpty(raw json.RawMessage) *string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "{}" {
		return nil
	}
	var b bytes.Buffer
	if json.Compact(&b, raw) != nil {
		return nil
	}
	s := b.String()
	return &s
}

// settings maps API camelCase settings onto the schema.
func (t *translator) settings(in map[string]json.RawMessage, path string) *tfvars.FlowSettings {
	s := &tfvars.FlowSettings{}
	str := func(k string) *string {
		var v string
		if raw, ok := in[k]; ok && json.Unmarshal(raw, &v) == nil {
			return &v
		}
		return nil
	}
	boolean := func(k string) *bool {
		raw, ok := in[k]
		if !ok {
			return nil
		}
		var b bool
		if json.Unmarshal(raw, &b) == nil {
			return &b
		}
		var str string
		if json.Unmarshal(raw, &str) == nil && (str == "true" || str == "false") {
			b = str == "true"
			return &b
		}
		t.warn("setting_invalid", path+"."+k, "", "not a boolean; dropped", false)
		return nil
	}
	num := func(k string) *float64 {
		var f float64
		if raw, ok := in[k]; ok && json.Unmarshal(raw, &f) == nil {
			return &f
		}
		return nil
	}
	strs := func(k string) []string {
		var v []string
		if raw, ok := in[k]; ok && json.Unmarshal(raw, &v) == nil && len(v) > 0 {
			return v
		}
		return nil
	}
	known := map[string]bool{}
	use := func(k string) string { known[k] = true; return k }
	s.CSP, s.CSS, s.CSSLinks = str(use("csp")), str(use("css")), strs(use("cssLinks"))
	s.CustomErrorScreenBrandLogoURL, s.CustomErrorShowFooter = str(use("customErrorScreenBrandLogoUrl")), boolean(use("customErrorShowFooter"))
	s.CustomFaviconLink, s.CustomLogoURLSelection = str(use("customFaviconLink")), num(use("customLogoURLSelection"))
	s.CustomTimeoutErrorScreenCSS, s.CustomTimeoutErrorScreenHTML = str(use("customTimeoutErrorScreenCSS")), str(use("customTimeoutErrorScreenHTML"))
	s.CustomTimeoutErrorScreenMessage, s.CustomTitle = str(use("customTimeoutErrorScreenMessage")), str(use("customTitle"))
	s.DefaultErrorScreenBrandLogo = boolean(use("defaultErrorScreenBrandLogo"))
	s.FlowHTTPTimeoutInSeconds, s.FlowTimeoutInSeconds = num(use("flowHttpTimeoutInSeconds")), num(use("flowTimeoutInSeconds"))
	s.IntermediateLoadingScreenCSS, s.IntermediateLoadingScreenHTML = str(use("intermediateLoadingScreenCSS")), str(use("intermediateLoadingScreenHTML"))
	s.JSCustomFlowPlayer = str(use("jsCustomFlowPlayer"))
	if raw, ok := in[use("jsLinks")]; ok {
		var links []map[string]any
		if json.Unmarshal(raw, &links) == nil {
			for _, l := range links {
				s.JSLinks = append(s.JSLinks, tfvars.JSLink{Crossorigin: fmt.Sprint(l["crossorigin"]), Defer: fmt.Sprint(l["defer"]) == "true",
					Integrity: fmt.Sprint(l["integrity"]), Label: fmt.Sprint(l["label"]), Referrerpolicy: fmt.Sprint(l["referrerpolicy"]),
					Type: fmt.Sprint(l["type"]), Value: fmt.Sprint(l["value"])})
			}
		}
	}
	if f := num(use("logLevel")); f != nil && *f == math.Trunc(*f) {
		l := int(*f)
		s.LogLevel = &l
	} else {
		l := DefaultLogLevel
		s.LogLevel = &l
	}
	s.PreviewFormRenderingUpdates, s.RequireAuthenticationToInitiate = boolean(use("previewFormRenderingUpdates")), boolean(use("requireAuthenticationToInitiate"))
	s.ScrubSensitiveInfo, s.SensitiveInfoFields = boolean(use("scrubSensitiveInfo")), strs(use("sensitiveInfoFields"))
	s.UseCSP, s.UseCustomCSS, s.UseCustomFlowPlayer = boolean(use("useCSP")), boolean(use("useCustomCSS")), boolean(use("useCustomFlowPlayer"))
	s.UseCustomScript, s.UseCustomTimeoutErrorScreen = boolean(use("useCustomScript")), boolean(use("useCustomTimeoutErrorScreen"))
	s.UseIntermediateLoadingScreen, s.ValidateOnSave = boolean(use("useIntermediateLoadingScreen")), boolean(use("validateOnSave"))
	for _, k := range sortedKeys(in) {
		if !known[k] {
			t.warn("setting_unsupported", path+"."+k, "", "setting is not in the provider schema; dropped", false)
		}
	}
	return s
}

// --- applications ---

func (t *translator) applications() {
	taken := map[string]string{}
	policies := map[string][]davinci.FlowPolicy{}
	for _, p := range t.inv.FlowPolicies {
		policies[p.ApplicationID] = append(policies[p.ApplicationID], p)
	}
	for _, a := range t.inv.Applications {
		key := t.keyFor("application", a.ID, a.Name, taken)
		if key == "" {
			continue
		}
		path := "davinci_applications" + q(key)
		app := tfvars.Application{Name: a.Name, FlowPolicies: map[string]tfvars.FlowPolicy{}}
		if a.APIKey != nil {
			e := a.APIKey.Enabled
			app.APIKeyEnabled = &e
		}
		if o := a.OAuth; o != nil {
			app.OAuth = &tfvars.OAuth{EnforceSignedRequestOpenID: o.EnforceSignedRequestOpenid, GrantTypes: o.GrantTypes,
				LogoutURIs: o.LogoutURIs, RedirectURIs: o.RedirectURIs, Scopes: o.Scopes, SPJWKSOpenID: o.SPJWKSOpenid, SPJWKSURL: o.SPJWKSURL}
		}
		ptaken := map[string]string{}
		for _, p := range policies[a.ID] {
			pk := t.keyFor("flow policy", p.ID, p.Name, ptaken)
			if pk == "" {
				continue
			}
			pp := path + ".flow_policies" + q(pk)
			pol := tfvars.FlowPolicy{Name: p.Name}
			if p.Status != "" {
				st := p.Status
				pol.Status = &st
			}
			if tr := p.Trigger; tr != nil {
				pol.Trigger = &tfvars.PolicyTrigger{Type: tr.Type}
				if c := tr.Configuration; c != nil {
					toggle := func(x davinci.TimedToggle) *tfvars.OptionalTimedToggle {
						e, tm, tf := x.Enabled, x.Time, x.TimeFormat
						return &tfvars.OptionalTimedToggle{Enabled: &e, Time: &tm, TimeFormat: &tf}
					}
					pol.Trigger.Configuration = &tfvars.PolicyTriggerConfig{MFA: toggle(c.MFA), PWD: toggle(c.PWD)}
				}
			}
			for i, d := range p.FlowDistributions {
				fk, ok := t.flowKeys[d.ID]
				if !ok {
					t.warn("unresolved_flow", fmt.Sprintf("%s.distributions[%d]", pp, i), "", "distribution flow is not in the inventory; dropped", false)
					continue
				}
				if d.Version != -1 {
					t.warn("version_latest", fmt.Sprintf("%s.distributions[%d].version", pp, i), "", "policy pinned a source flow version; set to -1 (latest deployed)", false)
				}
				dist := tfvars.Distribution{Flow: fk, Version: -1, Weight: d.Weight, IP: d.IP}
				for _, sn := range d.SuccessNodes {
					dist.SuccessNodes = append(dist.SuccessNodes, sn.ID)
				}
				pol.Distributions = append(pol.Distributions, dist)
			}
			app.FlowPolicies[pk] = pol
			t.res.Doc.ImportIDs.FlowPolicies[key+"/"+pk] = a.ID + "/" + p.ID
		}
		t.res.Doc.Applications[key] = app
		t.res.Doc.ImportIDs.Applications[key] = a.ID
	}
}

// --- tiers ---

// assignTiers places flows top-down: flows nothing references go in the top
// tier; a subflow sits one below its lowest parent. Existing tiers are
// sticky; edges they break are reported as TierConflicts.
func (t *translator) assignTiers() {
	parents := map[string][]string{}
	for p, children := range t.edges {
		cs := sortedKeys(children)
		t.res.SubflowEdges[p] = cs
		for _, c := range cs {
			parents[c] = append(parents[c], p)
		}
	}
	// Cycle detection.
	state := map[string]int{}
	var visit func(string) bool
	visit = func(f string) bool {
		switch state[f] {
		case 1:
			return false
		case 2:
			return true
		}
		state[f] = 1
		for c := range t.edges[f] {
			if !visit(c) {
				return false
			}
		}
		state[f] = 2
		return true
	}
	for _, f := range sortedKeys(t.res.Doc.Flows) {
		if !visit(f) {
			t.fail("subflow references form a cycle involving flow %q; DaVinci tiers cannot represent cycles", f)
			return
		}
	}
	tier := map[string]int{}
	var tierOf func(string) int
	tierOf = func(f string) int {
		if v, ok := tier[f]; ok {
			return v
		}
		v := tfvars.MaxTier
		if existing, ok := t.opts.ExistingTiers[f]; ok {
			v = existing
		} else if ps := parents[f]; len(ps) > 0 {
			v = math.MaxInt
			for _, p := range ps {
				v = min(v, tierOf(p)-1)
			}
		}
		tier[f] = v
		return v
	}
	for _, f := range sortedKeys(t.res.Doc.Flows) {
		v := tierOf(f)
		if v < 0 {
			t.fail("flow %q is nested more than %d subflow levels deep", f, tfvars.MaxTier+1)
			v = 0
		}
		fl := t.res.Doc.Flows[f]
		fl.Tier = v
		t.res.Doc.Flows[f] = fl
	}
	for _, p := range sortedKeys(t.edges) {
		for _, c := range sortedKeys(t.edges[p]) {
			if _, known := t.res.Doc.Flows[c]; !known {
				continue
			}
			if tier[c] >= tier[p] {
				t.res.TierConflicts = append(t.res.TierConflicts, TierConflict{Parent: p, ParentTier: tier[p], Child: c, ChildTier: tier[c]})
			}
		}
	}
}
