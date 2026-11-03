// Package keys derives stable logical keys from PingOne resource names.
//
// A logical key is the same in every environment, so it is a pure function of
// the resource's identity (its name, plus context and flow for variables). It
// never depends on PingOne IDs or on the order resources are listed in.
// Encoding is lossless: Decode(Encode(name)) == name for every byte string, and
// every key has exactly one decoding.
//
// Keys match ^[A-Za-z0-9_-]{1,128}$. Name encoding:
//   - A-Z, a-z, 0-9 and _ are kept as is;
//   - "-" is written "--";
//   - every other byte is written "-XX" with two uppercase hex digits
//     (UTF-8 is escaped byte by byte, so "é" becomes "-C3-A9").
//
// Decode accepts only that canonical form; "-2D" for "-" or "-41" for "A" are
// rejected so that one name has one key.
//
// Variable keys always encode the context, and the flow for flow-scoped
// variables, so a company variable and a flow variable with the same name, or
// the same flow variable name on two flows, never collide:
//
//	<name>_<context>            company, flowInstance, user
//	<name>_flow_<flowKey>       flow
//
// In the <name> part "_" is also escaped (as "-5F") so that the first "_" is
// always the separator, which keeps variable keys decodable.
package keys

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// MaxLen is the maximum logical key length.
const MaxLen = 128

// VariableContexts are the pingone_davinci_variable context values accepted
// by provider pingidentity/pingone 1.23.
var VariableContexts = []string{"company", "flow", "flowInstance", "user"}

// ErrTooLong means the encoded key exceeds MaxLen. Truncating would lose the
// one-to-one mapping, so the caller must ask for a shorter name.
var ErrTooLong = errors.New("logical key would exceed 128 characters; shorten the resource name")

// ErrEmpty means the name is empty.
var ErrEmpty = errors.New("resource name is empty")

// ErrInvalidKey means a key is not in canonical encoded form.
var ErrInvalidKey = errors.New("not a canonical logical key")

const hexDigits = "0123456789ABCDEF"

func plain(c byte, escapeUnderscore bool) bool {
	switch {
	case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		return true
	case c == '_':
		return !escapeUnderscore
	}
	return false
}

func encode(name string, escapeUnderscore bool) string {
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case plain(c, escapeUnderscore):
			b.WriteByte(c)
		case c == '-':
			b.WriteString("--")
		default:
			b.WriteByte('-')
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&0xF])
		}
	}
	return b.String()
}

func unhex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

func decode(key string, escapeUnderscore bool) (string, error) {
	var b strings.Builder
	for i := 0; i < len(key); i++ {
		c := key[i]
		if plain(c, escapeUnderscore) {
			b.WriteByte(c)
			continue
		}
		if c != '-' || i+1 >= len(key) {
			return "", ErrInvalidKey
		}
		if key[i+1] == '-' {
			b.WriteByte('-')
			i++
			continue
		}
		if i+2 >= len(key) {
			return "", ErrInvalidKey
		}
		hi, ok1 := unhex(key[i+1])
		lo, ok2 := unhex(key[i+2])
		if !ok1 || !ok2 {
			return "", ErrInvalidKey
		}
		v := hi<<4 | lo
		if plain(v, escapeUnderscore) || v == '-' {
			return "", ErrInvalidKey // non-canonical escape
		}
		b.WriteByte(v)
		i += 2
	}
	return b.String(), nil
}

func checkLen(key string) (string, error) {
	if len(key) > MaxLen {
		return "", ErrTooLong
	}
	return key, nil
}

// Encode returns the logical key for a connection, flow or application name.
func Encode(name string) (string, error) {
	if name == "" {
		return "", ErrEmpty
	}
	return checkLen(encode(name, false))
}

// Decode returns the name encoded in key.
func Decode(key string) (string, error) {
	if key == "" || len(key) > MaxLen {
		return "", ErrInvalidKey
	}
	return decode(key, false)
}

func validContext(c string) bool {
	for _, x := range VariableContexts {
		if x == c {
			return true
		}
	}
	return false
}

// VariableKey returns the logical key for a DaVinci variable. flowKey is the
// owning flow's logical key and is required exactly when context is "flow".
func VariableKey(name, context, flowKey string) (string, error) {
	if name == "" {
		return "", ErrEmpty
	}
	if !validContext(context) {
		return "", fmt.Errorf("unknown variable context; want one of %s", strings.Join(VariableContexts, ", "))
	}
	key := encode(name, true) + "_" + context
	if context == "flow" {
		if _, err := Decode(flowKey); err != nil {
			return "", errors.New("flow-scoped variable needs the owning flow's logical key")
		}
		key += "_" + flowKey
	} else if flowKey != "" {
		return "", errors.New("only flow-scoped variables have a flow key")
	}
	return checkLen(key)
}

// ParseVariableKey reverses VariableKey.
func ParseVariableKey(key string) (name, context, flowKey string, err error) {
	if key == "" || len(key) > MaxLen {
		return "", "", "", ErrInvalidKey
	}
	encName, rest, ok := strings.Cut(key, "_")
	if !ok || encName == "" {
		return "", "", "", ErrInvalidKey
	}
	if name, err = decode(encName, true); err != nil {
		return "", "", "", err
	}
	context, flowKey, hasFlow := strings.Cut(rest, "_")
	if !validContext(context) || hasFlow != (context == "flow") {
		return "", "", "", ErrInvalidKey
	}
	if hasFlow {
		if _, err := Decode(flowKey); err != nil {
			return "", "", "", ErrInvalidKey
		}
	}
	return name, context, flowKey, nil
}

// DuplicateNameError lists names that occur more than once. Identical names
// cannot get distinct keys without depending on order or IDs, so the user
// must rename one of them.
type DuplicateNameError struct{ Names []string }

func (e *DuplicateNameError) Error() string {
	return fmt.Sprintf("%d resource name(s) are used more than once; rename them so each logical key is unique", len(e.Names))
}

// Assign maps each name to its key. The result does not depend on the order
// of names. Duplicate names return a *DuplicateNameError naming them.
func Assign(names []string) (map[string]string, error) {
	out := make(map[string]string, len(names))
	dup := map[string]bool{}
	for _, n := range names {
		if _, seen := out[n]; seen {
			dup[n] = true
			continue
		}
		k, err := Encode(n)
		if err != nil {
			return nil, err
		}
		out[n] = k
	}
	if len(dup) > 0 {
		e := &DuplicateNameError{}
		for n := range dup {
			e.Names = append(e.Names, n)
		}
		sort.Strings(e.Names)
		return nil, e
	}
	return out, nil
}
