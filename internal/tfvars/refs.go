package tfvars

import (
	"regexp"
	"strings"
)

// Placeholder kinds. A placeholder is written @@kind:key@@ inside a JSON
// heredoc; @@@@ is a literal @@. The module resolves placeholders with
// split("@@", s): odd-indexed segments are reference tokens.
const (
	KindSecret     = "secret"
	KindParam      = "param"
	KindEnv        = "env"
	KindConnection = "connection"
	KindFlow       = "flow"
	KindVariable   = "variable"
	kindEscape     = ""
)

var (
	logicalKeyRE   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	valueKeyRE     = regexp.MustCompile(`^[a-z0-9_]{1,128}$`)
	uuidRE         = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	importIDRE     = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	policyIDRE     = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}/[A-Za-z0-9-]{1,64}$`)
	looksLikeRefRE = regexp.MustCompile(`@@(secret|param|env|connection|flow|variable):`)
)

// IsLogicalKey reports whether k is a valid logical key for connections,
// variables, flows, applications, flow policies, graph nodes and edges.
func IsLogicalKey(k string) bool { return logicalKeyRE.MatchString(k) }

// IsValueKey reports whether k is a valid secret or parameter key.
func IsValueKey(k string) bool { return valueKeyRE.MatchString(k) }

// Ref is one parsed placeholder.
type Ref struct {
	Kind string
	Key  string
}

// refTokens splits s the same way the module's resolver does. balanced is
// false when s has an unpaired @@.
func refTokens(s string) (tokens []string, balanced bool) {
	parts := strings.Split(s, "@@")
	for i := 1; i < len(parts); i += 2 {
		tokens = append(tokens, parts[i])
	}
	return tokens, len(parts)%2 == 1
}

// parseRef interprets a token. ok is false for malformed tokens.
func parseRef(tok string) (Ref, bool) {
	if tok == "" {
		return Ref{Kind: kindEscape}, true
	}
	kind, key, found := strings.Cut(tok, ":")
	if !found {
		return Ref{}, false
	}
	switch kind {
	case KindEnv:
		return Ref{Kind: kind, Key: key}, key == "id"
	case KindSecret, KindParam:
		return Ref{Kind: kind, Key: key}, IsValueKey(key)
	case KindConnection, KindFlow, KindVariable:
		return Ref{Kind: kind, Key: key}, IsLogicalKey(key)
	}
	return Ref{}, false
}
