package neutralize

import (
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

var unsafeLabelChars = regexp.MustCompile(`[^0-9A-Za-z_\-]`)

func sanitizePart(s string) string {
	return unsafeLabelChars.ReplaceAllStringFunc(s, func(m string) string { return fmt.Sprintf("-%04X-", m) })
}

// Label reproduces utils.SanitizeResourceName / SanitizeMultiKeyResourceName
// from pingcli-plugin-terraformer v0.6.0 (internal/utils/sanitize.go):
// special characters become "-XXXX-" (hex of the UTF-8 bytes, at least four
// digits), keys are joined with "_", and the label is prefixed "pingcli__".
func Label(keys ...string) string {
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = sanitizePart(k)
	}
	return LabelPrefix + strings.Join(parts, "_")
}

// VariableName reproduces utils.SanitizeVariableName: every character
// outside [A-Za-z0-9_] becomes "_".
func VariableName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

var escapedRun = regexp.MustCompile(`-((?:[0-9A-F]{2}){2,8})-`)

// DecodeLabel recovers a display name from a single-key label, for
// searching --list-resources output, which carries no names. A "_N"
// disambiguation suffix (allow_duplicate_labels types) is kept. ok is false
// when the label is not terraformer-shaped.
func DecodeLabel(label string) (name string, ok bool) {
	if !strings.HasPrefix(label, LabelPrefix) {
		return label, false
	}
	body := strings.TrimPrefix(label, LabelPrefix)
	out := escapedRun.ReplaceAllStringFunc(body, func(m string) string {
		raw := unpad(m[1 : len(m)-1])
		if raw == nil || !utf8.Valid(raw) || utf8.RuneCount(raw) != 1 {
			return m
		}
		return string(raw)
	})
	return out, true
}

// unpad decodes the hex digits, undoing %04X zero padding for one-byte
// characters ("0020" is a space).
func unpad(digits string) []byte {
	if len(digits) == 4 && digits[:2] == "00" {
		digits = digits[2:]
	}
	b, err := hex.DecodeString(digits)
	if err != nil {
		return nil
	}
	return b
}

// stripPrefix returns a label without "pingcli__".
func stripPrefix(label string) string { return strings.TrimPrefix(label, LabelPrefix) }
