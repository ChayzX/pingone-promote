package secretscan

import (
	"encoding/base64"
	"strings"
	"testing"
)

// Masked values are whole strings; asterisks inside text are not.
func TestMaskedValueIsAWholeString(t *testing.T) {
	for _, line := range []string{`  secret = "******"`, `"value" : "********",`, `"{\"clientSecret\":\"******\"}"`} {
		if len(ScanText("f", []byte(line))) != 1 {
			t.Errorf("not found: %s", line)
		}
	}
	for _, line := range []string{
		`css = "\n  :root {\n    /*******\n    * COMPANY LOGO\n    *******/\n"`,
		`"value" : "const masked = '*******' + phone.slice(-3);"`,
		`"value" : "x = \"a******b\""`,
	} {
		if f := ScanText("f", []byte(line)); len(f) != 0 {
			t.Errorf("false finding %v: %s", f, line)
		}
	}
}

func TestCredentialNamesAndValues(t *testing.T) {
	for name, want := range map[string]string{
		"davinci_variable_passwordPolicyId_company_value":       "passwordPolicyId",
		"davinci_variable_forgotPasswordUrl_flowInstance_value": "forgotPasswordUrl",
		"davinci_variable_RumAccessToken_value":                 "RumAccessToken",
	} {
		if got := VariableBase(name); got != want || CredentialName(got) != (want == "RumAccessToken") && want != "RumAccessToken" {
			t.Errorf("VariableBase(%q) = %q", name, got)
		}
	}
	for name, want := range map[string]bool{
		"LdapDSBindCreds": true, "basicAuthCredDV": true, "PingFedAuthNCreds": true, "basicAuthCred": true, "userCredentials": true,
		"adminPassword": true, "basicAuthLDAP": true, "csr_splunkHec_secret": true,
		"creditLimit": false, "forgotPasswordUrl": false, "resetPasswordPage": false, "passwordPolicyId": false, "secretQuestionFlowId": false, "logoUrl": false, "tokenEndpointUrl": false, "companyName": false,
	} {
		if got := CredentialName(name); got != want {
			t.Errorf("CredentialName(%q) = %v", name, got)
		}
	}
	for v, want := range map[string]bool{
		base64.StdEncoding.EncodeToString([]byte("uid=svc,ou=apps:s3cret!")): true,
		base64.StdEncoding.EncodeToString([]byte("user:pw")):                 true,
		"https://example.com/x": false, "aGVsbG8gd29ybGQ=": false, "Example Corp": false,
		"ghp_" + strings.Repeat("a1B2", 9): true, "Bearer abcdef0123456789": true, "Splunk 1234abcd-12ab-34cd-56ef-1234567890ab": true,
		"glpat-abcdefghij0123456789": true, "Bearer": false, "basic settings": false,
		"Authorization: Bearer abcdef0123456789xyz": true, "SSWS 00abcdefghijklmnopqrstuvwxyz": true, "ApiKey abcdef0123456789xyz": true,
		"https://x.example.com/cb?access_token=abcdef012345": true, "https://x.example.com/cb?state=abcdef012345": false,
	} {
		if got := CredentialValue(v); got != want {
			t.Errorf("CredentialValue(%q) = %v", v, got)
		}
	}
}

// A credential inside JSON text is one; labels and kinds of credentials
// are not.
func TestCredentialInsideJSONText(t *testing.T) {
	for _, v := range []string{
		`{"auth":"Bearer abcdefghijklmnopqrstuvwxyz0123"}`,
		`{"k":"ghp_abcdefghijklmnopqrstuvwxyz0123456789"}`,
		`{"basic":"dXNlcjpwYXNzd29yZDEyMw=="}`,
		`[{"clientSecret":"abc123def456"}]`,
		`{"password":"hunter2hunter2"}`,
	} {
		if !CredentialValue(v) {
			t.Errorf("not a credential: %s", v)
		}
	}
	for _, v := range []string{
		`{"forgotPasswordLabel":"Forgot your password?"}`,
		`{"secretQuestion":"What is your pet's name?"}`,
		`{"credentialType":"FIDO2"}`,
		`{"apiKeyHeader":"X-API-Key"}`,
		`{"passwordless":"true"}`,
		`{"password":"{{global.variables.pw}}"}`,
		`{"password":"********"}`,
		`{"CSRauth":true,"region":"us"}`,
		`[ "a", "b" ]`,
	} {
		if CredentialValue(v) {
			t.Errorf("a credential: %s", v)
		}
	}
}
