package tfvars

// Fixtures use obviously fake IDs and no real tenant data or secrets.

const fakeEnvID = "00000000-0000-4000-8000-000000000001"

func ptr[T any](v T) *T { return &v }

func emptyDocument() *Document {
	return &Document{
		SchemaVersion: 1,
		EnvironmentID: fakeEnvID,
		Parameters:    map[string]string{},
		Connections:   map[string]Connection{},
		Variables:     map[string]Variable{},
		Flows:         map[string]Flow{},
		Applications:  map[string]Application{},
		ImportIDs: ImportIDs{
			Connections: map[string]string{}, Flows: map[string]string{}, Variables: map[string]string{},
			Applications: map[string]string{}, FlowPolicies: map[string]string{},
		},
	}
}

// fullDocument exercises every placeholder kind, all four tiers, template
// escaping, the @@@@ escape, a company and a flow variable sharing a name,
// and import IDs for every resource kind and tier. The module tests consume
// the golden written from it.
func fullDocument() *Document {
	d := emptyDocument()
	d.Parameters = map[string]string{
		"idp_base_url":  "https://idp.dev.example.com",
		"support_email": "support@example.com",
	}
	d.Connections = map[string]Connection{
		"http":           {Name: "Http", Connector: "httpConnector"},
		"flow_conductor": {Name: "Flow Conductor", Connector: "flowConnector"},
		"pingone_sso": {Name: "PingOne", Connector: "pingOneSSOConnector", Properties: ptr(`{
			"clientId": {"type": "string", "value": "fake-client-id"},
			"clientSecret": {"type": "string", "value": "@@secret:PingOne_DaVinci_connection_PingOne_clientSecret@@"},
			"envId": {"type": "string", "value": "@@env:id@@"},
			"baseUrl": {"type": "string", "value": "@@param:idp_base_url@@/as"}
		}`)},
		"functions": {Name: "Functions", Connector: "functionsConnector", Properties: ptr(`{
			"note": {"type": "string", "value": "literal @@@@ stays"}
		}`)},
	}
	d.Variables = map[string]Variable{
		"brand_company": {Name: "brand", Context: "company", DataType: "string", Mutable: true,
			DisplayName: ptr("Brand"), Value: &VariableValue{String: ptr("acme")}},
		"apiToken_company": {Name: "apiToken", Context: "company", DataType: "secret", Mutable: false,
			Secret: ptr("davinci_variable_apiToken_value")},
		"attempts_company": {Name: "attempts", Context: "company", DataType: "number", Mutable: true,
			Min: ptr(0.0), Max: ptr(10.0), Value: &VariableValue{Float32: ptr(3.0)}},
		"attempts_flow_login": {Name: "attempts", Context: "flow", Flow: ptr("login"), DataType: "number", Mutable: true,
			Value: &VariableValue{Float32: ptr(0.0)}},
		"theme_company": {Name: "theme", Context: "company", DataType: "object", Mutable: true,
			Value: &VariableValue{JSONObject: ptr(`{"logo": "@@param:idp_base_url@@/logo.png", "dark": false}`)}},
		"greeting_flowInstance": {Name: "greeting", Context: "flowInstance", DataType: "string", Mutable: true,
			Value: &VariableValue{String: ptr("Hello ${user.name}")}},
	}
	d.Flows = map[string]Flow{
		"error_leaf": {Name: "Error leaf", Tier: 0, Graph: Graph{
			Nodes: map[string]Node{
				"n0err1": {NodeType: "CONNECTION", Connection: ptr("http"), ConnectorID: ptr("httpConnector"),
					CapabilityName: ptr("customHtmlMessage"), Label: ptr("Error"),
					Properties: ptr(`{"message": {"value": "Hi ${global.variables.brand}, 100%{done}"}}`),
					Position:   &Position{X: 277, Y: 236.25}},
			},
			Edges: map[string]Edge{},
		}},
		"password_reset": {Name: "Password reset", Tier: 1, Deploy: ptr(true), Graph: Graph{
			Nodes: map[string]Node{
				"n1sub1": subflowNode("error_leaf", "Error leaf"),
			},
			Edges: map[string]Edge{},
		}},
		"mfa_subflow": {Name: "MFA subflow", Tier: 2, Enabled: ptr(false),
			InputSchema: []InputField{{PropertyName: ptr("username"), PreferredDataType: ptr("string"), Required: ptr(true)}},
			OutputSchema: &OutputSchema{Output: OutputSpec{Type: "object", AdditionalProperties: ptr(false),
				Properties: ptr(`{"result": {"type": "string"}}`)}},
			Graph: Graph{
				Nodes: map[string]Node{
					"n2sub1": subflowNode("password_reset", "Password reset"),
					"n2sub2": subflowNode("error_leaf", "Error leaf"),
					"n2var1": {NodeType: "CONNECTION", Connection: ptr("http"), ConnectorID: ptr("httpConnector"),
						CapabilityName: ptr("customHtmlMessage"),
						Properties:     ptr(`{"variableId": {"value": "@@variable:brand_company@@"}}`)},
				},
				Edges: map[string]Edge{
					"e2a": {Source: "n2sub1", Target: "n2sub2"},
				},
			}},
		"login": {Name: "Login", Description: ptr("Primary sign-on"), Color: ptr("#AFD5FF"), Tier: 3, Enabled: ptr(true),
			Settings: &FlowSettings{LogLevel: ptr(2), CSSLinks: []string{"https://cdn.example.com/b.css", "https://cdn.example.com/a.css"},
				UseCSP: ptr(true), CSP: ptr("script-src 'self'"), FlowHTTPTimeoutInSeconds: ptr(300.0)},
			Trigger: &FlowTrigger{Type: "AUTHENTICATION"},
			Graph: Graph{
				Zoom: ptr(1.0), MaxZoom: ptr(1e50), MinZoom: ptr(1e-50), Pan: &Position{X: 0, Y: -12.5},
				PanningEnabled: ptr(true), Renderer: ptr(`{"name":"null"}`),
				Nodes: map[string]Node{
					"a1b2c3": {NodeType: "CONNECTION", Connection: ptr("pingone_sso"), ConnectorID: ptr("pingOneSSOConnector"),
						CapabilityName: ptr("userLookup"), Position: &Position{X: 240, Y: 120},
						Outcomes: []Outcome{{ID: "found", Label: "Found", Result: "true"}},
						Properties: ptr(`{
							"username": {"value": "${global.parameters.username}"},
							"hmacKey": {"value": "@@secret:login_hmac_key@@"},
							"support": {"value": "mailto:@@param:support_email@@"}
						}`)},
					"d4e5f6": subflowNode("mfa_subflow", "MFA subflow"),
					"g7h8i9": subflowNode("error_leaf", "Error leaf"),
					"j0eval": {NodeType: "EVAL", Label: ptr("Evaluator")},
				},
				Edges: map[string]Edge{
					"e1": {Source: "a1b2c3", Target: "d4e5f6"},
					"e2": {Source: "a1b2c3", Target: "j0eval", MultiValueSourceID: ptr("found")},
					"e3": {Source: "j0eval", Target: "g7h8i9"},
				},
			}},
	}
	d.Applications = map[string]Application{
		"web_login": {Name: "Web Login", APIKeyEnabled: ptr(true),
			OAuth: &OAuth{GrantTypes: []string{"authorizationCode"}, Scopes: []string{"profile", "openid"},
				RedirectURIs: []string{"https://app.dev.example.com/cb"}, EnforceSignedRequestOpenID: ptr(false)},
			FlowPolicies: map[string]FlowPolicy{
				"default": {Name: "Login", Status: ptr("enabled"),
					Distributions: []Distribution{{Flow: "login", Version: -1, Weight: ptr(100.0)}}},
			}},
	}
	d.ImportIDs = ImportIDs{
		Connections:  map[string]string{"pingone_sso": "fake-conn-0001"},
		Flows:        map[string]string{"login": "fake-flow-0003", "mfa_subflow": "fake-flow-0002", "error_leaf": "fake-flow-0000"},
		Variables:    map[string]string{"brand_company": "fake-var-0001", "attempts_flow_login": "fake-var-0002"},
		Applications: map[string]string{"web_login": "fake-app-0001"},
		FlowPolicies: map[string]string{"web_login/default": "fake-app-0001/fake-policy-0001"},
	}
	return d
}

func subflowNode(flowKey, label string) Node {
	return Node{NodeType: "CONNECTION", Connection: ptr("flow_conductor"), ConnectorID: ptr("flowConnector"),
		CapabilityName: ptr("startUiSubFlow"),
		Properties: ptr(`{"subFlowId": {"value": {"label": "` + label + `", "value": "@@flow:` + flowKey + `@@"}},
			"subFlowVersionId": {"value": -1}}`)}
}
