package secretscan

import (
	"encoding/base64"
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
	for name, want := range map[string]bool{
		"LdapDSBindCreds": true, "basicAuthCredDV": true, "PingFedAuthNCreds": true, "basicAuthCred": true, "userCredentials": true,
		"adminPassword": true, "basicAuthLDAP": true, "csr_splunkHec_secret": true,
		"creditLimit": false, "forgotPasswordUrl": false, "resetPasswordPage": false, "logoUrl": false, "tokenEndpointUrl": false, "companyName": false,
	} {
		if got := CredentialName(name); got != want {
			t.Errorf("CredentialName(%q) = %v", name, got)
		}
	}
	for v, want := range map[string]bool{
		base64.StdEncoding.EncodeToString([]byte("uid=svc,ou=apps:s3cret!")): true,
		base64.StdEncoding.EncodeToString([]byte("user:pw")):                 true,
		"https://example.com/x": false, "aGVsbG8gd29ybGQ=": false, "Example Corp": false,
	} {
		if got := CredentialValue(v); got != want {
			t.Errorf("CredentialValue(%q) = %v", v, got)
		}
	}
}
