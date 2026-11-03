package tfvars

import (
	"bytes"
	"errors"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// heredocMarker delimits canonical JSON heredocs. A canonical JSON line is
// never exactly this word because every JSON line starts with a quote,
// bracket or brace.
const heredocMarker = "JSON"

// WriteError reports a value the writer cannot represent. It names the path,
// never the value.
type WriteError struct {
	Path    string
	Message string
}

func (e *WriteError) Error() string { return e.Path + ": " + e.Message }

// Write renders doc in the canonical, app-managed layout: managed header,
// sorted map keys, quoted map keys, one attribute per line, canonical JSON
// heredocs, sorted and de-duplicated sets, and hclwrite formatting (the same
// formatter terraform fmt uses). The output is deterministic, and
// Write(Parse(Write(doc))) is byte-identical to Write(doc).
func Write(doc *Document) ([]byte, error) {
	if doc == nil {
		return nil, errors.New("tfvars: nil document")
	}
	w := &writer{}
	w.b.WriteString(Header)
	w.b.WriteString("\n")
	rv := reflect.ValueOf(doc).Elem()
	for i, f := range fieldsOf(rv.Type()) {
		// Scalars at the top stay together; each collection gets its own
		// paragraph.
		if i < 2 {
			if i == 0 {
				w.b.WriteString("\n")
			}
		} else {
			w.b.WriteString("\n")
		}
		w.attr(f, rv.Field(f.index), 0, "")
		if w.err != nil {
			return nil, w.err
		}
	}
	return hclwrite.Format(w.b.Bytes()), nil
}

type writer struct {
	b   bytes.Buffer
	err error
}

func (w *writer) fail(path, msg string) {
	if w.err == nil {
		w.err = &WriteError{Path: path, Message: msg}
	}
}

func indent(depth int) string { return strings.Repeat("  ", depth) }

// attr writes "name = value" at depth. Absent optional values are skipped.
// Top-level nil maps are written as {} so that every document declares every
// collection.
func (w *writer) attr(f field, v reflect.Value, depth int, parent string) {
	path := attrPath(parent, f.name)
	if f.optional && isAbsent(v) {
		return
	}
	w.b.WriteString(indent(depth))
	w.b.WriteString(f.name)
	w.b.WriteString(" = ")
	w.value(f, v, depth, path)
	w.b.WriteString("\n")
}

func isAbsent(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Map:
		return v.IsNil()
	}
	return false
}

func (w *writer) value(f field, v reflect.Value, depth int, path string) {
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	switch v.Kind() {
	case reflect.String:
		if f.json {
			w.heredoc(v.String(), depth, path)
			return
		}
		w.quoted(v.String(), path)
	case reflect.Bool:
		w.b.WriteString(strconv.FormatBool(v.Bool()))
	case reflect.Int:
		w.b.WriteString(strconv.FormatInt(v.Int(), 10))
	case reflect.Float64:
		s, ok := formatNumber(v.Float())
		if !ok {
			w.fail(path, "number is not finite")
			return
		}
		w.b.WriteString(s)
	case reflect.Slice:
		w.list(f, v, depth, path)
	case reflect.Map:
		w.mapValue(f, v, depth, path)
	case reflect.Struct:
		w.object(v, depth, path)
	default:
		w.fail(path, "unsupported kind")
	}
}

func (w *writer) quoted(s, path string) {
	if !utf8.ValidString(s) {
		w.fail(path, "string is not valid UTF-8")
		return
	}
	w.b.Write(hclwrite.TokensForValue(cty.StringVal(s)).Bytes())
}

// heredoc writes canonical JSON as an indented heredoc with template
// sequences escaped, so Terraform reads back exactly the canonical JSON.
func (w *writer) heredoc(s string, depth int, path string) {
	canonical, err := CanonicalJSON(s)
	if err != nil {
		w.fail(path, err.Error())
		return
	}
	w.b.WriteString("<<-" + heredocMarker + "\n")
	for _, line := range strings.Split(canonical, "\n") {
		w.b.WriteString(indent(depth + 1))
		w.b.WriteString(escapeTemplate(line))
		w.b.WriteString("\n")
	}
	w.b.WriteString(indent(depth))
	w.b.WriteString(heredocMarker)
}

// escapeTemplate doubles the template introducers ${ and %{, the same rule
// hclwrite applies to quoted strings.
func escapeTemplate(s string) string {
	if !strings.ContainsAny(s, "$%") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		b.WriteByte(c)
		if (c == '$' || c == '%') && i+1 < len(s) && s[i+1] == '{' {
			b.WriteByte(c)
		}
	}
	return b.String()
}

func (w *writer) list(f field, v reflect.Value, depth int, path string) {
	if v.Type().Elem().Kind() == reflect.String {
		items := make([]string, v.Len())
		for i := range items {
			items[i] = v.Index(i).String()
		}
		if f.set {
			items = sortedUnique(items)
		}
		w.b.WriteString("[")
		for i, s := range items {
			if i > 0 {
				w.b.WriteString(", ")
			}
			w.quoted(s, path+"["+strconv.Itoa(i)+"]")
		}
		w.b.WriteString("]")
		return
	}
	if v.Len() == 0 {
		w.b.WriteString("[]")
		return
	}
	w.b.WriteString("[\n")
	for i := 0; i < v.Len(); i++ {
		w.b.WriteString(indent(depth + 1))
		w.value(field{}, v.Index(i), depth+1, path+"["+strconv.Itoa(i)+"]")
		w.b.WriteString(",\n")
	}
	w.b.WriteString(indent(depth))
	w.b.WriteString("]")
}

func (w *writer) mapValue(f field, v reflect.Value, depth int, path string) {
	if v.Len() == 0 {
		w.b.WriteString("{}")
		return
	}
	keys := make([]string, 0, v.Len())
	for _, k := range v.MapKeys() {
		keys = append(keys, k.String())
	}
	sort.Strings(keys)
	w.b.WriteString("{\n")
	for _, k := range keys {
		child := path + "[" + strconv.Quote(k) + "]"
		w.b.WriteString(indent(depth + 1))
		w.quoted(k, child)
		w.b.WriteString(" = ")
		w.value(field{json: f.json}, v.MapIndex(reflect.ValueOf(k)), depth+1, child)
		w.b.WriteString("\n")
	}
	w.b.WriteString(indent(depth))
	w.b.WriteString("}")
}

func (w *writer) object(v reflect.Value, depth int, path string) {
	fields := fieldsOf(v.Type())
	empty := true
	for _, f := range fields {
		if !f.optional || !isAbsent(v.Field(f.index)) {
			empty = false
			break
		}
	}
	if empty {
		w.b.WriteString("{}")
		return
	}
	w.b.WriteString("{\n")
	for _, f := range fields {
		w.attr(f, v.Field(f.index), depth+1, path)
	}
	w.b.WriteString(indent(depth))
	w.b.WriteString("}")
}

func sortedUnique(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	j := 0
	for i, s := range out {
		if i == 0 || s != out[j-1] {
			out[j] = s
			j++
		}
	}
	return out[:j]
}

// formatNumber renders the shortest decimal that parses back to f. Very large
// or small magnitudes use exponent notation, which HCL accepts.
func formatNumber(f float64) (string, bool) {
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return "", false
	}
	if f == 0 {
		return "0", true
	}
	a := math.Abs(f)
	if a >= 1e-6 && a < 1e21 {
		return strconv.FormatFloat(f, 'f', -1, 64), true
	}
	return strconv.FormatFloat(f, 'e', -1, 64), true
}
