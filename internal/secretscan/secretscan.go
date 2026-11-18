// Package secretscan holds the secret-detection heuristics shared by the
// terraformer exporter, the neutralizer, the merge and the request job.
// They originated in the retired v1 translator and promotion packages.
//
// Every function reports positions and kinds, never values.
package secretscan

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

var (
	// Masked matches a masked value as PingOne returns it ("******").
	Masked = regexp.MustCompile(`\*{6,}`)
	// MaskedString matches a masked value as a whole quoted string in
	// configuration ("******", or \"******\" inside an encoded string),
	// not asterisks within text such as a CSS comment banner or code that
	// builds a mask ('*******' + digits).
	MaskedString = regexp.MustCompile(`(?:^|[^\\])"\*{6,}"|\\"\*{6,}\\"`)
	// PrivateKey matches a PEM private key header.
	PrivateKey = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)
	// JWT matches a signed or unsigned JSON Web Token.
	JWT = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.`)
	// TemplateOnly matches a value that is wholly a DaVinci expression or a
	// Terraform interpolation, which is a reference rather than a secret.
	TemplateOnly = regexp.MustCompile(`^\s*(\{\{[^{}]*\}\}|\$\{[^{}]*\})\s*$`)
)

func compact(key string) string {
	return strings.ToLower(strings.NewReplacer("_", "", "-", "", " ", "").Replace(key))
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]`)

var secretSuffixes = []string{"secret", "password", "passwd", "passphrase", "privatekey", "apikey", "token", "credential", "credentials", "sharedkey", "signingkey"}

// HasSecretSuffix is the narrow rule (suffix match) used for names of
// variables and parameters, where "key" alone is too common.
func HasSecretSuffix(name string) bool {
	n := nonAlnum.ReplaceAllString(strings.ToLower(name), "")
	for _, s := range secretSuffixes {
		if strings.HasSuffix(n, s) {
			return true
		}
	}
	return false
}

// Kinds of findings.
const (
	KindMasked     = "masked_value"
	KindPrivateKey = "private_key"
	KindJWT        = "jwt"
	KindNamed      = "secret_named_literal"
)

// Finding is one suspected secret. It never carries the value.
type Finding struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Kind string `json:"kind"`
	// Name is the attribute or variable name, when known.
	Name string `json:"name,omitempty"`
}

// ScanText reports masked values, private keys and JWTs line by line.
func ScanText(file string, data []byte) []Finding {
	var out []Finding
	for i, line := range bytes.Split(data, []byte("\n")) {
		switch {
		case PrivateKey.Match(line):
			out = append(out, Finding{File: file, Line: i + 1, Kind: KindPrivateKey})
		case JWT.Match(line):
			out = append(out, Finding{File: file, Line: i + 1, Kind: KindJWT})
		case MaskedString.Match(line):
			out = append(out, Finding{File: file, Line: i + 1, Kind: KindMasked})
		}
	}
	return out
}

// ScanFiles scans every file in a path-to-content map in path order.
func ScanFiles(files map[string][]byte) []Finding {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var out []Finding
	for _, p := range paths {
		out = append(out, ScanText(p, files[p])...)
	}
	return out
}

// SuspectLiteral reports whether a literal value under a secret-looking name
// should be treated as a leaked secret. Empty values, references and masked
// values (reported separately) are not suspects.
func SuspectLiteral(name, value string) bool {
	v := strings.TrimSpace(value)
	return v != "" && HasSecretSuffix(name) && !TemplateOnly.MatchString(v) && !Masked.MatchString(v)
}

// CredentialName is the rule for DaVinci variable names: one that names a
// credential anywhere (basicAuthCred, LdapDSBindCreds, PingFedAuthNCreds,
// RumAccessToken) rather than only at the end.
func CredentialName(name string) bool {
	if urlNameRE.MatchString(name) {
		return false // forgotPasswordUrl: a link, not a password
	}
	if credWordRE.MatchString(name) {
		return true
	}
	n := compact(name)
	for _, s := range []string{"password", "passwd", "passphrase", "secret", "apikey", "privatekey", "basicauth", "bindpw"} {
		if strings.Contains(n, s) {
			return true
		}
	}
	return false
}

// urlNameRE matches names of links, addresses and IDs (a password
// policy's ID is not a password).
var urlNameRE = regexp.MustCompile(`(?i)(url|uri|href|link|endpoint|path|page|redirect)s?$|(Id|ID|_id|Ids|IDs|_ids)$`)

// tokenRE matches an HTTP authorization value and the access tokens of
// well-known services (GitHub, GitLab, Slack, AWS, Stripe, OpenAI-style
// keys, Splunk HEC "Splunk <token>").
var tokenRE = regexp.MustCompile(`^(?i:authorization:\s*)?(?i:bearer|basic|splunk|token|ssws|apikey|api-key|negotiate)\s+[A-Za-z0-9._~+/=-]{16,}$|^(?:gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,}|glpat-[A-Za-z0-9_-]{20,}|xox[abpors]-[A-Za-z0-9-]{10,}|AKIA[0-9A-Z]{16}|[sr]k_(?:live|test)_[A-Za-z0-9]{16,}|sk-[A-Za-z0-9_-]{20,})$`)

// credWordRE matches "cred", "creds" or "credential(s)" as a word of a
// camelCase or snake_case name (not "credit").
var credWordRE = regexp.MustCompile(`[Cc]red(?:s|entials?)?(?:$|[^a-z])`)

// CredentialValue reports whether a value is a credential whatever its
// name: a JWT, a private key, or base64 of "user:password".
func CredentialValue(v string) bool {
	v = strings.TrimSpace(v)
	if JSONText(v) {
		return jsonCredential(v)
	}
	if JWT.MatchString(v) || PrivateKey.MatchString(v) || tokenRE.MatchString(v) || queryTokenRE.MatchString(v) || urlPassword(v) || namedPairRE.MatchString(v) {
		return true
	}
	if len(v) <= 512 && connectionPasswordRE.MatchString(v) {
		return true
	}
	if len(v) < 8 || strings.ContainsAny(v, " {}<>") {
		return false
	}
	b, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return false
	}
	user, pass, ok := strings.Cut(string(b), ":")
	if !ok || user == "" || pass == "" || !utf8.Valid(b) {
		return false
	}
	for _, r := range string(b) {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// userinfoRE matches a URL with a user and a password before the host
// (https://user:password@host).
var userinfoRE = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^/\s:@]*:([^/\s@]+)@`)

// urlPassword reports whether v holds a URL with a literal password. A
// password that is wholly a reference ({{...}} or ${...}) is not one.
func urlPassword(v string) bool {
	for _, m := range userinfoRE.FindAllStringSubmatch(v, -1) {
		if !TemplateOnly.MatchString(m[1]) {
			return true
		}
	}
	return false
}

// namedPairRE matches a value that is wholly "name=secret" or
// "name: secret" for a credential's name (password=..., client_secret: ...).
var namedPairRE = regexp.MustCompile(`(?i)^(?:password|passwd|pwd|client[_-]?secret|api[_-]?key|secret|token)\s*[:=]\s*[^\s{}$]\S{5,}$`)

// connectionPasswordRE matches the password of a connection string
// (Server=db;User=sa;Password=...).
var connectionPasswordRE = regexp.MustCompile(`(?i)(?:^|;)\s*(?:password|pwd)\s*=\s*[^;\s{}$][^;\s]{3,}`)

// CredentialInText reports whether free text (a title or a note) holds a
// credential: a private key, a JWT, or a word (or a "Bearer <token>"
// pair of words) that CredentialValue recognises.
func CredentialInText(s string) bool {
	if PrivateKey.MatchString(s) || JWT.MatchString(s) {
		return true
	}
	words := strings.Fields(s)
	for i, w := range words {
		w = strings.Trim(w, `"'()[]<>,;.:!?`)
		if !JSONText(w) && CredentialValue(w) {
			return true
		}
		if i+1 < len(words) && tokenRE.MatchString(w+" "+strings.Trim(words[i+1], `"'()[]<>,;.`)) {
			return true
		}
	}
	return false
}

// queryTokenRE matches a URL that carries a credential in its query.
var queryTokenRE = regexp.MustCompile(`(?i)[?&#](access_token|id_token|refresh_token|api_?key|client_secret|password|passwd|sig|token)=[^&#\s]{8,}`)

// variableContextRE matches the context a split DaVinci value variable
// carries after its name (davinci_variable_<name>_<context>_value).
var variableContextRE = regexp.MustCompile(`_(company|flowInstance|flow|user)$`)

// VariableBase is the DaVinci variable name a value variable stands for:
// davinci_variable_passwordPolicyId_company_value -> passwordPolicyId.
func VariableBase(name string) string {
	base := strings.TrimSuffix(strings.TrimPrefix(name, "davinci_variable_"), "_value")
	return variableContextRE.ReplaceAllString(base, "")
}

// JSONText reports whether v is the text of a JSON object or list (a
// DaVinci variable holding an object is kept as such text).
func JSONText(v string) bool {
	v = strings.TrimSpace(v)
	return (strings.HasPrefix(v, "{") || strings.HasPrefix(v, "[")) && json.Valid([]byte(v))
}

func jsonCredential(v string) bool {
	d := json.NewDecoder(strings.NewReader(v))
	d.UseNumber()
	var x any
	if d.Decode(&x) != nil {
		return false
	}
	return CredentialInJSON(x)
}

// CredentialInJSON reports whether a decoded JSON value holds a
// credential: a string that is one (CredentialValue or a ScanText
// finding), or a non-empty, non-template string under a key named like a
// credential (CredentialKey).
func CredentialInJSON(x any) bool {
	switch t := x.(type) {
	case string:
		return CredentialValue(t) || len(ScanText("", []byte(t))) > 0
	case []any:
		for _, e := range t {
			if CredentialInJSON(e) {
				return true
			}
		}
	case map[string]any:
		for k, e := range t {
			if CredentialKey(k) && hasCredentialLiteral(e, broadKey(k)) {
				return true
			}
			if CredentialInJSON(e) {
				return true
			}
		}
		// {"name": "Authorization", "value": "..."}: a header or setting
		// given as a pair, whose name names a credential.
		if val, ok := t[pairKey(t, "value")].(string); ok && literal(val) {
			for _, nk := range []string{"name", "key", "header"} {
				if n, ok := t[pairKey(t, nk)].(string); ok && pairName(n) {
					return true
				}
			}
		}
	}
	return false
}

// literal reports whether s is a value, not empty, a reference or masked.
func literal(s string) bool {
	return strings.TrimSpace(s) != "" && !TemplateOnly.MatchString(s) && !Masked.MatchString(s)
}

// pairKey finds the key of m that is want, whatever its case.
func pairKey(m map[string]any, want string) string {
	for k := range m {
		if strings.EqualFold(k, want) {
			return k
		}
	}
	return want
}

// pairName reports whether the name of a name/value pair names a
// credential (Authorization, X-Api-Key, Cookie, clientSecret).
func pairName(n string) bool {
	switch compact(n) {
	case "cookie", "setcookie":
		return true
	}
	return CredentialKey(n) || strings.Contains(compact(n), "authorization")
}

// hasCredentialLiteral checks a value already protected by a credential key.
// Some DaVinci configs wrap credentials in arrays or objects; retain the key's
// context while walking those containers so opaque leaves are not missed.
func hasCredentialLiteral(x any, broad bool) bool {
	switch v := x.(type) {
	case string:
		if broad && settingValue(v) {
			return false
		}
		return strings.TrimSpace(v) != "" && !TemplateOnly.MatchString(v) && !Masked.MatchString(v)
	case json.Number:
		// A numeric password or PIN ({"password": 12345678}); a small
		// number under a broad key is a setting ({"token": 30}).
		return !broad && len(v.String()) >= 4
	case float64:
		return !broad && (v >= 1000 || v <= -1000)
	case []any:
		for _, e := range v {
			if hasCredentialLiteral(e, broad) {
				return true
			}
		}
	case map[string]any:
		// A member counts when it names a credential itself or is a
		// common wrapper ({"password": {"current": "..."}}, {"auth":
		// {"data": {"value": "..."}}}); other members are settings
		// ({"auth": {"region": "us", "mode": "regional"}}).
		for k, e := range v {
			switch {
			case CredentialKey(k):
				if hasCredentialLiteral(e, broadKey(k)) {
					return true
				}
			case wrapperKeys[compact(k)]:
				if hasCredentialLiteral(e, broad) {
					return true
				}
			}
		}
	}
	return false
}

// wrapperKeys are keys that only wrap the value of their parent.
var wrapperKeys = map[string]bool{"value": true, "val": true, "current": true, "new": true, "old": true, "plain": true,
	"plaintext": true, "raw": true, "data": true, "content": true, "string": true}

// broadKey reports whether a key names a credential or, as often, a
// setting about one ("auth": "basic", "token": "30", "mfaToken": "sms").
// Under such a key an ordinary setting word is not a credential; under
// "password" or "clientSecret" any value is.
func broadKey(k string) bool {
	c := compact(k)
	switch c {
	case "auth", "authorization", "credential", "credentials", "cookie", "bearer", "jwt":
		return true
	}
	return strings.HasSuffix(c, "token")
}

// settingValue reports whether v is a word or a small number that
// configures something rather than proving who someone is.
func settingValue(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	if settingWords[v] {
		return true
	}
	if len(v) == 0 || len(v) > 6 {
		return false
	}
	for _, r := range v {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

var settingWords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`none basic bearer digest oauth oauth1 oauth2 oidc saml ldap kerberos ntlm mtls
		apikey api_key jwt opaque required optional enabled disabled true false yes no on off include omit
		same-origin sameorigin header cookie query body form rotate reuse sms email totp push voice default
		auto manual internal external public client user session id_token access_token refresh_token code
		password client_credentials implicit`) {
		m[w] = true
	}
	return m
}()

// labelKeyRE matches keys that name a label, a text or a kind of
// credential rather than one (forgotPasswordLabel, secretQuestion,
// credentialType, apiKeyHeader, passwordless).
var labelKeyRE = regexp.MustCompile(`(?i)(label|text|question|type|header|name|less|message|title|hint|prompt|placeholder|description|policy|enabled|required|length|mode|format|field|rules?|expiry|expiration|lifetime|ttl|age|days|history|strength|complexity|attempts|count|reset|reuse|rotation|validity|timeout|duration)s?$`)

// settingKeyRE matches keys that switch something about a credential on
// or off, or bound it (showPassword, forgotPassword, requireToken,
// minPasswordLength).
var settingKeyRE = regexp.MustCompile(`^(?i:show|hide|forgot|forgotten|allow|require|enable|disable|is|has|use|min|max|can|should)[A-Z_-]`)

// CredentialKey reports whether a key inside a JSON value names a
// credential (password, clientSecret, apiKey), not a label of one.
func CredentialKey(k string) bool {
	if labelKeyRE.MatchString(k) || settingKeyRE.MatchString(k) {
		return false
	}
	switch compact(k) {
	case "auth", "authorization", "token", "accesstoken", "refreshtoken", "idtoken",
		"pass", "pwd", "passcode", "pin", "bearer", "jwt", "cookie", "hmackey", "encryptionkey", "connectionstring", "dsn":
		return true
	default:
		return CredentialName(k) || HasSecretSuffix(k)
	}
}
