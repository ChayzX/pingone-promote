package exporter

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
	"pingone-promote/internal/resourcecatalog"
)

type referenceIndex struct {
	ids       map[string][]Resource
	addresses map[string]bool
}

func newReferenceIndex(rs []Resource) *referenceIndex {
	idx := &referenceIndex{ids: map[string][]Resource{}, addresses: map[string]bool{}}
	for _, r := range rs {
		idx.addresses[r.String()] = true
		if r.ID != "" {
			idx.ids[r.ID] = append(idx.ids[r.ID], r)
		}
	}
	return idx
}

var camelBoundary = regexp.MustCompile(`([a-z0-9])([A-Z])`)

func referenceField(s string) string {
	return strings.ToLower(camelBoundary.ReplaceAllString(s, "${1}_${2}"))
}
func idField(field string) bool {
	f := referenceField(field)
	return f == "scopes" || f == "id" || strings.HasSuffix(f, "_id") || strings.HasSuffix(f, "_ids")
}
func environmentField(field string) bool {
	f := referenceField(field)
	return f == "environment_id" || f == "environment_ids" || f == "env_id" || f == "env_ids" || f == "environment"
}
func (idx *referenceIndex) resolve(id, field, owner string) (string, error) {
	candidates := idx.ids[id]
	if len(candidates) == 0 {
		return "", nil
	}
	hint := referenceField(field)
	hint = strings.TrimSuffix(strings.TrimSuffix(hint, "_ids"), "_id")
	hint = strings.TrimPrefix(hint, "scope_")
	aliases := map[string]string{"default_authentication_policy": "mfa_device_policy", "authentication_policy": "mfa_device_policy", "parent": strings.TrimPrefix(owner, "pingone_"), "flow": "davinci_flow", "connector": "davinci_connector_instance", "connection": "davinci_connector_instance", "population": "population", "custom_resource": "resource", "scope": "resource_scope", "scopes": "resource_scope", "applications": "application", "client": "application", "theme": "branding_theme", "branding_theme": "branding_theme", "flow_policy": "davinci_application_flow_policy", "sub_flow": "davinci_flow"}
	if v, ok := aliases[hint]; ok {
		hint = v
	}
	if hint != "id" && hint != "target" && hint != "" {
		var exact []Resource
		for _, r := range candidates {
			if r.Type == "pingone_"+hint {
				exact = append(exact, r)
			}
		}
		if len(exact) > 0 {
			candidates = exact
		} else {
			var partial []Resource
			for _, r := range candidates {
				if strings.TrimPrefix(r.Type, "pingone_") == hint {
					partial = append(partial, r)
				}
			}
			if len(partial) > 0 {
				candidates = partial
			}
		}
	}
	if len(candidates) != 1 {
		return "", fmt.Errorf("exporter: ambiguous source ID in %s reference field %s", owner, field)
	}
	return candidates[0].String(), nil
}
func (idx *referenceIndex) rawDependencies(values map[string]any, owner string) ([]string, error) {
	deps := map[string]bool{}
	var visit func(any, string) error
	visit = func(v any, field string) error {
		if environmentField(field) {
			return nil
		}
		switch x := v.(type) {
		case map[string]any:
			for _, k := range sortedKeys(x) {
				next := k
				if k == "id" {
					next = field
					if !idField(next) {
						next += "_id"
					}
				}
				if err := visit(x[k], next); err != nil {
					return err
				}
				if _, ok := idx.ids[k]; ok {
					a, err := idx.resolve(k, field, owner)
					if err != nil {
						return err
					}
					if a != "" {
						deps[a] = true
					}
				}
			}
		case []any:
			for _, item := range x {
				if err := visit(item, field); err != nil {
					return err
				}
			}
		case string:
			if idField(field) || idx.ids[x] != nil && field == "parent" {
				a, err := idx.resolve(x, field, owner)
				if err != nil {
					return err
				}
				if a != "" {
					deps[a] = true
				}
			} else if strings.HasPrefix(strings.TrimSpace(x), "{") || strings.HasPrefix(strings.TrimSpace(x), "[") {
				var parsed any
				decoder := json.NewDecoder(strings.NewReader(x))
				decoder.UseNumber()
				if json.Valid([]byte(x)) && decoder.Decode(&parsed) == nil {
					return visit(parsed, field)
				}
			}
		}
		return nil
	}
	for _, k := range sortedKeys(values) {
		if k == "id" {
			continue
		}
		if err := visit(values[k], k); err != nil {
			return nil, err
		}
	}
	return sortedSet(deps), nil
}

type graphRewrite struct {
	values                      *hcl.EvalContext
	idx                         *referenceIndex
	selected                    map[string]bool
	excluded                    []string
	deps                        map[string]bool
	vars                        map[string]bool
	sourceValues                map[string]cty.Value
	owner, address, environment string
}

func rewriteGraph(e *Export, catalog []Resource, selected map[string]bool, excluded []string) (*Export, map[string]bool, error) {
	if e == nil {
		return nil, nil, errors.New("exporter: nil export")
	}
	g := &graphRewrite{idx: newReferenceIndex(catalog), selected: selected, excluded: excluded, deps: map[string]bool{}, vars: map[string]bool{}, sourceValues: map[string]cty.Value{}, environment: e.EnvironmentID}
	values, err := graphVariableValues(e)
	if err != nil {
		return nil, nil, err
	}
	g.values = values
	out := cloneGraphExport(e)
	for _, p := range sortedKeys(e.Files) {
		if path.Dir(p) != e.ModuleDir || !strings.HasSuffix(p, ".tf") {
			continue
		}
		f, diags := hclwrite.ParseConfig(e.Files[p], p, hcl.InitialPos)
		if diags.HasErrors() {
			return nil, nil, fmt.Errorf("exporter: invalid HCL in %s", p)
		}
		for _, b := range f.Body().Blocks() {
			if b.Type() != "resource" || len(b.Labels()) != 2 {
				continue
			}
			g.owner = b.Labels()[0]
			g.address = g.owner + "." + b.Labels()[1]
			if err := g.body(b.Body(), ""); err != nil {
				return nil, nil, err
			}
		}
		out.Files[p] = f.Bytes()
	}
	if len(g.vars) > 0 {
		extra := &Export{ModuleDir: e.ModuleDir, ModuleName: e.ModuleName, Files: map[string][]byte{}, EnvLocalVariables: map[string]bool{}}
		vars := hclwrite.NewEmptyFile()
		mod := hclwrite.NewEmptyFile()
		m := mod.Body().AppendNewBlock("module", []string{e.ModuleName})
		for _, name := range sortedSet(g.vars) {
			extra.EnvLocalVariables[name] = true
			b := vars.Body().AppendNewBlock("variable", []string{name})
			b.Body().SetAttributeTraversal("type", hclTraversal("string"))
			b.Body().SetAttributeValue("description", cty.StringVal("Environment-local reference; supply the target environment value."))
			m.Body().SetAttributeTraversal(name, hclTraversal("var", name))
		}
		if len(g.sourceValues) > 0 {
			source := hclwrite.NewEmptyFile()
			for _, name := range sortedKeys(g.sourceValues) {
				source.Body().SetAttributeValue(name, g.sourceValues[name])
			}
			extra.Files[e.ModuleName+"-references.auto.tfvars"] = source.Bytes()
		}
		extra.Files[e.ModuleDir+"/variables.tf"] = vars.Bytes()
		extra.Files[e.ModuleName+"-variables.tf"] = vars.Bytes()
		extra.Files[e.ModuleName+"-module.tf"] = mod.Bytes()
		var err error
		out, err = mergeExports(out, extra)
		if err != nil {
			return nil, nil, err
		}
	}
	return out, g.deps, nil
}
func (g *graphRewrite) body(body *hclwrite.Body, parent string) error {
	for _, field := range sortedKeys(body.Attributes()) {
		a := body.GetAttribute(field)
		for _, v := range a.Expr().Variables() {
			parts := strings.Split(string(v.BuildTokens(nil).Bytes()), ".")
			if len(parts) >= 2 && g.idx.addresses[parts[0]+"."+parts[1]] {
				g.deps[parts[0]+"."+parts[1]] = true
			}
		}
		src := a.Expr().BuildTokens(nil).Bytes()
		expr, diags := hclsyntax.ParseExpression(src, "reference.tf", hcl.InitialPos)
		if diags.HasErrors() {
			return errors.New("exporter: invalid reference expression")
		}
		context := field
		if field == "id" && parent != "" {
			context = parent + "_id"
		}
		var edits []graphEdit
		if err := g.expression(expr, context, &edits); err != nil {
			return err
		}
		if len(edits) > 0 {
			sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
			for _, edit := range edits {
				src = append(append(append([]byte(nil), src[:edit.start]...), edit.bytes...), src[edit.end:]...)
			}
			file, d := hclwrite.ParseConfig(append(append([]byte("value = "), src...), '\n'), "reference.tf", hcl.InitialPos)
			if d.HasErrors() {
				return errors.New("exporter: generated invalid reference expression")
			}
			body.SetAttributeRaw(field, file.Body().GetAttribute("value").Expr().BuildTokens(nil))
		}
	}
	for _, b := range body.Blocks() {
		if err := g.body(b.Body(), b.Type()); err != nil {
			return err
		}
	}
	return nil
}

type graphEdit struct {
	start, end int
	bytes      []byte
}

func (g *graphRewrite) expression(expr hclsyntax.Expression, field string, edits *[]graphEdit) error {
	eval := (*hcl.EvalContext)(nil)
	if idField(field) || environmentField(field) {
		eval = g.values
	}
	v, d := expr.Value(eval)
	if !d.HasErrors() {
		tokens, changed, err := g.value(v, field)
		if err != nil {
			return err
		}
		if changed {
			r := expr.Range()
			*edits = append(*edits, graphEdit{r.Start.Byte, r.End.Byte, tokens.Bytes()})
		}
		return nil
	}
	switch e := expr.(type) {
	case *hclsyntax.ObjectConsExpr:
		for _, item := range e.Items {
			key, d := item.KeyExpr.Value(nil)
			context := field
			if !d.HasErrors() && key.Type() == cty.String {
				if tokens, changed, err := g.mapKey(key.AsString(), field); err != nil {
					return err
				} else if changed {
					r := item.KeyExpr.Range()
					*edits = append(*edits, graphEdit{r.Start.Byte, r.End.Byte, tokens.Bytes()})
				}
				context = key.AsString()
				if context == "id" {
					context = field
					if !idField(context) {
						context += "_id"
					}
				}
				if context == "value" && idField(field) {
					context = field
				}
			}
			if err := g.expression(item.ValueExpr, context, edits); err != nil {
				return err
			}
		}
	case *hclsyntax.TupleConsExpr:
		for _, item := range e.Exprs {
			if err := g.expression(item, field, edits); err != nil {
				return err
			}
		}
	case *hclsyntax.FunctionCallExpr:
		for _, arg := range e.Args {
			if err := g.expression(arg, field, edits); err != nil {
				return err
			}
		}
	case *hclsyntax.ParenthesesExpr:
		return g.expression(e.Expression, field, edits)
	case *hclsyntax.ConditionalExpr:
		if err := g.expression(e.TrueResult, field, edits); err != nil {
			return err
		}
		return g.expression(e.FalseResult, field, edits)
	}
	return nil
}

func (g *graphRewrite) value(v cty.Value, field string) (hclwrite.Tokens, bool, error) {
	if !v.IsKnown() || v.IsNull() {
		return hclwrite.TokensForValue(v), false, nil
	}
	if environmentField(field) {
		if v.Type() == cty.String && v.AsString() == g.environment && g.environment != "" {
			return hclwrite.TokensForTraversal(hclTraversal("var", "pingone_environment_id")), true, nil
		}
		return hclwrite.TokensForValue(v), false, nil
	}
	if v.Type() == cty.String {
		s := v.AsString()
		if idField(field) {
			a, err := g.idx.resolve(s, field, g.owner)
			if err != nil {
				return nil, false, err
			}
			if a != "" {
				g.deps[a] = true
				if g.selected[a] && !matchesAny(g.excluded, a) {
					parts := strings.Split(a, ".")
					return hclwrite.TokensForTraversal(hclTraversal(parts[0], parts[1], "id")), true, nil
				}
				name := excludedReferenceName(a)
				g.vars[name] = true
				return hclwrite.TokensForTraversal(hclTraversal("var", name)), true, nil
			}
			if LooksLikeID(s) {
				name := g.externalID(s, field)
				return hclwrite.TokensForTraversal(hclTraversal("var", name)), true, nil
			}
		}
		if strings.HasPrefix(strings.TrimSpace(s), "{") || strings.HasPrefix(strings.TrimSpace(s), "[") {
			var parsed any
			decoder := json.NewDecoder(strings.NewReader(s))
			decoder.UseNumber()
			if json.Valid([]byte(s)) && decoder.Decode(&parsed) == nil {
				val, err := jsonGraphValue(parsed)
				if err != nil {
					return nil, false, err
				}
				t, changed, err := g.value(val, field)
				if err != nil {
					return nil, false, err
				}
				if changed {
					return hclwrite.TokensForFunctionCall("jsonencode", t), true, nil
				}
			}
		}
		return hclwrite.TokensForValue(v), false, nil
	}
	if v.Type().IsObjectType() || v.Type().IsMapType() {
		attrs := []hclwrite.ObjectAttrTokens{}
		changed := false
		for it := v.ElementIterator(); it.Next(); {
			k, val := it.Element()
			name := k.AsString()
			context := name
			if name == "id" {
				context = field
				if !idField(context) {
					context += "_id"
				}
			}
			if name == "value" && idField(field) {
				context = field
			}
			t, ch, err := g.value(val, context)
			if err != nil {
				return nil, false, err
			}
			kt, keyChanged, err := g.mapKey(name, field)
			if err != nil {
				return nil, false, err
			}
			ch = ch || keyChanged
			attrs = append(attrs, hclwrite.ObjectAttrTokens{Name: kt, Value: t})
			changed = changed || ch
		}
		return hclwrite.TokensForObject(attrs), changed, nil
	}
	if v.CanIterateElements() {
		var items []hclwrite.Tokens
		changed := false
		for it := v.ElementIterator(); it.Next(); {
			_, val := it.Element()
			t, ch, err := g.value(val, field)
			if err != nil {
				return nil, false, err
			}
			items = append(items, t)
			changed = changed || ch
		}
		return hclwrite.TokensForTuple(items), changed, nil
	}
	return hclwrite.TokensForValue(v), false, nil
}
func jsonGraphValue(v any) (cty.Value, error) {
	switch x := v.(type) {
	case nil:
		return cty.NullVal(cty.DynamicPseudoType), nil
	case string:
		return cty.StringVal(x), nil
	case bool:
		return cty.BoolVal(x), nil
	case json.Number:
		return cty.ParseNumberVal(string(x))
	case float64:
		return cty.NumberFloatVal(x), nil
	case []any:
		vs := []cty.Value{}
		for _, item := range x {
			v, e := jsonGraphValue(item)
			if e != nil {
				return cty.NilVal, e
			}
			vs = append(vs, v)
		}
		return cty.TupleVal(vs), nil
	case map[string]any:
		vs := map[string]cty.Value{}
		for k, item := range x {
			v, e := jsonGraphValue(item)
			if e != nil {
				return cty.NilVal, e
			}
			vs[k] = v
		}
		return cty.ObjectVal(vs), nil
	}
	return cty.NilVal, errors.New("exporter: unsupported JSON reference value")
}
func cloneGraphExport(e *Export) *Export {
	o := *e
	o.Files = map[string][]byte{}
	for p, b := range e.Files {
		o.Files[p] = append([]byte(nil), b...)
	}
	o.EnvLocalVariables = map[string]bool{}
	for a, v := range e.EnvLocalVariables {
		o.EnvLocalVariables[a] = v
	}
	o.AdoptOnly = map[string]bool{}
	for a, v := range e.AdoptOnly {
		o.AdoptOnly[a] = v
	}
	return &o
}

// mergeExports combines canonical module files. Equal blocks/values are deduped;
// conflicting resource, variable, import or value definitions fail closed.
func mergeExports(a, b *Export) (*Export, error) {
	if a == nil || b == nil {
		return nil, errors.New("exporter: cannot merge a nil export")
	}
	if a.ModuleDir != b.ModuleDir || a.ModuleName != b.ModuleName || a.EnvironmentID != "" && b.EnvironmentID != "" && a.EnvironmentID != b.EnvironmentID {
		return nil, errors.New("exporter: incompatible export module layouts")
	}
	out := cloneGraphExport(a)
	if out.EnvironmentID == "" {
		out.EnvironmentID = b.EnvironmentID
	}
	out.Findings = append(out.Findings, b.Findings...)
	for k, v := range b.EnvLocalVariables {
		out.EnvLocalVariables[k] = v
	}
	for k, v := range b.AdoptOnly {
		out.AdoptOnly[k] = v
	}
	for _, p := range sortedKeys(b.Files) {
		src := b.Files[p]
		if old, ok := out.Files[p]; ok {
			if bytes.Equal(old, src) {
				continue
			}
			if strings.HasSuffix(p, ".tf") || strings.HasSuffix(p, ".tfvars") {
				f, d := hclwrite.ParseConfig(old, p, hcl.InitialPos)
				if d.HasErrors() {
					return nil, fmt.Errorf("exporter: invalid HCL in %s", p)
				}
				other, d := hclwrite.ParseConfig(src, p, hcl.InitialPos)
				if d.HasErrors() {
					return nil, fmt.Errorf("exporter: invalid HCL in %s", p)
				}
				if err := mergeGraphBodies(f.Body(), other.Body(), p); err != nil {
					return nil, err
				}
				out.Files[p] = f.Bytes()
			} else {
				return nil, fmt.Errorf("exporter: conflicting output file %s", p)
			}
		} else {
			out.Files[p] = append([]byte(nil), src...)
		}
	}
	return out, nil
}
func mergeGraphBodies(dst, src *hclwrite.Body, where string) error {
	for _, name := range sortedKeys(src.Attributes()) {
		a := src.GetAttribute(name)
		if old := dst.GetAttribute(name); old != nil {
			if name == "description" && strings.Contains(where, "variable ") {
				continue
			}
			if !bytes.Equal(hclwrite.Format(old.Expr().BuildTokens(nil).Bytes()), hclwrite.Format(a.Expr().BuildTokens(nil).Bytes())) {
				return fmt.Errorf("exporter: conflicting %s attribute %s", where, name)
			}
		} else {
			dst.SetAttributeRaw(name, a.Expr().BuildTokens(nil))
		}
	}
	for _, block := range src.Blocks() {
		key := graphBlockKey(block)
		var existing *hclwrite.Block
		for _, candidate := range dst.Blocks() {
			if graphBlockKey(candidate) == key {
				existing = candidate
				break
			}
		}
		if existing == nil {
			dst.AppendBlock(block)
			continue
		}
		if bytes.Equal(hclwrite.Format(existing.BuildTokens(nil).Bytes()), hclwrite.Format(block.BuildTokens(nil).Bytes())) {
			continue
		}
		if block.Type() == "module" || block.Type() == "terraform" || block.Type() == "required_providers" || block.Type() == "variable" {
			if block.Type() == "required_providers" {
				normalizeGraphProvider(existing.Body())
				normalizeGraphProvider(block.Body())
			}
			if err := mergeGraphBodies(existing.Body(), block.Body(), where+" "+key); err != nil {
				return err
			}
		} else {
			return fmt.Errorf("exporter: conflicting %s block %s", where, key)
		}
	}
	return nil
}
func normalizeGraphProvider(b *hclwrite.Body) {
	if b.GetAttribute("pingone") != nil {
		b.SetAttributeValue("pingone", cty.ObjectVal(map[string]cty.Value{"source": cty.StringVal("pingidentity/pingone"), "version": cty.StringVal("= " + resourcecatalog.ProviderVersion())}))
	}
}
func graphBlockKey(b *hclwrite.Block) string {
	k := b.Type() + " " + strings.Join(b.Labels(), ".")
	if b.Type() == "validation" {
		sum := sha256.Sum256(hclwrite.Format(b.BuildTokens(nil).Bytes()))
		k += fmt.Sprintf(" %x", sum[:])
	}
	if b.Type() == "import" {
		if a := b.Body().GetAttribute("to"); a != nil {
			k += " " + strings.TrimSpace(string(a.Expr().BuildTokens(nil).Bytes()))
		}
	}
	return k
}

// Match the downstream neutralizer's excluded-reference declaration naming.
func excludedReferenceName(address string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' {
			return r
		}
		return '_'
	}, address+"_id")
}

// Variable values participate only in reference fields, never arbitrary payload
// evaluation. Sensitive declarations are excluded even when an exporter writes
// their source values into its temporary auto.tfvars.
func graphVariableValues(e *Export) (*hcl.EvalContext, error) {
	sensitive := map[string]bool{}
	for p, src := range e.Files {
		if path.Dir(p) != e.ModuleDir || !strings.HasSuffix(p, ".tf") {
			continue
		}
		f, d := hclsyntax.ParseConfig(src, p, hcl.InitialPos)
		if d.HasErrors() {
			return nil, errors.New("exporter: invalid graph variable declaration")
		}
		for _, b := range f.Body.(*hclsyntax.Body).Blocks {
			if b.Type != "variable" || len(b.Labels) != 1 {
				continue
			}
			if a := b.Body.Attributes["sensitive"]; a != nil {
				v, d := a.Expr.Value(nil)
				if !d.HasErrors() && v.Type() == cty.Bool && v.True() {
					sensitive[b.Labels[0]] = true
				}
			}
		}
	}
	values := map[string]cty.Value{}
	for _, p := range sortedKeys(e.Files) {
		if path.Dir(p) != "." || !strings.HasSuffix(p, ".auto.tfvars") {
			continue
		}
		f, d := hclsyntax.ParseConfig(e.Files[p], p, hcl.InitialPos)
		if d.HasErrors() {
			return nil, errors.New("exporter: invalid graph variable values")
		}
		for name, a := range f.Body.(*hclsyntax.Body).Attributes {
			if sensitive[name] {
				continue
			}
			v, d := a.Expr.Value(nil)
			if d.HasErrors() {
				return nil, errors.New("exporter: graph variable value must be literal")
			}
			if old, ok := values[name]; ok && !old.RawEquals(v) {
				return nil, errors.New("exporter: conflicting graph variable values")
			}
			values[name] = v
		}
	}
	v := cty.EmptyObjectVal
	if len(values) > 0 {
		v = cty.ObjectVal(values)
	}
	return &hcl.EvalContext{Variables: map[string]cty.Value{"var": v}}, nil
}

func (g *graphRewrite) externalID(id, field string) string {
	sum := sha256.Sum256([]byte(g.address + "/" + field + "/" + id))
	name := safeVariableName(g.address + "_" + field + fmt.Sprintf("_%x", sum[:4]))
	g.vars[name] = true
	g.sourceValues[name] = cty.StringVal(id)
	return name
}
func (g *graphRewrite) mapKey(key, field string) (hclwrite.Tokens, bool, error) {
	var tokens hclwrite.Tokens
	if g.idx.ids[key] != nil {
		a, err := g.idx.resolve(key, field, g.owner)
		if err != nil {
			return nil, false, err
		}
		g.deps[a] = true
		if g.selected[a] && !matchesAny(g.excluded, a) {
			parts := strings.Split(a, ".")
			tokens = hclwrite.TokensForTraversal(hclTraversal(parts[0], parts[1], "id"))
		} else {
			name := excludedReferenceName(a)
			g.vars[name] = true
			tokens = hclwrite.TokensForTraversal(hclTraversal("var", name))
		}
	} else if LooksLikeID(key) {
		name := g.externalID(key, field)
		tokens = hclwrite.TokensForTraversal(hclTraversal("var", name))
	} else {
		return hclwrite.TokensForValue(cty.StringVal(key)), false, nil
	}
	out := append(hclwrite.Tokens{&hclwrite.Token{Type: hclsyntax.TokenOParen, Bytes: []byte("(")}}, tokens...)
	return append(out, &hclwrite.Token{Type: hclsyntax.TokenCParen, Bytes: []byte(")")}), true, nil
}
