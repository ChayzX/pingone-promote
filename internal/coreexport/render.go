package coreexport

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/hashicorp/hcl/v2/hclwrite"

	"pingone-promote/internal/catalog"
	"pingone-promote/internal/neutralize"
)

// Output file names. They differ from terraformer's so both exports can be
// combined into one input for the neutralizer.
const (
	ImportsFile = "ping-export-core-imports.tf"
	ValuesFile  = "ping-export-core.auto.tfvars"
	// VariablesFile is merged into the module's variables.tf.
	VariablesFile = "variables.tf"
)

// RenderInput selects what to render.
type RenderInput struct {
	Inventory *Inventory
	// Addresses are the core objects to render.
	Addresses []string
	// Exported is every address in the combined export (core and DaVinci):
	// a reference to one of them is a resource reference, any other known
	// ID becomes a per-environment ID variable.
	Exported map[string]bool
	// IDs maps every known ID (core and DaVinci) to its address.
	IDs map[string]string
	// ModuleDir and ModuleName of the export (terraformer's defaults).
	ModuleDir, ModuleName string
}

// Rendered is terraformer-shaped output for the core objects.
type Rendered struct {
	// Files maps export-relative paths to content; variables.tf holds only
	// the variable blocks (the caller merges it with terraformer's).
	Files map[string][]byte
	// Referenced are addresses referenced by a rendered object but not in
	// the export, with their source IDs: they became ID variables named
	// neutralize.ExcludedVariableName(address).
	Referenced map[string]string
	// Adopt lists rendered built-in objects that every environment must
	// adopt by import ID instead of creating.
	Adopt    []string
	Warnings []string
}

type renderer struct {
	in        RenderInput
	out       *Rendered
	vars      map[string]string // name -> declaration
	values    map[string]string // name -> HCL value (source)
	obj       *Object
	def       *catalog.Def
	selfAddrs map[string]bool
}

// Render writes the selected objects.
func Render(in RenderInput) (*Rendered, error) {
	if in.ModuleDir == "" {
		in.ModuleDir = neutralize.DefaultExportModuleDir
	}
	if in.ModuleName == "" {
		in.ModuleName = neutralize.DefaultExportModuleName
	}
	r := &renderer{in: in, out: &Rendered{Files: map[string][]byte{}, Referenced: map[string]string{}},
		vars: map[string]string{}, values: map[string]string{}}
	byType := map[string][]string{}
	var imports strings.Builder
	addrs := append([]string(nil), in.Addresses...)
	sort.Strings(addrs)
	for _, a := range addrs {
		o := in.Inventory.Object(a)
		if o == nil {
			return nil, fmt.Errorf("coreexport: %s is not in the inventory", a)
		}
		d, _ := catalog.Get(o.Type)
		r.obj, r.def = o, d
		text, err := r.block()
		if err != nil {
			return nil, err
		}
		byType[o.Type] = append(byType[o.Type], text)
		if o.Builtin && d.Import != catalog.ImportEnvironment { // imported by environment ID: nothing to enter
			r.out.Adopt = append(r.out.Adopt, a)
		}
		if d.Import != catalog.ImportEnvironment && o.ImportID != "" {
			fmt.Fprintf(&imports, "import {\n  to = module.%s.%s\n  id = %s\n}\n\n", in.ModuleName, a, quote(in.Inventory.EnvironmentID+"/"+o.ImportID))
		}
	}
	for t, blocks := range byType {
		r.out.Files[in.ModuleDir+"/"+t+".tf"] = hclwrite.Format([]byte(strings.Join(blocks, "\n")))
	}
	if imports.Len() > 0 {
		r.out.Files[ImportsFile] = hclwrite.Format([]byte(imports.String()))
	}
	names := make([]string, 0, len(r.vars))
	for n := range r.vars {
		names = append(names, n)
	}
	sort.Strings(names)
	var vb, tv strings.Builder
	for _, n := range names {
		vb.WriteString(r.vars[n] + "\n")
		if v, ok := r.values[n]; ok {
			tv.WriteString(n + " = " + v + "\n")
		}
	}
	if vb.Len() > 0 {
		r.out.Files[in.ModuleDir+"/"+VariablesFile] = hclwrite.Format([]byte(vb.String()))
	}
	if tv.Len() > 0 {
		r.out.Files[ValuesFile] = hclwrite.Format([]byte(tv.String()))
	}
	sort.Strings(r.out.Adopt)
	return r.out, nil
}

func (r *renderer) block() (string, error) {
	o, d := r.obj, r.def
	attrs, ok := catalog.Schema(o.Type)
	if !ok {
		return "", fmt.Errorf("coreexport: no provider schema for %s", o.Type)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "resource %q %q {\n", o.Type, o.Label)
	b.WriteString("  environment_id = var." + neutralize.EnvironmentIDVariable + "\n")
	skip := map[string]bool{"id": true, "environment_id": true, d.ParentAttr: true}
	if d.ParentAttr != "" && o.Parent != nil {
		b.WriteString("  " + d.ParentAttr + " = " + r.idExpr(o.Parent.ID, d.ParentAttr) + "\n")
	}
	if d.ParentValue != "" && o.Parent != nil {
		b.WriteString("  " + d.ParentValue + " = " + quote(o.Parent.ID) + "\n")
		skip[d.ParentValue] = true
	}
	if d.ParentName != "" && o.Parent != nil {
		if n, ok := o.Parent.Raw["name"].(string); ok {
			b.WriteString("  " + d.ParentName + " = " + quote(n) + "\n")
			skip[d.ParentName] = true
		}
	}
	for _, attr := range sortedKeysS(d.Ancestors) {
		for p := o.Parent; p != nil; p = p.Parent {
			if p.Type == d.Ancestors[attr] {
				b.WriteString("  " + attr + " = " + r.idExpr(p.ID, attr) + "\n")
				skip[attr] = true
				break
			}
		}
	}
	body, err := r.attrs(attrs, o.Raw, "", o.Raw, skip)
	if err != nil {
		return "", err
	}
	b.WriteString(body)
	b.WriteString("}\n")
	return b.String(), nil
}

// attrs renders the configurable attributes of one schema level from the
// API object src. path is the schema path prefix ("oidc_options.").
func (r *renderer) attrs(schema map[string]catalog.Attr, src map[string]any, path string, root map[string]any, skip map[string]bool) (string, error) {
	names := make([]string, 0, len(schema))
	for n := range schema {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		a := schema[n]
		full := path + n
		if skip[n] || !a.Configurable() {
			continue
		}
		// A deprecated "<x>_id" next to an object attribute "<x>" that the
		// API fills would set the same thing twice.
		if base, ok := strings.CutSuffix(n, "_id"); ok {
			if _, sib := schema[base]; sib && src[camel(base)] != nil {
				continue
			}
		}
		var v any
		if cond, ok := r.def.Hoist[full]; ok {
			if !Cond(cond, root) {
				continue
			}
			v = src // the nested object is read from the same API object
			if a.Nesting == "list" || a.Nesting == "set" {
				v = []any{src}
			}
		} else if rn, ok := r.def.Rename[full]; ok {
			if rn == "-" {
				continue
			}
			v = Lookup(src, rn)
		} else {
			v = r.find(src, n, path == "")
		}
		if a.Secret() {
			cond, declared := r.def.Secrets[full]
			if v != nil || a.Required || (declared && Cond(cond, root)) {
				b.WriteString("  " + n + " = var." + r.secretVar(full) + "\n")
			}
			continue
		}
		if v == nil {
			continue
		}
		expr, err := r.value(a, v, full, root)
		if err != nil {
			return "", err
		}
		if expr == "" {
			continue
		}
		if a.Block {
			b.WriteString(r.asBlocks(n, a, expr))
			continue
		}
		b.WriteString("  " + n + " = " + expr + "\n")
	}
	return b.String(), nil
}

func (r *renderer) asBlocks(name string, a catalog.Attr, expr string) string {
	// Nested blocks: an object renders as one block, a list as several.
	body := strings.TrimSpace(expr)
	if strings.HasPrefix(body, "[") {
		body = strings.TrimSuffix(strings.TrimPrefix(body, "["), "]")
		var out strings.Builder
		for _, item := range splitTop(body) {
			out.WriteString("  " + name + " " + strings.TrimSpace(item) + "\n")
		}
		return out.String()
	}
	return "  " + name + " " + body + "\n"
}

// splitTop splits "{...}, {...}" at top-level commas.
func splitTop(s string) []string {
	var out []string
	depth, start := 0, 0
	inStr := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inStr:
			if c == '\\' {
				i++
			} else if c == '"' {
				inStr = false
			}
		case c == '"':
			inStr = true
		case c == '{' || c == '[':
			depth++
		case c == '}' || c == ']':
			depth--
		case c == ',' && depth == 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if strings.TrimSpace(s[start:]) != "" {
		out = append(out, s[start:])
	}
	return out
}

// find reads a schema attribute from an API object: the camelCase field,
// then the same name ignoring case and underscores (bindDN for bind_dn),
// then "<x>.id" for an attribute "<x>_id", then the catalog's fallback
// sub-objects at the root level.
func (r *renderer) find(src map[string]any, name string, top bool) any {
	if v, ok := src[camel(name)]; ok {
		return v
	}
	norm := func(s string) string { return strings.ToLower(strings.ReplaceAll(s, "_", "")) }
	for k, v := range src {
		if norm(k) == norm(name) {
			return v
		}
	}
	if base, ok := strings.CutSuffix(name, "_id"); ok {
		if m, ok := src[camel(base)].(map[string]any); ok {
			if id, ok := m["id"]; ok {
				return id
			}
		}
	}
	if top {
		for _, f := range r.def.Fallback {
			if m, ok := src[f].(map[string]any); ok {
				if v := r.find(m, name, false); v != nil {
					return v
				}
			}
		}
	}
	return nil
}

// value renders an API value for a schema attribute ("" omits it).
func (r *renderer) value(a catalog.Attr, v any, path string, root map[string]any) (string, error) {
	if len(a.Attrs) > 0 {
		switch a.Nesting {
		case "single":
			m, ok := v.(map[string]any)
			if !ok {
				return "", nil
			}
			inner, err := r.attrs(a.Attrs, m, path+".", root, nil)
			if err != nil || strings.TrimSpace(inner) == "" {
				return "", err
			}
			return "{\n" + inner + "}", nil
		case "list", "set":
			items, ok := v.([]any)
			if !ok {
				return "", nil
			}
			var parts []string
			for _, it := range items {
				m, ok := it.(map[string]any)
				if !ok {
					continue
				}
				inner, err := r.attrs(a.Attrs, m, path+".", root, nil)
				if err != nil {
					return "", err
				}
				parts = append(parts, "{\n"+inner+"}")
			}
			if a.Nesting == "set" {
				sort.Strings(parts) // sets come back in no stable order
			}
			if len(parts) == 0 {
				// An explicitly empty list is configuration (and may be
				// required); a list of non-objects is not this attribute.
				if len(items) == 0 {
					return "[]", nil
				}
				return "", nil
			}
			return "[\n" + strings.Join(parts, ",\n") + "\n]", nil
		case "map":
			m, ok := v.(map[string]any)
			if !ok {
				return "", nil
			}
			keys := sortedKeys(m)
			var parts []string
			for _, k := range keys {
				im, ok := m[k].(map[string]any)
				if !ok {
					continue
				}
				inner, err := r.attrs(a.Attrs, im, path+".", root, nil)
				if err != nil {
					return "", err
				}
				parts = append(parts, quote(k)+" = {\n"+inner+"}")
			}
			return "{\n" + strings.Join(parts, "\n") + "\n}", nil
		}
	}
	return r.typed(a.Type, v, path)
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// typed renders a value of a Terraform type expression (cty JSON form).
func (r *renderer) typed(t json.RawMessage, v any, path string) (string, error) {
	var simple string
	if json.Unmarshal(t, &simple) == nil {
		switch simple {
		case "string":
			if m, isMap := v.(map[string]any); isMap {
				if id, ok := m["id"].(string); ok {
					v = id // a reference object {"id": ...} where the schema wants the ID
				}
			}
			s, ok := scalar(v)
			if !ok {
				b, _ := json.Marshal(v)
				s = string(b)
			}
			return r.stringExpr(s, path), nil
		case "number":
			switch x := v.(type) {
			case json.Number:
				return x.String(), nil
			case string:
				if f, err := strconv.ParseFloat(x, 64); err == nil && !math.IsInf(f, 0) && !math.IsNaN(f) {
					return x, nil
				}
			}
			return "", nil
		case "bool":
			if b, ok := v.(bool); ok {
				return strconv.FormatBool(b), nil
			}
			return "", nil
		default: // dynamic
			return r.dynamic(v, path), nil
		}
	}
	var complex []json.RawMessage
	if err := json.Unmarshal(t, &complex); err != nil || len(complex) != 2 {
		return "", fmt.Errorf("coreexport: %s: unsupported schema type %s", path, t)
	}
	var kind string
	_ = json.Unmarshal(complex[0], &kind)
	switch kind {
	case "list", "set":
		items, ok := v.([]any)
		if !ok {
			return "", nil
		}
		var parts []string
		for _, it := range items {
			e, err := r.typed(complex[1], it, path)
			if err != nil {
				return "", err
			}
			if e != "" {
				parts = append(parts, e)
			}
		}
		if kind == "set" {
			sort.Strings(parts) // the API returns sets in no stable order
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	case "map":
		m, ok := v.(map[string]any)
		if !ok {
			return "", nil
		}
		var parts []string
		for _, k := range sortedKeys(m) {
			e, err := r.typed(complex[1], m[k], path)
			if err != nil {
				return "", err
			}
			if e != "" {
				parts = append(parts, quote(k)+" = "+e)
			}
		}
		return "{\n" + strings.Join(parts, "\n") + "\n}", nil
	case "object":
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(complex[1], &fields)
		m, ok := v.(map[string]any)
		if !ok {
			return "", nil
		}
		var parts []string
		for _, f := range sortedRaw(fields) {
			fv := m[camel(f)]
			if fv == nil {
				fv = m[f]
			}
			if fv == nil {
				continue
			}
			e, err := r.typed(fields[f], fv, path+"."+f)
			if err != nil {
				return "", err
			}
			if e != "" {
				parts = append(parts, f+" = "+e)
			}
		}
		return "{\n" + strings.Join(parts, "\n") + "\n}", nil
	}
	return r.dynamic(v, path), nil
}

func sortedRaw(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func scalar(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case json.Number:
		return x.String(), true
	case bool:
		return strconv.FormatBool(x), true
	}
	return "", false
}

// dynamic renders any JSON value as HCL.
func (r *renderer) dynamic(v any, path string) string {
	switch x := v.(type) {
	case map[string]any:
		var parts []string
		for _, k := range sortedKeys(x) {
			parts = append(parts, quote(k)+" = "+r.dynamic(x[k], path))
		}
		return "{\n" + strings.Join(parts, "\n") + "\n}"
	case []any:
		var parts []string
		for _, it := range x {
			parts = append(parts, r.dynamic(it, path))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case string:
		return r.stringExpr(x, path)
	case json.Number:
		return x.String()
	case bool:
		return strconv.FormatBool(x)
	}
	return "null"
}

// stringExpr renders a string, turning a known ID into a reference or an
// ID variable. The source environment ID stays literal: the neutralizer
// replaces it everywhere with var.pingone_environment_id.
func (r *renderer) stringExpr(s, path string) string {
	if addr, ok := r.in.IDs[s]; ok && addr != r.obj.Address() {
		return r.idExpr(s, path)
	}
	return quote(s)
}

// idExpr references the object with ID id.
func (r *renderer) idExpr(id, path string) string {
	addr, ok := r.in.IDs[id]
	if !ok {
		return quote(id)
	}
	if r.in.Exported[addr] {
		return addr + ".id"
	}
	name := neutralize.ExcludedVariableName(addr)
	r.out.Referenced[addr] = id
	typ, _, _ := strings.Cut(addr, ".")
	r.vars[name] = fmt.Sprintf("variable %q {\n  type        = string\n  description = %q\n}\n", name, "ID of "+typ+" resource (excluded from export)")
	r.values[name] = quote(id)
	return "var." + name
}

func (r *renderer) secretVar(path string) string {
	name := neutralize.VariableName(strings.TrimPrefix(r.obj.Type, "pingone_") + "_" + strings.TrimPrefix(r.obj.Label, neutralize.LabelPrefix) + "_" + strings.ReplaceAll(path, ".", "_"))
	r.vars[name] = fmt.Sprintf("variable %q {\n  type        = string\n  description = %q\n  sensitive   = true\n}\n", name, path+" of "+r.obj.Address()+" (secret; never exported)")
	return name
}

// quote renders an HCL string literal: JSON escaping plus template escapes.
func quote(s string) string {
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	q := strings.TrimSpace(buf.String())
	q = strings.ReplaceAll(q, "${", "$${")
	q = strings.ReplaceAll(q, "%{", "%%{")
	return q
}

func sortedKeysS(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
