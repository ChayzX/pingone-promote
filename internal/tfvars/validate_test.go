package tfvars

import (
	"strings"
	"testing"
)

func codes(r Report) map[string]bool {
	out := map[string]bool{}
	for _, p := range r.Problems {
		out[p.Code] = true
	}
	return out
}

func TestValidateFullFixture(t *testing.T) {
	if r := Validate(fullDocument()); !r.OK() {
		t.Fatalf("fixture invalid: %+v", r.Problems)
	}
	if r := Validate(emptyDocument()); !r.OK() {
		t.Fatalf("empty invalid: %+v", r.Problems)
	}
}

func setNodeProps(d *Document, flow, node, props string) {
	f := d.Flows[flow]
	n := f.Graph.Nodes[node]
	n.Properties = ptr(props)
	f.Graph.Nodes[node] = n
	d.Flows[flow] = f
}

func TestValidateProblems(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Document)
		code   string
	}{
		{"schema version", func(d *Document) { d.SchemaVersion = 2 }, CodeSchemaVersion},
		{"environment id", func(d *Document) { d.EnvironmentID = "not-a-uuid" }, CodeEnvironmentID},
		{"bad flow key", func(d *Document) {
			d.Flows["bad key"] = d.Flows["login"]
			delete(d.Flows, "login")
			d.Applications = nil
			delete(d.Variables, "attempts_flow_login")
			d.ImportIDs.Flows = nil
		}, CodeKeyFormat},
		{"bad parameter key", func(d *Document) { d.Parameters["Bad-Key"] = "x" }, CodeKeyFormat},
		{"tier violation", func(d *Document) {
			f := d.Flows["mfa_subflow"]
			f.Tier = 3
			d.Flows["mfa_subflow"] = f
		}, CodeTierOrder},
		{"self reference", func(d *Document) {
			setNodeProps(d, "error_leaf", "n0err1", `{"x": {"value": "@@flow:error_leaf@@"}}`)
		}, CodeTierOrder},
		{"unknown flow", func(d *Document) { setNodeProps(d, "login", "j0eval", `{"x": "@@flow:nope@@"}`) }, CodeRefUnknown},
		{"unknown connection placeholder", func(d *Document) { setNodeProps(d, "login", "j0eval", `{"x": "@@connection:nope@@"}`) }, CodeRefUnknown},
		{"unknown param", func(d *Document) { setNodeProps(d, "login", "j0eval", `{"x": "@@param:nope@@"}`) }, CodeRefUnknown},
		{"unknown node connection", func(d *Document) {
			f := d.Flows["login"]
			n := f.Graph.Nodes["j0eval"]
			n.Connection = ptr("nope")
			f.Graph.Nodes["j0eval"] = n
		}, CodeRefUnknown},
		{"flow-scoped variable by id", func(d *Document) { setNodeProps(d, "login", "j0eval", `{"x": "@@variable:attempts_flow_login@@"}`) }, CodeRefKind},
		{"secret in variable json", func(d *Document) {
			v := d.Variables["theme_company"]
			v.Value = &VariableValue{JSONObject: ptr(`{"k": "@@secret:x@@"}`)}
			d.Variables["theme_company"] = v
		}, CodeRefKind},
		{"flow ref in connection", func(d *Document) {
			c := d.Connections["http"]
			c.Properties = ptr(`{"k": "@@flow:login@@"}`)
			d.Connections["http"] = c
		}, CodeRefKind},
		{"unbalanced", func(d *Document) { setNodeProps(d, "login", "j0eval", `{"x": "a@@b"}`) }, CodeRefUnbalanced},
		{"malformed", func(d *Document) { setNodeProps(d, "login", "j0eval", `{"x": "@@secret:has-dash@@"}`) }, CodeRefMalformed},
		{"dotted secret key", func(d *Document) { setNodeProps(d, "login", "j0eval", `{"x": "@@secret:pingone_sso.client_secret@@"}`) }, CodeRefMalformed},
		{"dotted variable secret", func(d *Document) {
			v := d.Variables["apiToken_company"]
			v.Secret = ptr("dv.api_token")
			d.Variables["apiToken_company"] = v
		}, CodeKeyFormat},
		{"unknown kind", func(d *Document) { setNodeProps(d, "login", "j0eval", `{"x": "@@p1:population:x@@"}`) }, CodeRefMalformed},
		{"invalid json", func(d *Document) { setNodeProps(d, "login", "j0eval", `{"x": 1,}`) }, CodeJSON},
		{"json array", func(d *Document) { setNodeProps(d, "login", "j0eval", `[1]`) }, CodeJSON},
		{"duplicate json key", func(d *Document) { setNodeProps(d, "login", "j0eval", `{"x": 1, "x": 2}`) }, CodeJSON},
		{"placeholder in plain string", func(d *Document) {
			f := d.Flows["login"]
			f.Description = ptr("see @@secret:x@@")
			d.Flows["login"] = f
		}, CodeRefPlainString},
		{"masked secret", func(d *Document) {
			c := d.Connections["http"]
			c.Properties = ptr(`{"clientSecret": {"value": "******"}}`)
			d.Connections["http"] = c
		}, CodeMaskedSecret},
		{"literal secret", func(d *Document) {
			c := d.Connections["http"]
			c.Properties = ptr(`{"clientSecret": {"type": "string", "value": "hunter2-fake"}}`)
			d.Connections["http"] = c
		}, CodeSecretSuspect},
		{"private key", func(d *Document) { setNodeProps(d, "login", "j0eval", `{"x": "-----BEGIN RSA PRIVATE KEY-----"}`) }, CodeSecretSuspect},
		{"secret-named parameter", func(d *Document) { d.Parameters["api_token"] = "fake-value" }, CodeSecretSuspect},
		{"variable key without context", func(d *Document) {
			d.Variables["brand"] = d.Variables["brand_company"]
			delete(d.Variables, "brand_company")
			delete(d.ImportIDs.Variables, "brand_company")
			setNodeProps(d, "mfa_subflow", "n2var1", `{"x": "@@variable:brand@@"}`)
		}, CodeVariableKey},
		{"duplicate variable identity", func(d *Document) { d.Variables["brand_company_copy"] = d.Variables["brand_company"] }, CodeVariableIdentity},
		{"flow variable without flow", func(d *Document) {
			v := d.Variables["attempts_flow_login"]
			v.Flow = nil
			d.Variables["attempts_flow_login"] = v
		}, CodeVariableRules},
		{"secret variable with value", func(d *Document) {
			v := d.Variables["apiToken_company"]
			v.Value = &VariableValue{String: ptr("x")}
			d.Variables["apiToken_company"] = v
		}, CodeVariableRules},
		{"value type mismatch", func(d *Document) {
			v := d.Variables["brand_company"]
			v.Value = &VariableValue{Bool: ptr(true)}
			d.Variables["brand_company"] = v
		}, CodeVariableRules},
		{"two values", func(d *Document) {
			v := d.Variables["brand_company"]
			v.Value = &VariableValue{String: ptr("a"), Bool: ptr(true)}
			d.Variables["brand_company"] = v
		}, CodeVariableRules},
		{"bad context", func(d *Document) {
			v := d.Variables["brand_company"]
			v.Context = "global"
			d.Variables["brand_company"] = v
		}, CodeEnum},
		{"bad tier", func(d *Document) {
			f := d.Flows["login"]
			f.Tier = 4
			d.Flows["login"] = f
		}, CodeEnum},
		{"edge to missing node", func(d *Document) {
			f := d.Flows["login"]
			f.Graph.Edges["e9"] = Edge{Source: "a1b2c3", Target: "missing"}
		}, CodeGraph},
		{"policy to unknown flow", func(d *Document) {
			a := d.Applications["web_login"]
			p := a.FlowPolicies["default"]
			p.Distributions = []Distribution{{Flow: "nope", Version: -1}}
			a.FlowPolicies["default"] = p
		}, CodeRefUnknown},
		{"import without definition", func(d *Document) { d.ImportIDs.Flows["ghost"] = "fake-flow-9" }, CodeImport},
		{"bad policy import id", func(d *Document) { d.ImportIDs.FlowPolicies["web_login/default"] = "only-one-part" }, CodeImport},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := fullDocument()
			tc.mutate(d)
			r := Validate(d)
			if !codes(r)[tc.code] {
				t.Fatalf("want %s, got %+v", tc.code, r.Problems)
			}
			for _, p := range r.Problems {
				if strings.Contains(p.Message, "hunter2") || strings.Contains(p.Message, "fake-value") {
					t.Fatalf("problem leaked a value: %+v", p)
				}
			}
		})
	}
}

// The coordinator's collision rule: same name in different contexts, or on
// different flows, is valid and distinct; the same identity twice is not.
func TestValidateVariableIdentities(t *testing.T) {
	d := fullDocument()
	d.Variables["attempts_flow_mfa_subflow"] = Variable{Name: "attempts", Context: "flow", Flow: ptr("mfa_subflow"), DataType: "number", Mutable: true}
	d.Variables["attempts_user"] = Variable{Name: "attempts", Context: "user", DataType: "number", Mutable: true}
	if r := Validate(d); !r.OK() {
		t.Fatalf("distinct identities rejected: %+v", r.Problems)
	}
}

func TestValidateDoesNotFlagExpressionsOrPlaceholders(t *testing.T) {
	d := fullDocument()
	setNodeProps(d, "login", "j0eval", `{"accessToken": {"value": "{{global.variables.token}}"}, "password": "${user.password}", "tokenEndpoint": "https://idp.example.com/token"}`)
	if r := Validate(d); !r.OK() {
		t.Fatalf("false positive: %+v", r.Problems)
	}
}
