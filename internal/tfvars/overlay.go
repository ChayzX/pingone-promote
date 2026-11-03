package tfvars

// Flat environment overlays (v2): environments/<env>.tfvars holds only
// top-level literal attributes, one per root variable of the config repo:
//
//	pingone_environment_id = "<uuid>"
//	davinci_variable_companyName_value = "Example"
//	import_ids = { "module.davinci.pingone_davinci_flow.pingcli__Login" = "<id>" }
//
// The reader accepts literals only (no references, functions or
// templates). The writer is deterministic and fmt-clean; AppendOverlay adds
// keys as a separate group so existing lines keep their bytes (ADR 0015).

import (
	"bytes"
	"errors"
	"fmt"
	"sort"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// OverlayError is a problem in an overlay file. It never quotes values.
type OverlayError struct {
	File string
	Line int
	Key  string
	Msg  string
}

func (e *OverlayError) Error() string {
	loc := e.File
	if e.Line > 0 {
		loc = fmt.Sprintf("%s:%d", e.File, e.Line)
	}
	if e.Key != "" {
		return loc + ": " + e.Key + ": " + e.Msg
	}
	return loc + ": " + e.Msg
}

// Overlay is a parsed flat overlay.
type Overlay struct {
	Values map[string]cty.Value
	// Lines records each key's line, for messages.
	Lines map[string]int
}

// Keys returns the overlay keys sorted.
func (o *Overlay) Keys() []string {
	out := make([]string, 0, len(o.Values))
	for k := range o.Values {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ParseOverlay parses a flat overlay. Blocks, duplicate keys and
// non-literal expressions are errors.
func ParseOverlay(src []byte, filename string) (*Overlay, error) {
	if len(src) > MaxFileSize {
		return nil, &OverlayError{File: filename, Msg: "exceeds the maximum size"}
	}
	f, diags := hclsyntax.ParseConfig(src, filename, hcl.InitialPos)
	if diags.HasErrors() {
		line := 0
		if diags[0].Subject != nil {
			line = diags[0].Subject.Start.Line
		}
		return nil, &OverlayError{File: filename, Line: line, Msg: "is not valid HCL: " + diags[0].Summary}
	}
	body := f.Body.(*hclsyntax.Body)
	if len(body.Blocks) > 0 {
		return nil, &OverlayError{File: filename, Line: body.Blocks[0].TypeRange.Start.Line, Msg: "blocks are not allowed in an environment overlay"}
	}
	o := &Overlay{Values: map[string]cty.Value{}, Lines: map[string]int{}}
	for name, attr := range body.Attributes {
		line := attr.SrcRange.Start.Line
		if err := literalOnly(attr.Expr); err != nil {
			return nil, &OverlayError{File: filename, Line: line, Key: name, Msg: err.Error()}
		}
		v, d := attr.Expr.Value(nil)
		if d.HasErrors() {
			return nil, &OverlayError{File: filename, Line: line, Key: name, Msg: "is not a literal value"}
		}
		o.Values[name] = v
		o.Lines[name] = line
	}
	return o, nil
}

func literalOnly(e hclsyntax.Expression) error {
	switch x := e.(type) {
	case *hclsyntax.LiteralValueExpr:
		return nil
	case *hclsyntax.TemplateExpr:
		for _, p := range x.Parts {
			if _, ok := p.(*hclsyntax.LiteralValueExpr); !ok {
				return errors.New("templates and interpolation are not allowed")
			}
		}
		return nil
	case *hclsyntax.TupleConsExpr:
		for _, el := range x.Exprs {
			if err := literalOnly(el); err != nil {
				return err
			}
		}
		return nil
	case *hclsyntax.ObjectConsExpr:
		for _, it := range x.Items {
			k, ok := it.KeyExpr.(*hclsyntax.ObjectConsKeyExpr)
			if !ok {
				return errors.New("object keys must be literals")
			}
			if _, isTrav := k.Wrapped.(*hclsyntax.ScopeTraversalExpr); !isTrav {
				if err := literalOnly(k.Wrapped); err != nil {
					return err
				}
			}
			if err := literalOnly(it.ValueExpr); err != nil {
				return err
			}
		}
		return nil
	case *hclsyntax.UnaryOpExpr:
		if x.Op == hclsyntax.OpNegate {
			return literalOnly(x.Val)
		}
	}
	return errors.New("only literal values are allowed (no references or functions)")
}

// overlayOrder puts pingone_environment_id first and import_ids last.
func overlayOrder(keys []string) []string {
	sort.Slice(keys, func(i, j int) bool {
		rank := func(k string) int {
			switch k {
			case "pingone_environment_id":
				return 0
			case "import_ids":
				return 2
			}
			return 1
		}
		if rank(keys[i]) != rank(keys[j]) {
			return rank(keys[i]) < rank(keys[j])
		}
		return keys[i] < keys[j]
	})
	return keys
}

// OverlayHeader is the first line of a generated overlay.
const OverlayHeader = "# Environment values for one environment. Shared definitions live in definitions/."

func renderAttrs(values map[string]cty.Value) ([]byte, error) {
	keys := make([]string, 0, len(values))
	for k := range values {
		if !hclsyntax.ValidIdentifier(k) {
			return nil, &OverlayError{Key: k, Msg: "is not a valid variable name"}
		}
		keys = append(keys, k)
	}
	f := hclwrite.NewEmptyFile()
	for _, k := range overlayOrder(keys) {
		f.Body().SetAttributeValue(k, values[k])
	}
	return hclwrite.Format(f.Bytes()), nil
}

// RenderOverlay renders a complete overlay deterministically.
func RenderOverlay(values map[string]cty.Value) ([]byte, error) {
	body, err := renderAttrs(values)
	if err != nil {
		return nil, err
	}
	return append([]byte(OverlayHeader+"\n\n"), body...), nil
}

// AppendOverlay adds keys that src does not define, as one group at the
// end. Existing bytes are unchanged. A key that already exists is an error:
// changing an existing value is a separate, explicit operation (SetOverlay).
func AppendOverlay(src []byte, filename string, add map[string]cty.Value) ([]byte, error) {
	cur, err := ParseOverlay(src, filename)
	if err != nil {
		return nil, err
	}
	for k := range add {
		if _, ok := cur.Values[k]; ok {
			return nil, &OverlayError{File: filename, Key: k, Msg: "is already set; AppendOverlay only adds keys"}
		}
	}
	if len(add) == 0 {
		return src, nil
	}
	body, err := renderAttrs(add)
	if err != nil {
		return nil, err
	}
	out := append([]byte(nil), src...)
	if len(bytes.TrimSpace(out)) > 0 {
		if !bytes.HasSuffix(out, []byte("\n")) {
			out = append(out, '\n')
		}
		out = append(out, '\n')
	} else {
		out = nil
	}
	out = append(out, body...)
	return out, nil
}

// SetOverlay replaces the values of existing keys and appends new ones.
// Only the replaced attributes' bytes change.
func SetOverlay(src []byte, filename string, set map[string]cty.Value) ([]byte, error) {
	cur, err := ParseOverlay(src, filename)
	if err != nil {
		return nil, err
	}
	f, diags := hclwrite.ParseConfig(src, filename, hcl.InitialPos)
	if diags.HasErrors() {
		return nil, &OverlayError{File: filename, Msg: "is not valid HCL"}
	}
	add := map[string]cty.Value{}
	changed := false
	for k, v := range set {
		if old, ok := cur.Values[k]; ok {
			if old.RawEquals(v) {
				continue
			}
			f.Body().SetAttributeValue(k, v)
			changed = true
		} else {
			add[k] = v
		}
	}
	out := src
	if changed {
		out = hclwrite.Format(f.Bytes())
	}
	return AppendOverlay(out, filename, add)
}
