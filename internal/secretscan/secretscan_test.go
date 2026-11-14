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

func TestOpaqueCredentialsUnderCredentialKeys(t *testing.T) {
	for _, v := range []string{
		`{"auth":"SYNTHETIC-opaque-auth-value-123456","region":"us"}`,
		`{"authorization":"SYNTHETIC-opaque-authorization-value-123456"}`,
		`{"token":"SYNTHETIC-opaque-token-value-123456"}`,
		`{"access_token":"SYNTHETIC-opaque-access-token-value-123456"}`,
		`{"refreshToken":"SYNTHETIC-opaque-refresh-token-value-123456"}`,
		`{"id_token":"SYNTHETIC-opaque-id-token-value-123456"}`,
		`{"token":["SYNTHETIC-opaque-token-value-123456"]}`,
		`{"token":{"value":"SYNTHETIC-opaque-token-value-123456"}}`,
	} {
		if !CredentialValue(v) {
			t.Errorf("opaque credential was not found: %s", v)
		}
	}
	for _, v := range []string{
		`{"tokenEndpointUrl":"https://example.invalid/token"}`,
		`{"passwordPolicyId":"policy-123456789"}`,
		`{"secretQuestion":"What is your favorite color?"}`,
		`{"apiKeyHeader":"X-API-Key"}`,
		`{"authLabel":"Sign in"}`,
		`{"authMode":"SYNTHETIC-mode-value","authType":"SYNTHETIC-type-value","tokenType":"Bearer","authorizationEndpoint":"https://example.invalid/oauth2/authorize"}`,
		`{"auth":{"region":"us","mode":"regional"}}`,
	} {
		if CredentialValue(v) {
			t.Errorf("metadata was classified as a credential: %s", v)
		}
	}
}

// A credential is recognised by its shape whatever it is named: a URL
// with a password, name=secret, a connection string; and inside JSON a
// name/value pair, a wrapped value and more key names.
func TestCredentialShapesAndJSONPairs(t *testing.T) {
	for _, v := range []string{
		"https://user:hunter2secret@smtp.example.com",
		"postgres://svc:hunter2secret@db.example.com/app",
		"password=Sup3rS3cret!",
		"client_secret: 9f8e7d6c5b4a39281706f5e4d3c2b1a0",
		"Server=db;User=sa;Password=Sup3rS3cret",
		`{"headers":[{"name":"Authorization","value":"opaque-value-123"}]}`,
		`{"headers":[{"key":"X-Api-Key","value":"opaque-value-123"}]}`,
		`{"headers":[{"Name":"Cookie","Value":"session=abcdef0123456789"}]}`,
		`{"password":{"current":"opaque-value-123"}}`,
		`{"auth":{"user":"svc","pass":"opaque-value-123"}}`,
		`{"auth":{"data":{"value":"opaque-value-123"}}}`,
		`{"hmacKey":"opaque-value-123"}`,
		`{"url":"https://svc:opaque-value-123@host.example.com/x"}`,
		`{"connectionString":"Server=db;User=sa;Password=opaque-value-123"}`,
	} {
		if !CredentialValue(v) {
			t.Errorf("not recognised as a credential: %s", v)
		}
	}
	for _, v := range []string{
		"https://hooks.example.com/in?source=promote",
		"https://user@example.com/path",
		"https://svc:{{global.variables.pw}}@host.example.com",
		"noreply@example.com",
		"user:name",
		"password",
		"token: {{flow.token}}",
		"Reset your password = click the link",
		`{"headers":[{"name":"Content-Type","value":"application/json"}]}`,
		`{"headers":[{"name":"Authorization","value":"{{global.variables.auth}}"}]}`,
		`{"auth":{"region":"us","mode":"regional"}}`,
		`{"auth":{"type":"basic","url":"https://idp.example.com"}}`,
	} {
		if CredentialValue(v) {
			t.Errorf("an ordinary value was recognised as a credential: %s", v)
		}
	}
	for _, s := range []string{
		"token ghp_abcdefghijklmnopqrstuvwxyz0123456789",
		"see\nAuthorization: Bearer abcdef0123456789abcdef0123456789",
		"use (glpat-abcdefghij0123456789).",
	} {
		if !CredentialInText(s) {
			t.Errorf("no credential found in text: %q", s)
		}
	}
	for _, s := range []string{"PingOne: Flow B from dev", "Adds the subflow.\nSee CIAM-77 for the token lifetime change.", "Bearer tokens are now refreshed"} {
		if CredentialInText(s) {
			t.Errorf("a credential was found in ordinary text: %q", s)
		}
	}
}

// A setting about a credential is not one: an ordinary word or a small
// number under a key such as auth or token, and keys that switch
// something on or bound it. A real value under the same keys still is.
func TestSettingsAboutCredentialsAreNotSecrets(t *testing.T) {
	for _, v := range []string{
		`{"auth":"none"}`, `{"auth":"basic"}`, `{"auth":"oauth2"}`, `{"authorization":"required"}`,
		`{"token":"30"}`, `{"token":30}`, `{"idToken":"enabled"}`, `{"refreshToken":"rotate"}`, `{"mfaToken":"sms"}`,
		`{"csrfToken":"on"}`, `{"credentials":"include"}`, `{"auth":{"value":"none"}}`,
		`{"passwordExpiry":"90"}`, `{"passwordExpiry":90}`, `{"showPassword":"yes"}`, `{"forgotPassword":"Forgot your password?"}`,
		`{"requireToken":"always"}`, `{"minPasswordLength":"12"}`, `{"tokenLifetime":"3600"}`,
	} {
		if CredentialValue(v) {
			t.Errorf("a setting was taken for a credential: %s", v)
		}
	}
	for _, v := range []string{
		`{"auth":"SYNTHETIC-opaque-value-123456"}`, `{"token":"8f14e45fceea167a5a36dedd4bea2543"}`,
		`{"token":["opaque-value-123"]}`, `{"password":"none"}`, `{"password":12345678}`, `{"pin":"8841"}`,
		`{"clientSecret":"basic"}`, `{"auth":{"value":"opaque-value-123"}}`,
	} {
		if !CredentialValue(v) {
			t.Errorf("a credential was taken for a setting: %s", v)
		}
	}
}
