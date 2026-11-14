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
