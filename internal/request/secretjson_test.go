package request

import (
	"encoding/json"
	"testing"
)

// A secret inside JSON text (a DaVinci variable's value kept as a string)
// is refused like one inside a JSON value.
func TestSecretInsideJSONText(t *testing.T) {
	for _, s := range []string{
		`{"auth":"Bearer abcdefghijklmnopqrstuvwxyz0123"}`,
		`{"k":"ghp_abcdefghijklmnopqrstuvwxyz0123456789"}`,
		`{"basic":"dXNlcjpwYXNzd29yZDEyMw=="}`,
		`["Bearer abcdefghijklmnopqrstuvwxyz0123"]`,
		`{"password":"hunter2hunter2"}`,
		` { "a": [ {"clientSecret": "abc123def456"} ] } `,
	} {
		raw, _ := json.Marshal(s)
		if SecretKind("davinci_variable_cfg_value", raw) == "" {
			t.Errorf("accepted as text: %s", s)
		}
		if SecretKind("davinci_variable_cfg_value", json.RawMessage(s)) == "" {
			t.Errorf("accepted as JSON: %s", s)
		}
	}
	for _, s := range []string{`{"CSRauth":true,"region":"us"}`, `{"password":""}`, `{"passwordPolicy":"{{global.policy}}"}`, `[1,2]`, `not json {`} {
		raw, _ := json.Marshal(s)
		if k := SecretKind("davinci_variable_cfg_value", raw); k != "" {
			t.Errorf("refused %s (%s)", s, k)
		}
	}
}

func TestOpaqueJSONCredentialsAreRejectedAsValuesAndText(t *testing.T) {
	for _, input := range []struct {
		name       string
		raw        string
		wantSecret bool
	}{
		{"JSON value", `{"auth":"SYNTHETIC-opaque-auth-value-123456","region":"us"}`, true},
		{"JSON text", `"{\"auth\":\"SYNTHETIC-opaque-auth-value-123456\",\"region\":\"us\"}"`, true},
		{"nested array", `{"token":["SYNTHETIC-opaque-token-value-123456"]}`, true},
		{"JSON value metadata", `{"authMode":"SYNTHETIC-mode-value","authType":"SYNTHETIC-type-value","tokenType":"Bearer","authorizationEndpoint":"https://example.invalid/oauth2/authorize"}`, false},
		{"template reference", `{"token":"{{global.variables.apiToken}}"}`, false},
	} {
		if kind := SecretKind("davinci_variable_config_value", json.RawMessage(input.raw)); (kind != "") != input.wantSecret {
			t.Errorf("SecretKind for %s = %q, want secret=%v", input.name, kind, input.wantSecret)
		}
	}
}
