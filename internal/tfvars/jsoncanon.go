package tfvars

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"pingone-promote/internal/strictjson"
)

var errJSON = errors.New("must be a JSON object with unique keys")

// CanonicalJSON returns the canonical form of a JSON object: keys sorted,
// two-space indentation, numbers kept as written, and no trailing newline.
// Strings keep printable UTF-8 when they are already NFC; otherwise their
// non-ASCII characters are \u-escaped so that HCL's NFC normalization of the
// surrounding heredoc can never change the decoded value.
func CanonicalJSON(s string) (string, error) {
	v, err := decodeJSONObject(s)
	if err != nil {
		return "", err
	}
	for _, asciiOnly := range []bool{false, true} {
		var b strings.Builder
		writeJSON(&b, v, 0, asciiOnly)
		out := b.String()
		if norm.NFC.IsNormalString(out) {
			return out, nil
		}
	}
	return "", errors.New("tfvars: canonical JSON is not NFC") // unreachable: ASCII is NFC
}

func decodeJSONObject(s string) (map[string]any, error) {
	if !utf8.ValidString(s) {
		return nil, errJSON
	}
	var probe any
	if err := strictjson.Unmarshal([]byte(s), &probe); err != nil {
		return nil, errJSON
	}
	d := json.NewDecoder(strings.NewReader(s))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, errJSON
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, errJSON
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, errJSON
	}
	return obj, nil
}

func writeJSON(b *strings.Builder, v any, depth int, asciiOnly bool) {
	indent := func(d int) { b.WriteString(strings.Repeat("  ", d)) }
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case json.Number:
		b.WriteString(x.String())
	case string:
		writeJSONString(b, x, asciiOnly)
	case []any:
		if len(x) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteString("[\n")
		for i, e := range x {
			indent(depth + 1)
			writeJSON(b, e, depth+1, asciiOnly)
			if i < len(x)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		indent(depth)
		b.WriteByte(']')
	case map[string]any:
		if len(x) == 0 {
			b.WriteString("{}")
			return
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString("{\n")
		for i, k := range keys {
			indent(depth + 1)
			writeJSONString(b, k, asciiOnly)
			b.WriteString(": ")
			writeJSON(b, x[k], depth+1, asciiOnly)
			if i < len(keys)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		indent(depth)
		b.WriteByte('}')
	default:
		panic(fmt.Sprintf("tfvars: unexpected JSON type %T", v))
	}
}

func writeJSONString(b *strings.Builder, s string, asciiOnly bool) {
	escapeAll := asciiOnly || !norm.NFC.IsNormalString(s)
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(b, `\u%04x`, r)
		case r < utf8.RuneSelf:
			b.WriteRune(r)
		case escapeAll || !unicode.IsPrint(r):
			if r > 0xffff {
				r1, r2 := utf16.EncodeRune(r)
				fmt.Fprintf(b, `\u%04x\u%04x`, r1, r2)
			} else {
				fmt.Fprintf(b, `\u%04x`, r)
			}
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
}

// jsonEqual reports whether two JSON documents are semantically identical
// after canonicalization. Invalid JSON compares by raw bytes.
func jsonEqual(a, b string) bool {
	ca, errA := CanonicalJSON(a)
	cb, errB := CanonicalJSON(b)
	if errA != nil || errB != nil {
		return a == b
	}
	return ca == cb
}
