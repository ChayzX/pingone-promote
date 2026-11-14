package mockapi

// Demo IDs of the two invented environments.
const (
	DevID = "11111111-1111-4111-8111-111111111111"
	QAID  = "22222222-2222-4222-8222-222222222222"
)

func node(id, name, connector, capability string, props map[string]any) map[string]any {
	return map[string]any{"id": id, "name": name, "connectorId": connector, "capabilityName": capability, "properties": props}
}

// Demo is an invented pair of environments: dev has a new version of the
// "CIAM Sign On" flow that calls a new MFA subflow and logs to Splunk;
// qa still has the earlier version. Nothing here is real tenant data.
func Demo() *Mock {
	devFlow := map[string]any{
		"name": "CIAM Sign On", "description": "Sign on with step-up MFA for risky sessions", "deployedVersion": 7.0,
		"settings": map[string]any{"logLevel": 2.0, "flowTimeoutInSeconds": 600.0, "csp": "worker-src 'self' blob:;"},
		"graphData": map[string]any{"nodes": []any{
			node("start", "Sign-on form", "httpConnector", "customHTMLTemplate", map[string]any{"formTitle": "Sign on", "showForgotPassword": true}),
			node("check", "Check password", "pingOneSSOConnector", "checkPassword", map[string]any{"population": "{{global.variables.population}}"}),
			node("risk", "Evaluate risk", "pingOneRiskConnector", "evaluateRisk", map[string]any{"riskPolicy": "CIAM default", "highRiskAction": "mfa"}),
			node("mfa", "Step-up MFA", "flowConnector", "startUiSubFlow", map[string]any{"subFlow": "MFA Subflow"}),
			node("audit", "Send audit event", "splunkConnector", "sendEvent", map[string]any{"index": "ciam", "event": "sign_on"}),
			node("done", "Return success", "httpConnector", "createSuccessResponse", map[string]any{}),
		}},
	}
	qaFlow := map[string]any{
		"name": "CIAM Sign On", "description": "Sign on", "deployedVersion": 5.0,
		"settings": map[string]any{"logLevel": 1.0, "flowTimeoutInSeconds": 300.0, "csp": "worker-src 'self' blob:;"},
		"graphData": map[string]any{"nodes": []any{
			node("start", "Sign-on form", "httpConnector", "customHTMLTemplate", map[string]any{"formTitle": "Sign on", "showForgotPassword": false}),
			node("check", "Check password", "pingOneSSOConnector", "checkPassword", map[string]any{"population": "{{global.variables.population}}"}),
			node("done", "Return success", "httpConnector", "createSuccessResponse", map[string]any{}),
		}},
	}
	subflow := map[string]any{
		"name": "MFA Subflow", "description": "Prompt for a registered device", "deployedVersion": 2.0,
		"settings": map[string]any{"logLevel": 1.0},
		"graphData": map[string]any{"nodes": []any{
			node("devices", "List devices", "pingOneMfaConnector", "listDevices", map[string]any{}),
			node("otp", "Check one-time passcode", "pingOneMfaConnector", "checkOtp", map[string]any{"attempts": 3.0}),
		}},
	}
	splunk := func(host, token string) map[string]any {
		return map[string]any{"name": "Splunk HEC", "connectorId": "splunkConnector",
			"properties": map[string]any{"url": "https://" + host + ":8088/services/collector", "token": token, "verifyTls": true}}
	}
	variable := func(v string) map[string]any {
		return map[string]any{"name": "mfaForHighRisk", "context": "company", "type": "boolean", "value": v, "mutable": true}
	}
	dev := map[string]Res{
		"f-signon":  {ID: "f-signon", Kind: "flows", Name: "CIAM Sign On", Config: devFlow, Refs: []string{"f-mfa", "c-splunk", "v-mfa"}},
		"f-mfa":     {ID: "f-mfa", Kind: "flows", Name: "MFA Subflow", Config: subflow},
		"c-splunk":  {ID: "c-splunk", Kind: "connectorInstances", Name: "Splunk HEC", Config: splunk("splunk.dev.example.com", "SYNTHETIC-dev-hec-token-000111")},
		"v-mfa":     {ID: "v-mfa", Kind: "variables", Name: "mfaForHighRisk", Config: variable("true")},
		"f-profile": {ID: "f-profile", Kind: "flows", Name: "Profile Update", Config: map[string]any{"name": "Profile Update", "deployedVersion": 3.0}},
	}
	qa := map[string]Res{
		"q-signon": {ID: "q-signon", Kind: "flows", Name: "CIAM Sign On", Config: qaFlow},
		"q-splunk": {ID: "q-splunk", Kind: "connectorInstances", Name: "Splunk HEC", Config: splunk("splunk.qa.example.com", "SYNTHETIC-qa-hec-token-000222")},
		"q-mfa":    {ID: "q-mfa", Kind: "variables", Name: "mfaForHighRisk", Config: variable("false")},
	}
	return New(map[string]map[string]Res{DevID: dev, QAID: qa})
}
