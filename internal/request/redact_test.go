package request

import (
	"encoding/json"
	"strings"
	"testing"
)

// Fixture values shaped like secrets; none is real.
const (
	fixtureJWT      = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJmaXh0dXJlLW9ubHkifQ.Zml4dHVyZS1zaWduYXR1cmU"
	fixtureLiteral  = "fixture-literal-6f3b0c"
	fixturePEMBody  = "MIIBfixtureONLYfixtureONLYfixtureONLY"
	fixturePEMBlock = "-----BEGIN RSA PRIVATE KEY-----\n+" + fixturePEMBody + "\n+-----END RSA PRIVATE KEY-----"
)

var fixtureLeaks = []string{fixtureJWT, "Zml4dHVyZS1zaWduYXR1cmU", fixtureLiteral, fixturePEMBody}

func assertNoLeak(t *testing.T, r *Result) {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range fixtureLeaks {
		if strings.Contains(string(b), v) {
			t.Fatalf("result.json carries a secret-shaped fixture value %q:\n%s", v, b)
		}
	}
}

func TestRedactEveryStatus(t *testing.T) {
	for _, status := range []string{StatusPreviewed, StatusSubmitted, StatusBlocked, StatusFailed} {
		t.Run(status, func(t *testing.T) {
			r := newResult()
			r.Status = status
			r.Diff = "+resource \"x\" \"y\" {\n+  client_secret = \"" + fixtureLiteral + "\"\n+  name = \"kept\"\n+  token_value = \"${var.x}\"\n+  bearer = \"" + fixtureJWT + "\"\n+  key = <<EOT\n+" + fixturePEMBlock + "\n+EOT\n+}\n"
			r.Warnings = []string{"connector says api_token = \"" + fixtureLiteral + "\"", "token " + fixtureJWT}
			r.Blockers[RequestBlockers] = []BlockerOut{{Code: "secret_finding", Message: "found \"clientSecret\": \"" + fixtureLiteral + "\" in export"}}
			r.Selected = []Selected{{Query: fixtureJWT}}
			r.Changes = []ChangeOut{{Address: "a.b", Reason: "password = \"" + fixtureLiteral + "\""}}
			if status == StatusBlocked || status == StatusFailed {
				r.Error = &ErrorOut{Code: ErrPreflight, Message: "exporter failed: " + fixturePEMBlock + " " + fixtureJWT,
					Candidates: []Selected{{Query: fixtureJWT}}}
			}
			r.redact()
			assertNoLeak(t, r)
			for _, keep := range []string{`name = "kept"`, `token_value = "${var.x}"`, `client_secret = "[redacted]"`, "+}"} {
				if !strings.Contains(r.Diff, keep) {
					t.Fatalf("redaction removed %q or did not mark the value:\n%s", keep, r.Diff)
				}
			}
		})
	}
}

func TestRedactLeavesOrdinaryTextAlone(t *testing.T) {
	for _, s := range []string{
		"+resource \"pingone_davinci_flow\" \"pingcli__Flow-0020-B\" {\n+  name = \"Flow B\"\n+}\n",
		`qa.tfvars must set pingone_davinci_flow_x_id: the ID of the existing flow`,
		`+  client_secret = "******"`,
		`+  token_endpoint_auth_method = "CLIENT_SECRET_BASIC"`,
	} {
		if got := redactText(s); got != s {
			t.Fatalf("changed ordinary text:\n%s\n->\n%s", s, got)
		}
	}
}

// TestFailedRunRedactsEchoedInput runs the engine: the selection error
// quotes the requested flow name, which here looks like a token.
func TestFailedRunRedactsEchoedInput(t *testing.T) {
	req := requestJSON(func(m map[string]any) {
		m["flows"] = []map[string]string{{"name": fixtureJWT}}
		m["dryRun"] = true
	})
	h := newHarness(t, req)
	res := h.run(req)
	if res.Status != StatusFailed || res.Error == nil {
		t.Fatalf("want a failed selection, got %s %+v", res.Status, res.Error)
	}
	if !strings.Contains(res.Error.Message, "[redacted]") {
		t.Fatalf("error message was not redacted: %s", res.Error.Message)
	}
	assertNoLeak(t, res)
}

func TestRedactOpaqueBearerTokens(t *testing.T) {
	for _, in := range []string{
		"Authorization: Bearer abcdefghijklmnop0123456789ACCESS",
		"authorization=Basic dXNlcjpwYXNzd29yZA==",
		"curl -H 'authorization: bearer zz-opaque.token~value_1234'",
	} {
		out := redactText(in)
		if !strings.Contains(out, "[redacted]") || strings.Contains(out, "0123456789ACCESS") || strings.Contains(out, "dXNlcjpwYXNzd29yZA") || strings.Contains(out, "opaque.token") {
			t.Errorf("redactText(%q) = %q", in, out)
		}
	}
}
