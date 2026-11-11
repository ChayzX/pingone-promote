package neutralize

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"

	"pingone-promote/internal/hclblocks"
	"pingone-promote/internal/secretscan"
)

// findPath resolves a dot path through attributes, object items and nested
// blocks to the expression it names.
func findPath(body *hclsyntax.Body, parts []string) hclsyntax.Expression {
	if len(parts) == 0 {
		return nil
	}
	if a, ok := body.Attributes[parts[0]]; ok {
		return findInExpr(a.Expr, parts[1:])
	}
	for _, b := range body.Blocks {
		if b.Type == parts[0] && len(b.Labels) == 0 {
			return findPath(b.Body, parts[1:])
		}
	}
	return nil
}

func findInExpr(e hclsyntax.Expression, parts []string) hclsyntax.Expression {
	if len(parts) == 0 {
		return e
	}
	obj, ok := e.(*hclsyntax.ObjectConsExpr)
	if !ok {
		return nil
	}
	for _, it := range obj.Items {
		if k, ok := objectKey(it.KeyExpr); ok && k == parts[0] {
			return findInExpr(it.ValueExpr, parts[1:])
		}
	}
	return nil
}

func typeOf(v cty.Value) string {
	t := v.Type()
	switch {
	case t == cty.String:
		return "string"
	case t == cty.Number:
		return "number"
	case t == cty.Bool:
		return "bool"
	case t.IsTupleType() || t.IsListType():
		all := true
		for it := v.ElementIterator(); it.Next(); {
			_, el := it.Element()
			if el.Type() != cty.String {
				all = false
			}
		}
		if all {
			return "list(string)"
		}
	}
	return "any"
}

// LiftedVariableName is the variable for a lifted attribute:
// <type without "pingone_">_<label without prefix>_<path with "_">.
func LiftedVariableName(resourceType, label, attribute string) string {
	return VariableName(strings.TrimPrefix(resourceType, "pingone_") + "_" + stripPrefix(label) + "_" + strings.ReplaceAll(attribute, ".", "_"))
}

func (s *state) liftAttributes() error {
	for _, rb := range s.raw {
		for _, rule := range s.opts.Lift {
			if rule.ResourceType != rb.typ || (rule.Label != "" && rule.Label != rb.label) {
				continue
			}
			e := findPath(rb.body, strings.Split(rule.Attribute, "."))
			if e == nil {
				continue
			}
			v, diags := e.Value(nil)
			if diags.HasErrors() {
				return fmt.Errorf("neutralize: %s.%s is not a literal and cannot be lifted", rb.addr(), rule.Attribute)
			}
			label := rb.label
			if nl, ok := s.relabel[rb]; ok {
				label = nl
			}
			name := LiftedVariableName(rb.typ, label, rule.Attribute)
			if _, dup := s.res.Variables[name]; dup {
				return &CollisionError{What: "variable", Name: name, Addresses: []string{rb.addr()}, Hint: "a lifted attribute's variable name is already taken"}
			}
			s.res.Variables[name] = &Variable{Name: name, Type: typeOf(v), Kind: KindEnvLocal, Origin: "lift:" + rb.addr() + "." + rule.Attribute,
				Description: "Environment value for " + rule.Attribute + " of " + rb.typ + "." + label}
			s.res.SourceValues[name] = v
			r := e.Range()
			// Ranges in the AST are file offsets; make them block-relative.
			s.lifted[rb] = append(s.lifted[rb], lift{rng: r, name: name})
		}
	}
	return s.liftCoreAttributes()
}

// blockStart is the block's offset in its file, for converting AST ranges.
func blockStart(rb *rawBlock) int { return rb.start }

// templateEscape returns the escaped body of a quoted HCL string.
func templateEscape(s string) []byte {
	q := hclwrite.TokensForValue(cty.StringVal(s)).Bytes()
	return q[1 : len(q)-1]
}

// stringWithEnvID renders a quoted template in which every occurrence of
// the source environment ID becomes ${var.pingone_environment_id}.
func (s *state) stringWithEnvID(v string) []byte {
	parts := strings.Split(v, s.opts.SourceEnvironmentID)
	var b bytes.Buffer
	b.WriteByte('"')
	for i, p := range parts {
		if i > 0 {
			b.WriteString("${var." + EnvironmentIDVariable + "}")
		}
		b.Write(templateEscape(p))
	}
	b.WriteByte('"')
	return b.Bytes()
}

func (s *state) valueTokens(v cty.Value) []byte {
	if v.Type() == cty.String && v.AsString() == s.opts.SourceEnvironmentID {
		return []byte("var." + EnvironmentIDVariable)
	}
	if v.Type() == cty.String && strings.Contains(v.AsString(), s.opts.SourceEnvironmentID) {
		return s.stringWithEnvID(v.AsString())
	}
	return hclwrite.TokensForValue(v).Bytes()
}

func (s *state) rewrite() error {
	for _, rb := range s.raw {
		text, err := s.rewriteBlock(rb)
		if err != nil {
			return fmt.Errorf("neutralize: %s: %w", rb.addr(), err)
		}
		label := rb.label
		if nl, ok := s.relabel[rb]; ok {
			label = nl
		}
		b := Block{Address: rb.typ + "." + label, Type: rb.typ, Label: label, File: rb.typ + ".tf", Text: text}
		if n, ok := literalString(rb.attrs["name"]); ok {
			b.Name = n
		}
		if b.Refs, b.Vars, err = refsOf(text); err != nil {
			return err
		}
		s.res.Blocks = append(s.res.Blocks, b)
	}
	sort.Slice(s.res.Blocks, func(i, j int) bool { return s.res.Blocks[i].Address < s.res.Blocks[j].Address })
	// Source IDs follow renames.
	for _, rn := range s.res.Renames {
		if id, ok := s.res.SourceIDs[rn.From]; ok {
			s.res.SourceIDs[rn.To] = id
		}
	}
	for _, rn := range s.res.Renames {
		if s.res.Block(rn.From) == nil {
			delete(s.res.SourceIDs, rn.From)
		}
	}
	// Drop variables no block uses (for example, inlined ones).
	used := map[string]bool{EnvironmentIDVariable: true}
	for _, b := range s.res.Blocks {
		for _, v := range b.Vars {
			used[v] = true
		}
	}
	for name := range s.res.Variables {
		if !used[name] {
			delete(s.res.Variables, name)
			delete(s.res.SourceValues, name)
		}
	}
	for name := range s.res.Variables {
		if s.res.Variables[name].Kind == KindSecret {
			delete(s.res.SourceValues, name)
		}
	}
	s.reportExcluded()
	return nil
}

// reportExcluded records what became of each excluded resource.
func (s *state) reportExcluded() {
	referenced := map[string]bool{}
	for _, b := range s.res.Blocks {
		for _, r := range b.Refs {
			referenced[r] = true
		}
	}
	for _, e := range s.opts.Excluded {
		ref := ExcludedRef{Address: e.Address, Managed: e.Managed}
		if e.Managed {
			ref.Referenced = referenced[e.Address]
		} else if v, ok := s.res.Variables[ExcludedVariableName(e.Address)]; ok {
			ref.Variable, ref.Referenced = v.Name, true
		}
		s.res.Excluded = append(s.res.Excluded, ref)
	}
	sort.Slice(s.res.Excluded, func(i, j int) bool { return s.res.Excluded[i].Address < s.res.Excluded[j].Address })
}

func (s *state) rewriteBlock(rb *rawBlock) ([]byte, error) {
	toks, diags := hclsyntax.LexConfig(rb.text, "block.tf", hcl.InitialPos)
	if diags.HasErrors() {
		return nil, fmt.Errorf("does not lex")
	}
	base := blockStart(rb)
	var edits []hclblocks.Edit
	var lifted [][2]int
	for _, l := range s.lifted[rb] {
		st, en := l.rng.Start.Byte-base, l.rng.End.Byte-base
		lifted = append(lifted, [2]int{st, en})
		edits = append(edits, hclblocks.Edit{Start: st, End: en, Text: []byte("var." + l.name)})
	}
	inLifted := func(a, b int) bool {
		for _, r := range lifted {
			if a < r[1] && b > r[0] {
				return true
			}
		}
		return false
	}
	// Label.
	if nl, ok := s.relabel[rb]; ok {
		if len(toks) < 7 || toks[5].Type != hclsyntax.TokenQuotedLit {
			return nil, fmt.Errorf("unexpected block header")
		}
		edits = append(edits, hclblocks.Edit{Start: toks[5].Range.Start.Byte, End: toks[5].Range.End.Byte, Text: []byte(nl)})
	}
	envID := []byte(s.opts.SourceEnvironmentID)
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		st, en := t.Range.Start.Byte, t.Range.End.Byte
		switch {
		case isTraversal(toks, i, "var"):
			name := string(toks[i+2].Bytes)
			vEnd := toks[i+2].Range.End.Byte
			if inLifted(st, vEnd) {
				i += 2
				continue
			}
			final := s.finalVarName(rb, name)
			if addr, ok := s.mapBack[name]; ok {
				// The variable stood for a Git-managed excluded resource:
				// point at the managed resource again.
				edits = append(edits, hclblocks.Edit{Start: st, End: vEnd, Text: []byte(addr + ".id")})
			} else if val, ok := s.inline[final]; ok {
				if i+3 < len(toks) && (toks[i+3].Type == hclsyntax.TokenDot || toks[i+3].Type == hclsyntax.TokenOBrack) {
					return nil, fmt.Errorf("var.%s is indexed and cannot be inlined", name)
				}
				switch {
				case i >= 2 && toks[i-1].Type == hclsyntax.TokenTemplateInterp && toks[i-2].Type == hclsyntax.TokenOQuote &&
					i+4 < len(toks) && toks[i+3].Type == hclsyntax.TokenTemplateSeqEnd && toks[i+4].Type == hclsyntax.TokenCQuote:
					edits = append(edits, hclblocks.Edit{Start: toks[i-2].Range.Start.Byte, End: toks[i+4].Range.End.Byte, Text: s.valueTokens(val)})
				case i >= 1 && toks[i-1].Type == hclsyntax.TokenTemplateInterp && i+3 < len(toks) && toks[i+3].Type == hclsyntax.TokenTemplateSeqEnd:
					if val.Type() != cty.String {
						return nil, fmt.Errorf("var.%s is not a string and is used inside a template", name)
					}
					q := s.stringWithEnvID(val.AsString())
					edits = append(edits, hclblocks.Edit{Start: toks[i-1].Range.Start.Byte, End: toks[i+3].Range.End.Byte, Text: q[1 : len(q)-1]})
				default:
					edits = append(edits, hclblocks.Edit{Start: st, End: vEnd, Text: s.valueTokens(val)})
				}
			} else if n, ok := s.rename[final]; ok && n != name {
				edits = append(edits, hclblocks.Edit{Start: toks[i+2].Range.Start.Byte, End: vEnd, Text: []byte(n)})
			} else if final != name {
				edits = append(edits, hclblocks.Edit{Start: toks[i+2].Range.Start.Byte, End: vEnd, Text: []byte(final)})
			}
			i += 2
		case isTraversal(toks, i, "") && strings.HasPrefix(string(t.Bytes), "pingone_"):
			addr := string(t.Bytes) + "." + string(toks[i+2].Bytes)
			if ex, ok := s.excl[addr]; ok && !ex.Managed {
				// A reference terraformer left as a resource reference
				// (an embedded one, such as a subflow inside node
				// properties): v0.6.0 does not look at the excluded set
				// there. The block is not in the export, so use the ID
				// variable.
				if !(i+4 < len(toks) && toks[i+3].Type == hclsyntax.TokenDot && toks[i+4].Type == hclsyntax.TokenIdent && string(toks[i+4].Bytes) == "id") {
					return nil, fmt.Errorf("%s refers to the excluded %s other than by .id; only an ID reference can become a variable", rb.addr(), addr)
				}
				v := s.excludedRef(addr)
				edits = append(edits, hclblocks.Edit{Start: st, End: toks[i+4].Range.End.Byte, Text: []byte("var." + v.Name)})
				i += 4
				continue
			}
			to := s.refRename(rb, addr)
			if to == "" {
				return nil, &CollisionError{What: "label", Name: addr, Addresses: []string{rb.addr()}, Hint: "the reference matches several renamed resources"}
			}
			if to != addr && !inLifted(st, toks[i+2].Range.End.Byte) {
				edits = append(edits, hclblocks.Edit{Start: toks[i+2].Range.Start.Byte, End: toks[i+2].Range.End.Byte, Text: []byte(strings.SplitN(to, ".", 2)[1])})
			}
			i += 2
		case t.Type == hclsyntax.TokenQuotedLit && bytes.Equal(t.Bytes, envID) && i > 0 && i+1 < len(toks) &&
			toks[i-1].Type == hclsyntax.TokenOQuote && toks[i+1].Type == hclsyntax.TokenCQuote && !inLifted(st, en):
			// "<env id>" becomes a bare reference.
			edits = append(edits, hclblocks.Edit{Start: toks[i-1].Range.Start.Byte, End: toks[i+1].Range.End.Byte, Text: []byte("var." + EnvironmentIDVariable)})
			i++
		case (t.Type == hclsyntax.TokenQuotedLit || t.Type == hclsyntax.TokenStringLit) && bytes.Contains(t.Bytes, envID) && !inLifted(st, en):
			repl := bytes.ReplaceAll(t.Bytes, envID, []byte("${var."+EnvironmentIDVariable+"}"))
			edits = append(edits, hclblocks.Edit{Start: st, End: en, Text: repl})
		}
	}
	out, err := hclblocks.Apply(rb.text, edits)
	if err != nil {
		return nil, err
	}
	out = bytes.TrimSpace(hclwrite.Format(out))
	if _, err := hclblocks.Scan(out, rb.addr()); err != nil {
		return nil, fmt.Errorf("rewritten block does not parse: %w", err)
	}
	return out, nil
}

// refsOf returns the resource addresses and variables a block references.
func refsOf(text []byte) (refs, vars []string, err error) {
	toks, diags := hclsyntax.LexConfig(text, "block.tf", hcl.InitialPos)
	if diags.HasErrors() {
		return nil, nil, fmt.Errorf("neutralize: block does not lex")
	}
	rs, vs := map[string]bool{}, map[string]bool{}
	for i := 0; i+2 < len(toks); i++ {
		if !isTraversal(toks, i, "") {
			continue
		}
		root := string(toks[i].Bytes)
		switch {
		case root == "var":
			vs[string(toks[i+2].Bytes)] = true
		case strings.HasPrefix(root, "pingone_"):
			rs[root+"."+string(toks[i+2].Bytes)] = true
		}
	}
	return sortedKeys(rs), sortedKeys(vs), nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// scan runs the {{global.variables.x}} dependency scan and the secret scan
// over the neutralized output.
func (s *state) scan() error {
	have := map[string]bool{}
	for _, b := range s.res.Blocks {
		have[b.Address] = true
	}
	missing := map[string]bool{}
	for i := range s.res.Blocks {
		b := &s.res.Blocks[i]
		for _, r := range b.Refs {
			if !have[r] {
				missing[r] = true
			}
		}
		if b.Type != TypeFlow {
			continue
		}
		names := map[string]bool{}
		for _, m := range globalVarRE.FindAllSubmatch(b.Text, -1) {
			names[string(m[1])] = true
		}
		if len(names) == 0 {
			continue
		}
		s.res.GlobalVariableRefs[b.Address] = sortedKeys(names)
		for n := range names {
			a := TypeVariable + "." + Label(n, "company")
			if !have[a] {
				missing[a] = true
			}
		}
	}
	s.res.MissingDependencies = sortedKeys(missing)
	for _, b := range s.res.Blocks {
		for _, f := range secretscan.ScanText(b.File, b.Text) {
			f.Name = b.Address
			s.res.Findings = append(s.res.Findings, f)
		}
	}
	for _, name := range sortedValueKeys(s.res.SourceValues) {
		v := s.res.SourceValues[name]
		if v.Type() != cty.String {
			continue
		}
		for _, f := range secretscan.ScanText("source values", []byte(v.AsString())) {
			f.Name, f.Line = name, 0
			s.res.Findings = append(s.res.Findings, f)
		}
		if secretscan.SuspectLiteral(name, v.AsString()) {
			s.res.Findings = append(s.res.Findings, secretscan.Finding{File: "source values", Kind: secretscan.KindNamed, Name: name})
		}
	}
	return nil
}

func sortedValueKeys(m map[string]cty.Value) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
