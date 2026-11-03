package translate

import (
	"regexp"
	"strings"
)

// Secret naming rules. Each rule is one function so the convention can change
// in one place. The generated name is only a suggestion for a NEW secret; an
// operator can map any site to an existing pipeline secret name through
// Options.SecretNames.

var unsafeSecretChars = regexp.MustCompile(`[^A-Za-z0-9_]`)

func sanitizeSecretPart(s string) string { return unsafeSecretChars.ReplaceAllString(s, "_") }

// VariableSecretName is the suggested secret key for a DaVinci variable with
// data type "secret", for example davinci_variable_PingFedAuthNCreds_value.
func VariableSecretName(variableName string) string {
	return "davinci_variable_" + sanitizeSecretPart(variableName) + "_value"
}

// ConnectionSecretName is the suggested secret key for a secret connector
// property, for example PingOne_DaVinci_connection_PingOne_clientSecret. This
// follows a truncated example from the enterprise pipeline and is a best guess.
func ConnectionSecretName(connectionName, property string) string {
	return "PingOne_DaVinci_connection_" + sanitizeSecretPart(connectionName) + "_" + sanitizeSecretPart(property)
}

// NodeSecretName is the suggested secret key for a secret value inside a flow
// node's properties. There is no enterprise example; this is an assumption.
func NodeSecretName(flowName, nodeID, property string) string {
	return "PingOne_DaVinci_flow_" + sanitizeSecretPart(flowName) + "_" + sanitizeSecretPart(nodeID) + "_" + sanitizeSecretPart(property)
}

// Secret-looking names. These heuristics move here from promotion.Redact,
// which is deleted with the native workflow in #44. Here a match is not a
// silent redaction: it classifies the value as secret and raises a warning
// the operator must acknowledge (or override with Options.NotSecret).
func redactionKey(key string) string {
	return strings.ToLower(strings.NewReplacer("_", "", "-", "", " ", "").Replace(key))
}

func secretLookingName(key string) bool {
	lk := redactionKey(key)
	for _, s := range []string{"password", "passphrase", "secret", "token", "key", "credential", "authentication", "authorization", "connectionstring", "connectionuri"} {
		if strings.Contains(lk, s) {
			return true
		}
	}
	return lk == "dsn" || lk == "auth"
}

// templateOnly matches a value that is wholly a DaVinci expression, which is
// a reference rather than a secret.
var templateOnly = regexp.MustCompile(`^\s*(\{\{[^{}]*\}\}|\$\{[^{}]*\})\s*$`)
