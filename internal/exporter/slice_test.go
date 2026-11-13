package exporter

import (
	"context"
	"fmt"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"pingone-promote/internal/exporter/exportertest"
	"pingone-promote/internal/hclblocks"
)

// normal is the meaning of an export, independent of block order and
// whitespace: resources, declared variables, values and import IDs.
type normal struct {
	resources map[string]string
	// vars are the declared variables a resource uses (terraformer also
	// declares DaVinci form variables nothing in the export uses; a slice
	// does not).
	vars    []string
	values  map[string]string
	imports map[string]string
}

func normalize(t *testing.T, e *Export) normal {
	t.Helper()
	n := normal{resources: map[string]string{}, values: map[string]string{}, imports: map[string]string{}}
	for p, src := range e.Files {
		switch {
		case path.Dir(p) == e.ModuleDir && strings.HasPrefix(path.Base(p), "pingone_"):
			bs, err := hclblocks.Scan(src, p)
			if err != nil {
				t.Fatal(err)
			}
			for _, b := range bs {
				if b.Type == "resource" {
					n.resources[b.Labels[0]+"."+b.Labels[1]] = strings.TrimSpace(string(b.Text(src)))
				}
			}
		case p == e.ModuleDir+"/variables.tf":
			bs, err := hclblocks.Scan(src, p)
			if err != nil {
				t.Fatal(err)
			}
			for _, b := range bs {
				if b.Type == "variable" {
					n.vars = append(n.vars, b.Labels[0])
				}
			}
		case strings.HasSuffix(p, ".auto.tfvars"):
			f, _ := hclsyntax.ParseConfig(src, p, hcl.InitialPos)
			for name, a := range f.Body.(*hclsyntax.Body).Attributes {
				r := a.Expr.Range()
				n.values[name] = string(src[r.Start.Byte:r.End.Byte])
			}
		case p == e.ModuleName+"-imports.tf":
			ids, _, err := ExportIDs(&Export{ModuleName: e.ModuleName, Files: map[string][]byte{p: src}})
			if err != nil {
				t.Fatal(err)
			}
			n.imports = ids
		}
	}
	used := map[string]bool{"pingone_environment_id": true}
	for _, text := range n.resources {
		for _, m := range sliceVarRE.FindAllStringSubmatch(text, -1) {
			used[m[1]] = true
		}
	}
	var vars []string
	for _, v := range n.vars {
		if used[v] {
			vars = append(vars, v)
		}
	}
	n.vars = vars
	for k := range n.values {
		if !used[k] {
			delete(n.values, k)
		}
	}
	sort.Strings(n.vars)
	return n
}

// sameExport reports how a slice differs from terraformer's own output.
// extraVars allows the slice DaVinci variables that its flows name in
// templates ({{global.variables.x}}) and terraformer's limited passes
// left out.
func sameExport(t *testing.T, what string, got, want *Export, extraVars bool) {
	t.Helper()
	g, w := normalize(t, got), normalize(t, want)
	if extraVars {
		for a := range g.resources {
			if _, ok := w.resources[a]; !ok && strings.HasPrefix(a, "pingone_davinci_variable.") && templateNamed(g, a) {
				t.Logf("%s: the slice adds %s, which a flow names in a template", what, a)
				delete(g.resources, a)
				delete(g.imports, a)
			}
		}
		// Variables only those additions used go with them.
		used := map[string]bool{"pingone_environment_id": true}
		for _, text := range g.resources {
			for _, m := range sliceVarRE.FindAllStringSubmatch(text, -1) {
				used[m[1]] = true
			}
		}
		var vars []string
		for _, v := range g.vars {
			if used[v] {
				vars = append(vars, v)
			}
		}
		g.vars = vars
	}
	if len(w.resources) == 0 {
		t.Errorf("%s: terraformer exported nothing; nothing was compared", what)
	}
	var diffs []string
	for a, wt := range w.resources {
		switch gt, ok := g.resources[a]; {
		case !ok:
			diffs = append(diffs, "missing resource "+a)
		case gt != wt:
			diffs = append(diffs, "resource text differs: "+a)
		}
	}
	for a := range g.resources {
		if _, ok := w.resources[a]; !ok {
			diffs = append(diffs, "extra resource "+a)
		}
	}
	if fmt.Sprint(g.vars) != fmt.Sprint(w.vars) {
		diffs = append(diffs, fmt.Sprintf("variables differ:\n  slice %v\n  terraformer %v", g.vars, w.vars))
	}
	for k, wv := range w.values {
		if wv == `""` {
			continue // terraformer writes an empty value where it has none
		}
		if g.values[k] != wv {
			diffs = append(diffs, "value differs: "+k)
		}
	}
	for a, id := range w.imports {
		if g.imports[a] != id {
			diffs = append(diffs, "import differs: "+a)
		}
	}
	if len(g.imports) != len(w.imports) {
		diffs = append(diffs, fmt.Sprintf("%d imports, terraformer %d", len(g.imports), len(w.imports)))
	}
	if len(diffs) > 0 {
		t.Errorf("%s: the slice differs from terraformer:\n  %s", what, strings.Join(diffs, "\n  "))
	}
}

// templateNamed reports whether a flow in n names the variable a in a
// {{global...variables.x}} template or its depends_on.
func templateNamed(n normal, a string) bool {
	for r, text := range n.resources {
		if !strings.HasPrefix(r, "pingone_davinci_flow.") {
			continue
		}
		if strings.Contains(text, a+",") || strings.Contains(text, a+"\n") {
			return true // in the flow's depends_on
		}
		for _, re := range []*regexp.Regexp{globalVarRE, companyVarRE} {
			for _, m := range re.FindAllStringSubmatch(text, -1) {
				if strings.HasPrefix(strings.TrimPrefix(a, "pingone_davinci_variable."), "pingcli__"+m[1]+"_") {
					return true
				}
			}
		}
	}
	return false
}

// compareSlices checks every selection of addrs, alone and with what it
// depends on, against terraformer runs.
func compareSlices(t *testing.T, r *Runner, c Credentials, addrs []string) {
	t.Helper()
	_, src, err := r.CatalogSource(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range addrs {
		want, err := r.SelectedExport(context.Background(), c, []string{a}, nil)
		if err != nil {
			t.Fatal(err)
		}
		got, err := src.Selected([]string{a}, nil)
		if err != nil {
			t.Fatal(err)
		}
		sameExport(t, a+" alone", got, want, false)
		want, err = r.ScopedExport(context.Background(), c, []string{a}, nil)
		if err != nil {
			t.Fatal(err)
		}
		got, err = src.Scoped([]string{a}, nil)
		if err != nil {
			t.Fatal(err)
		}
		sameExport(t, a+" with dependencies", got, want, true)
	}
}

// TestSlicesMatchTheFakeTerraformer: for every resource of the fixtures,
// the slice equals what the fake (which follows terraformer v0.6.0) writes
// for the same selection, and the whole slice with an exclusion equals a
// whole export with it.
func TestSlicesMatchTheFakeTerraformer(t *testing.T) {
	for _, fx := range []string{"dev", "cross"} {
		f := exportertest.Build(t, t.TempDir(), exportertest.FixtureDir(fx))
		r := runner(f)
		list, err := r.ListResources(context.Background(), creds)
		if err != nil {
			t.Fatal(err)
		}
		var addrs []string
		for _, a := range list {
			addrs = append(addrs, a.String())
		}
		compareSlices(t, r, creds, addrs)
		_, src, err := r.CatalogSource(context.Background(), creds)
		if err != nil {
			t.Fatal(err)
		}
		for _, ex := range addrs[:min(3, len(addrs))] {
			want, err := r.WholeExport(context.Background(), creds, []string{ex})
			if err != nil {
				t.Fatal(err)
			}
			got, err := src.Whole([]string{ex})
			if err != nil {
				t.Fatal(err)
			}
			sameExport(t, fx+" whole without "+ex, got, want, false)
		}
	}
}

// TestSlicesMatchTheRealTerraformer runs the same comparison against a real
// environment (read-only) for a sample of items: set PINGONE_PROMOTE_LIVE_ENV
// and PINGONE_PROMOTE_REAL_TERRAFORMER. Each comparison runs terraformer
// twice (about a minute each).
func TestSlicesMatchTheRealTerraformer(t *testing.T) {
	bin := os.Getenv("PINGONE_PROMOTE_REAL_TERRAFORMER")
	if bin == "" || os.Getenv("PINGONE_PROMOTE_LIVE_ENV") == "" {
		t.Skip("set PINGONE_PROMOTE_LIVE_ENV and PINGONE_PROMOTE_REAL_TERRAFORMER to compare against a real environment")
	}
	c := liveCreds(t)
	r := &Runner{Config: Config{Path: bin, Version: "0.6.0"}}
	list, err := r.ListResources(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	// One of each main DaVinci type (deploy and enable come with flows),
	// plus any named in SLICE_EXTRA.
	seen := map[string]bool{"pingone_davinci_flow_deploy": true, "pingone_davinci_flow_enable": true}
	var addrs []string
	for _, a := range list {
		if !seen[a.Type] {
			seen[a.Type] = true
			addrs = append(addrs, a.String())
		}
	}
	if extra := os.Getenv("SLICE_EXTRA"); extra != "" {
		addrs = append(addrs, strings.Split(extra, ",")...)
	}
	if only := os.Getenv("SLICE_ONLY"); only != "" {
		addrs = strings.Split(only, ",")
	}
	t.Logf("comparing %d items", len(addrs))
	compareSlices(t, r, c, addrs)
}
