package exporter

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"

	"pingone-promote/internal/hclblocks"
	"pingone-promote/internal/neutralize"
	"pingone-promote/internal/secretscan"
)

// Slicing cuts selections out of one whole export (with import blocks and
// values) instead of running terraformer again for each: terraformer v0.6.0
// reads the whole environment for every export and filters afterwards, so
// a selection costs as much as the whole. The output has the shape
// terraformer writes for the same options (internal/module/generator.go):
//
//   - a reference to a resource outside the output becomes
//     var.<type>_<label>_id (core.resolveOneReference), declared with the
//     description "ID of <type> resource (excluded from export)" and the
//     source ID as its value; references inside "${...}" templates and
//     depends_on entries are left as they are, as terraformer leaves them;
//   - variables.tf keeps pingone_environment_id and the variables the
//     output uses; import blocks and values cover the output only.

var (
	sliceRefRE   = regexp.MustCompile(`\b(pingone_[a-z_]+)\.(pingcli__[A-Za-z0-9_\-]+)`)
	sliceIDRefRE = regexp.MustCompile(`\b(pingone_[a-z_]+)\.(pingcli__[A-Za-z0-9_\-]+)\.id\b`)
	sliceVarRE   = regexp.MustCompile(`\bvar\.([A-Za-z0-9_]+)`)
	nonVarRE     = regexp.MustCompile(`[^A-Za-z0-9_]`)
	// graphKeyRE ends a line prefix that is a quoted key in a flow graph.
	graphKeyRE = regexp.MustCompile(`"[^"]*"\s*:\s*$`)
)

type sliceBlock struct {
	typ, label string
	text       []byte
}

func (b sliceBlock) addr() string { return b.typ + "." + b.label }

// Source is a whole export to cut selections from.
type Source struct {
	full    *Export
	blocks  []sliceBlock
	byAddr  map[string]sliceBlock
	ids     map[string]string // address -> import ID ("<env>/<id>")
	decls   []hclblocks.Block // module variables.tf blocks, in order
	declSrc []byte
	values  map[string]string // variable -> value expression text
	shared  []string          // value variables declared for several resources
}

// NewSource prepares a whole export (All, Imports and values) for slicing.
func NewSource(full *Export) (*Source, error) {
	s := &Source{full: full, byAddr: map[string]sliceBlock{}, ids: map[string]string{}, values: map[string]string{}}
	var paths []string
	for p := range full.Files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		src := full.Files[p]
		switch {
		case path.Dir(p) == full.ModuleDir && strings.HasPrefix(path.Base(p), "pingone_") && strings.HasSuffix(p, ".tf"):
			bs, err := hclblocks.Scan(src, p)
			if err != nil {
				return nil, err
			}
			for _, b := range bs {
				if b.Type == "resource" && len(b.Labels) == 2 {
					sb := sliceBlock{typ: b.Labels[0], label: b.Labels[1], text: b.Text(src)}
					s.blocks = append(s.blocks, sb)
					s.byAddr[sb.addr()] = sb
				}
			}
		case p == full.ModuleDir+"/variables.tf":
			bs, err := hclblocks.Scan(src, p)
			if err != nil {
				return nil, err
			}
			s.decls, s.declSrc = bs, src
		case p == full.ModuleName+"-imports.tf":
			body, err := hclblocks.Parse(src, p)
			if err != nil {
				return nil, err
			}
			prefix := "module." + full.ModuleName + "."
			for _, b := range body.Blocks {
				to, ok1 := b.Body.Attributes["to"]
				id, ok2 := b.Body.Attributes["id"]
				if b.Type != "import" || !ok1 || !ok2 {
					continue
				}
				r := to.Expr.Range()
				v, d := id.Expr.Value(nil)
				if d.HasErrors() || v.Type() != cty.String || v.IsNull() {
					continue
				}
				s.ids[strings.TrimPrefix(string(src[r.Start.Byte:r.End.Byte]), prefix)] = v.AsString()
			}
		case strings.HasSuffix(p, ".auto.tfvars"):
			// terraformer may set a variable twice (one name in two
			// contexts); the first value is kept, as HCL reads it.
			f, _ := hclsyntax.ParseConfig(src, p, hcl.InitialPos)
			if f == nil {
				return nil, fmt.Errorf("exporter: %s is not readable", p)
			}
			body, ok := f.Body.(*hclsyntax.Body)
			if !ok {
				return nil, fmt.Errorf("exporter: %s is not readable", p)
			}
			for name, a := range body.Attributes {
				r := a.Expr.Range()
				s.values[name] = string(src[r.Start.Byte:r.End.Byte])
			}
		}
	}
	if s.decls == nil {
		return nil, errors.New("exporter: the export has no variables.tf")
	}
	seen := map[string]bool{}
	for _, d := range s.decls {
		if d.Type == "variable" && len(d.Labels) == 1 {
			if seen[d.Labels[0]] && !slices.Contains(s.shared, d.Labels[0]) {
				s.shared = append(s.shared, d.Labels[0])
			}
			seen[d.Labels[0]] = true
		}
	}
	sort.Strings(s.shared)
	return s, nil
}

// Resources lists the export's resources with IDs and exact names.
func (s *Source) Resources() ([]Resource, error) { return resourcesOf(s.full) }

// Whole is WholeExport cut from the source.
func (s *Source) Whole(exclude []string) (*Export, error) {
	sel := map[string]bool{}
	for _, b := range s.blocks {
		if !matchesAny(exclude, b.addr()) {
			sel[b.addr()] = true
		}
	}
	return s.write(sel)
}

// Selected is SelectedExport cut from the source: the seeds and the
// companions of selected flows, nothing they depend on.
func (s *Source) Selected(seeds, exclude []string) (*Export, error) {
	sel, err := s.seeds(seeds, exclude)
	if err != nil {
		return nil, err
	}
	return s.write(sel)
}

// Scoped is ScopedExport cut from the source: the seeds and everything
// they depend on, the companions (deploy, enable) of every flow among
// them, and the DaVinci variables their flows name in templates. The
// closure is complete in one go (terraformer needed up to three runs).
func (s *Source) Scoped(seeds, exclude []string) (*Export, error) {
	sel, err := s.seeds(seeds, exclude)
	if err != nil {
		return nil, err
	}
	queue := make([]string, 0, len(sel))
	for a := range sel {
		queue = append(queue, a)
	}
	add := func(a string) {
		if _, ok := s.byAddr[a]; ok && !sel[a] && !matchesAny(exclude, a) {
			sel[a] = true
			queue = append(queue, a)
		}
	}
	for len(queue) > 0 {
		a := queue[0]
		queue = queue[1:]
		b := s.byAddr[a]
		for _, m := range sliceRefRE.FindAllSubmatch(b.text, -1) {
			add(string(m[1]) + "." + string(m[2]))
		}
		if b.typ == neutralize.TypeFlow {
			for _, c := range Companions(a) {
				add(c)
			}
			for _, m := range globalVarRE.FindAllSubmatch(b.text, -1) {
				add(neutralize.TypeVariable + "." + neutralize.Label(string(m[1]), "flowInstance"))
			}
			for _, m := range companyVarRE.FindAllSubmatch(b.text, -1) {
				add(neutralize.TypeVariable + "." + neutralize.Label(string(m[1]), "company"))
			}
		}
	}
	return s.write(sel)
}

func (s *Source) seeds(seeds, exclude []string) (map[string]bool, error) {
	if len(seeds) == 0 {
		return nil, errors.New("exporter: nothing selected")
	}
	sel := map[string]bool{}
	for _, a := range seeds {
		if !addressRE.MatchString(a) {
			return nil, errors.New("exporter: seeds must be resource addresses")
		}
		if matchesAny(exclude, a) {
			return nil, errors.New("exporter: " + a + " is both selected and excluded")
		}
		if _, ok := s.byAddr[a]; ok {
			sel[a] = true
		}
		if strings.HasPrefix(a, neutralize.TypeFlow+".") {
			for _, c := range Companions(a) {
				if _, ok := s.byAddr[c]; ok && !matchesAny(exclude, c) {
					sel[c] = true
				}
			}
		}
	}
	return sel, nil
}

type sliceFallback struct{ typ, id string }

// write renders the selected resources as terraformer would.
func (s *Source) write(sel map[string]bool) (*Export, error) {
	full := s.full
	files := map[string][]byte{}
	byType := map[string][]string{}
	uses := map[string]int{} // variable -> selected resources using it
	fallbacks := map[string]sliceFallback{}
	var addrs []string
	for _, b := range s.blocks {
		if !sel[b.addr()] {
			continue
		}
		addrs = append(addrs, b.addr())
		text, err := s.rewriteFallbacks(b.text, sel, fallbacks)
		if err != nil {
			return nil, fmt.Errorf("exporter: %s: %w", b.addr(), err)
		}
		byType[b.typ] = append(byType[b.typ], string(text))
		seen := map[string]bool{}
		for _, m := range sliceVarRE.FindAllSubmatch(text, -1) {
			if n := string(m[1]); !seen[n] {
				seen[n] = true
				uses[n]++
			}
		}
	}
	for typ, texts := range byType {
		files[full.ModuleDir+"/"+typ+".tf"] = []byte("\n" + strings.Join(texts, "\n\n\n") + "\n")
	}
	for _, f := range []string{"versions.tf", "outputs.tf"} {
		if b, ok := full.Files[full.ModuleDir+"/"+f]; ok {
			files[full.ModuleDir+"/"+f] = b
		}
	}
	// Declarations: the environment ID, each used variable (as often as
	// resources using it declare it, at most as often as the whole export
	// does), then the fallbacks.
	var decls, rootDecls, inputs, names []string
	kept := map[string]int{}
	for _, b := range s.decls {
		if b.Type != "variable" || len(b.Labels) != 1 {
			continue
		}
		n := b.Labels[0]
		text := string(b.Text(s.declSrc))
		if n == neutralize.EnvironmentIDVariable {
			decls = append([]string{text}, decls...)
			continue
		}
		if kept[n] >= uses[n] || fallbacks[n] != (sliceFallback{}) {
			continue
		}
		kept[n]++
		decls = append(decls, text)
		rootDecls = append(rootDecls, text)
		names = append(names, n)
		if kept[n] == 1 {
			inputs = append(inputs, "  "+n+" = var."+n)
		}
	}
	var fbNames []string
	for n := range fallbacks {
		fbNames = append(fbNames, n)
	}
	sort.Strings(fbNames)
	for _, n := range fbNames {
		d := fmt.Sprintf("variable %q {\n  type        = string\n  description = %q\n}\n", n, "ID of "+fallbacks[n].typ+" resource (excluded from export)")
		decls = append(decls, d)
		rootDecls = append(rootDecls, d)
		names = append(names, n)
		inputs = append(inputs, "  "+n+" = var."+n)
	}
	files[full.ModuleDir+"/variables.tf"] = []byte(strings.Join(decls, "\n\n") + "\n")
	files[full.ModuleName+"-module.tf"] = []byte("module \"" + full.ModuleName + "\" {\n  source = \"./" + full.ModuleDir + "\"\n\n  pingone_environment_id = var.pingone_environment_id\n\n" + strings.Join(inputs, "\n") + "\n\n}\n")
	envDecl := ""
	if len(decls) > 0 {
		envDecl = decls[0]
	}
	files[full.ModuleName+"-variables.tf"] = []byte(envDecl + "\n\n" + strings.Join(rootDecls, "\n\n") + "\n")
	sort.Strings(addrs)
	var comments, blocks strings.Builder
	for _, a := range addrs {
		if id, ok := s.ids[a]; ok {
			to := "module." + full.ModuleName + "." + a
			fmt.Fprintf(&comments, "# terraform import %s %q\n", to, id)
			fmt.Fprintf(&blocks, "import {\n  to = %s\n  id = %q\n}\n\n", to, id)
		}
	}
	files[full.ModuleName+"-imports.tf"] = []byte(comments.String() + "\n" + blocks.String())
	var tv strings.Builder
	tv.WriteString("# Terraform variable values for DaVinci export\n# Generated by pingcli tf export\n\n")
	if v, ok := s.values[neutralize.EnvironmentIDVariable]; ok {
		fmt.Fprintf(&tv, "pingone_environment_id = %s\n\n", v)
	}
	for _, n := range names {
		switch {
		case fallbacks[n] != (sliceFallback{}):
			fmt.Fprintf(&tv, "%s = %q\n", n, fallbacks[n].id)
		case s.values[n] != "":
			fmt.Fprintf(&tv, "%s = %s\n", n, s.values[n])
		}
	}
	files[full.ModuleName+"-terraform.auto.tfvars"] = []byte(tv.String())
	return &Export{EnvironmentID: full.EnvironmentID, ModuleDir: full.ModuleDir, ModuleName: full.ModuleName,
		Files: files, Findings: secretscan.ScanFiles(files), SharedValues: s.shared}, nil
}

// rewriteFallbacks replaces attribute references (<addr>.id) to resources
// outside the selection with their fallback variable. References inside
// "${...}" templates and in a flow graph's values stay resource
// references, as terraformer leaves them.
func (s *Source) rewriteFallbacks(text []byte, sel map[string]bool, out map[string]sliceFallback) ([]byte, error) {
	var b strings.Builder
	last := 0
	for _, m := range sliceIDRefRE.FindAllSubmatchIndex(text, -1) {
		addr := string(text[m[2]:m[3]]) + "." + string(text[m[4]:m[5]])
		inTemplate := m[0] >= 2 && string(text[m[0]-2:m[0]]) == "${"
		// A reference inside a flow's graph ("value" : <ref>, a subflow)
		// is an embedded reference: terraformer resolves it against the
		// whole environment and leaves it a resource reference.
		lineStart := strings.LastIndexByte(string(text[:m[0]]), '\n') + 1
		embedded := graphKeyRE.Match(text[lineStart:m[0]])
		if _, exists := s.byAddr[addr]; !exists || sel[addr] || inTemplate || embedded {
			continue
		}
		name := nonVarRE.ReplaceAllString(addr+"_id", "_")
		id, ok := s.ids[addr]
		if !ok {
			// Its ID variable would have no value in any environment.
			return nil, fmt.Errorf("refers to %s, which the export has no import ID for", addr)
		}
		out[name] = sliceFallback{typ: string(text[m[2]:m[3]]), id: id[strings.LastIndex(id, "/")+1:]}
		b.Write(text[last:m[0]])
		b.WriteString("var." + name)
		last = m[1]
	}
	b.Write(text[last:])
	return []byte(b.String()), nil
}
