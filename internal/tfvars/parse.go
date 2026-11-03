package tfvars

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"sort"
	"strconv"
	"unicode/utf8"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

// MaxFileSize bounds the input accepted by Parse.
const MaxFileSize = 32 << 20

// maxDepth bounds bracket nesting before the recursive parser runs. Schema v1
// needs about ten levels; the margin is generous.
const maxDepth = 32

// ParseError reports why a file is not a valid schema v1 document. It carries
// a source position or an attribute path and never an attribute value.
type ParseError struct {
	Pos     string // "file:line:col" when known
	Path    string // attribute path when known
	Message string
}

func (e *ParseError) Error() string {
	switch {
	case e.Pos != "" && e.Path != "":
		return fmt.Sprintf("%s: %s: %s", e.Pos, e.Path, e.Message)
	case e.Pos != "":
		return e.Pos + ": " + e.Message
	case e.Path != "":
		return e.Path + ": " + e.Message
	}
	return e.Message
}

// Parse decodes a values-only tfvars file into a Document. The file may
// contain only literal values: no variables, functions, operators (other than
// a negative number sign), conditionals, for expressions, template
// interpolations or blocks. Unknown and missing attributes are errors.
func Parse(src []byte, filename string) (*Document, error) {
	if len(src) > MaxFileSize {
		return nil, &ParseError{Message: "file exceeds the maximum tfvars size"}
	}
	if !utf8.Valid(src) {
		return nil, &ParseError{Message: "file is not valid UTF-8"}
	}
	if err := checkDepth(src, filename); err != nil {
		return nil, err
	}
	file, diags := hclsyntax.ParseConfig(src, filename, hcl.InitialPos)
	if diags.HasErrors() {
		return nil, diagError(diags)
	}
	body, ok := file.Body.(*hclsyntax.Body)
	if !ok {
		return nil, &ParseError{Message: "unexpected HCL body"}
	}
	if len(body.Blocks) > 0 {
		b := body.Blocks[0]
		return nil, &ParseError{Pos: posString(b.TypeRange.Start, filename), Message: "blocks are not allowed in tfvars"}
	}
	values := map[string]cty.Value{}
	for name, attr := range body.Attributes {
		if err := checkLiteral(attr.Expr, filename, name); err != nil {
			return nil, err
		}
		v, diags := attr.Expr.Value(nil)
		if diags.HasErrors() {
			return nil, diagError(diags)
		}
		values[name] = v
	}
	var doc Document
	if err := decodeAttrs(values, reflect.ValueOf(&doc).Elem(), ""); err != nil {
		return nil, err
	}
	return &doc, nil
}

func posString(p hcl.Pos, filename string) string {
	return fmt.Sprintf("%s:%d:%d", filename, p.Line, p.Column)
}

// diagError keeps only the position and summary of the first error, because
// diagnostic details can quote source text.
func diagError(diags hcl.Diagnostics) error {
	for _, d := range diags {
		if d.Severity != hcl.DiagError {
			continue
		}
		e := &ParseError{Message: "invalid HCL: " + d.Summary}
		if d.Subject != nil {
			e.Pos = posString(d.Subject.Start, d.Subject.Filename)
		}
		return e
	}
	return &ParseError{Message: "invalid HCL"}
}

func checkDepth(src []byte, filename string) error {
	tokens, diags := hclsyntax.LexConfig(src, filename, hcl.InitialPos)
	if diags.HasErrors() {
		return diagError(diags)
	}
	depth := 0
	for _, t := range tokens {
		switch t.Type {
		case hclsyntax.TokenOBrace, hclsyntax.TokenOBrack, hclsyntax.TokenOParen,
			hclsyntax.TokenTemplateInterp, hclsyntax.TokenTemplateControl:
			depth++
			if depth > maxDepth {
				return &ParseError{Pos: posString(t.Range.Start, filename), Message: "nesting is too deep"}
			}
		case hclsyntax.TokenCBrace, hclsyntax.TokenCBrack, hclsyntax.TokenCParen, hclsyntax.TokenTemplateSeqEnd:
			depth--
		}
	}
	return nil
}

// checkLiteral rejects anything that is not a literal value, and duplicate
// object keys.
func checkLiteral(e hclsyntax.Expression, filename, path string) error {
	notLiteral := func() error {
		return &ParseError{Pos: posString(e.Range().Start, filename), Path: path,
			Message: "only literal values are allowed (no references, functions, operators or templates)"}
	}
	switch x := e.(type) {
	case *hclsyntax.LiteralValueExpr:
		return nil
	case *hclsyntax.TemplateExpr:
		for _, part := range x.Parts {
			lit, ok := part.(*hclsyntax.LiteralValueExpr)
			if !ok || lit.Val.Type() != cty.String {
				return notLiteral()
			}
		}
		return nil
	case *hclsyntax.UnaryOpExpr:
		lit, ok := x.Val.(*hclsyntax.LiteralValueExpr)
		if x.Op != hclsyntax.OpNegate || !ok || lit.Val.Type() != cty.Number {
			return notLiteral()
		}
		return nil
	case *hclsyntax.TupleConsExpr:
		for i, item := range x.Exprs {
			if err := checkLiteral(item, filename, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
		return nil
	case *hclsyntax.ObjectConsExpr:
		seen := map[string]bool{}
		for _, item := range x.Items {
			key, err := literalKey(item.KeyExpr, filename, path)
			if err != nil {
				return err
			}
			child := path + "[" + strconv.Quote(key) + "]"
			if seen[key] {
				return &ParseError{Pos: posString(item.KeyExpr.Range().Start, filename), Path: child, Message: "duplicate key"}
			}
			seen[key] = true
			if err := checkLiteral(item.ValueExpr, filename, child); err != nil {
				return err
			}
		}
		return nil
	}
	return notLiteral()
}

func literalKey(e hclsyntax.Expression, filename, path string) (string, error) {
	bad := &ParseError{Pos: posString(e.Range().Start, filename), Path: path, Message: "object keys must be identifiers or literal strings"}
	k, ok := e.(*hclsyntax.ObjectConsKeyExpr)
	if !ok || k.ForceNonLiteral {
		return "", bad
	}
	switch w := k.Wrapped.(type) {
	case *hclsyntax.ScopeTraversalExpr:
		if len(w.Traversal) != 1 {
			return "", bad
		}
	case *hclsyntax.TemplateExpr, *hclsyntax.LiteralValueExpr:
		if err := checkLiteral(w, filename, path); err != nil {
			return "", bad
		}
	default:
		return "", bad
	}
	v, diags := k.Value(nil)
	if diags.HasErrors() || v.IsNull() || v.Type() != cty.String {
		return "", bad
	}
	return v.AsString(), nil
}

func decodeErr(path, msg string) error { return &ParseError{Path: path, Message: msg} }

func attrPath(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "." + name
}

// decodeAttrs decodes an attribute set into a schema struct.
func decodeAttrs(attrs map[string]cty.Value, rv reflect.Value, path string) error {
	fields := fieldsOf(rv.Type())
	known := make(map[string]bool, len(fields))
	for _, f := range fields {
		known[f.name] = true
	}
	var unknown []string
	for name := range attrs {
		if !known[name] {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return decodeErr(attrPath(path, unknown[0]), "unsupported attribute")
	}
	for _, f := range fields {
		p := attrPath(path, f.name)
		v, present := attrs[f.name]
		if !present || v.IsNull() {
			if !f.optional {
				return decodeErr(p, "required attribute is missing")
			}
			continue
		}
		if err := decodeValue(v, rv.Field(f.index), p); err != nil {
			return err
		}
	}
	return nil
}

func decodeValue(v cty.Value, rv reflect.Value, path string) error {
	if v.IsNull() {
		return decodeErr(path, "null is not allowed here")
	}
	t := v.Type()
	switch rv.Kind() {
	case reflect.Pointer:
		elem := reflect.New(rv.Type().Elem())
		if err := decodeValue(v, elem.Elem(), path); err != nil {
			return err
		}
		rv.Set(elem)
		return nil
	case reflect.String:
		if t != cty.String {
			return decodeErr(path, "must be a string")
		}
		rv.SetString(v.AsString())
		return nil
	case reflect.Bool:
		if t != cty.Bool {
			return decodeErr(path, "must be a boolean")
		}
		rv.SetBool(v.True())
		return nil
	case reflect.Int:
		if t != cty.Number {
			return decodeErr(path, "must be a number")
		}
		bf := v.AsBigFloat()
		if !bf.IsInt() {
			return decodeErr(path, "must be a whole number")
		}
		i, acc := bf.Int64()
		if acc != big.Exact || i < math.MinInt32 || i > math.MaxInt32 {
			return decodeErr(path, "number is out of range")
		}
		rv.SetInt(i)
		return nil
	case reflect.Float64:
		if t != cty.Number {
			return decodeErr(path, "must be a number")
		}
		f, _ := v.AsBigFloat().Float64()
		if math.IsInf(f, 0) || math.IsNaN(f) {
			return decodeErr(path, "number is out of range")
		}
		rv.SetFloat(f)
		return nil
	case reflect.Slice:
		if !t.IsTupleType() && !t.IsListType() {
			return decodeErr(path, "must be a list")
		}
		n := v.LengthInt()
		out := reflect.MakeSlice(rv.Type(), n, n)
		i := 0
		for it := v.ElementIterator(); it.Next(); i++ {
			_, ev := it.Element()
			if err := decodeValue(ev, out.Index(i), fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
		rv.Set(out)
		return nil
	case reflect.Map:
		if !t.IsObjectType() && !t.IsMapType() {
			return decodeErr(path, "must be a map")
		}
		out := reflect.MakeMapWithSize(rv.Type(), v.LengthInt())
		for it := v.ElementIterator(); it.Next(); {
			kv, ev := it.Element()
			key := kv.AsString()
			elem := reflect.New(rv.Type().Elem()).Elem()
			if err := decodeValue(ev, elem, path+"["+strconv.Quote(key)+"]"); err != nil {
				return err
			}
			out.SetMapIndex(reflect.ValueOf(key), elem)
		}
		rv.Set(out)
		return nil
	case reflect.Struct:
		if !t.IsObjectType() {
			return decodeErr(path, "must be an object")
		}
		attrs := v.AsValueMap()
		if attrs == nil {
			attrs = map[string]cty.Value{}
		}
		return decodeAttrs(attrs, rv, path)
	}
	return errors.New("tfvars: unsupported schema kind " + rv.Kind().String())
}
