// Package secretscan holds the secret-detection heuristics shared by the
// translator (v1), the tfvars checker, the terraformer exporter and the
// neutralizer. They moved here from internal/translate/secrets.go (which
// took them from promotion.Redact) so the v2 path does not depend on the
// retired packages.
//
// Every function reports positions and kinds, never values.
package secretscan

import (
	"bytes"
	"regexp"
	"sort"
	"strings"
)

var (
	// Masked matches a masked value as PingOne returns it ("******").
	Masked = regexp.MustCompile(`\*{6,}`)
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

// LooksLikeSecretName is the broad rule (substring match) used to classify
// connector and node properties: password, secret, token, key, credential...
func LooksLikeSecretName(key string) bool {
	lk := compact(key)
	for _, s := range []string{"password", "passphrase", "secret", "token", "key", "credential", "authentication", "authorization", "connectionstring", "connectionuri"} {
		if strings.Contains(lk, s) {
			return true
		}
	}
	return lk == "dsn" || lk == "auth"
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
		case Masked.Match(line):
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
