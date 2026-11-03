// Command faketerraformer imitates pingcli-terraformer v0.6.0 in standalone
// mode for tests. It serves a fixture environment (a terraformer-shaped
// child module plus ids.txt and values.tfvars) and implements the parts of
// the CLI pingone-promote uses: --version, export --list-resources, and
// export --out with --include-resources, --exclude-resources,
// --include-upstream, --include-imports and --include-values.
//
// The output layout follows internal/module/generator.go at v0.6.0:
// <out>/<module-dir>/{versions,variables,outputs}.tf and one <type>.tf per
// resource type, and <out>/<module-name>-{module,variables,imports}.tf and
// <out>/<module-name>-terraform.auto.tfvars.
//
// Build settings (-ldflags -X): main.fixtureDir, main.recordPath,
// main.version.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"pingone-promote/internal/hclblocks"
	"pingone-promote/internal/overlay"
)

var (
	fixtureDir string
	recordPath string
	version    = "0.6.0"
)

type record struct {
	Argv []string `json:"argv"`
	Env  []string `json:"env"`
}

func main() {
	if recordPath != "" {
		if f, err := os.OpenFile(recordPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			_ = json.NewEncoder(f).Encode(record{Argv: os.Args, Env: os.Environ()})
			f.Close()
		}
	}
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Printf("pingcli-terraformer version %s (commit: fake)\n", version)
		return
	}
	if len(os.Args) < 2 || os.Args[1] != "export" {
		fail("subcommand required")
	}
	if err := export(os.Args[2:]); err != nil {
		fail(err.Error())
	}
}

func fail(msg string) {
	fmt.Fprintf(os.Stderr, "Error: %s\n", msg)
	os.Exit(1)
}

type opts struct {
	out, moduleDir, moduleName, format        string
	include, exclude                          []string
	list, upstream, imports, values, skipDeps bool
}

func parse(args []string) (opts, error) {
	o := opts{moduleDir: "ping-export-module", moduleName: "ping-export", format: "hcl"}
	strs := map[string]*string{"--out": &o.out, "-o": &o.out, "--module-dir": &o.moduleDir, "--module-name": &o.moduleName, "--output-format": &o.format}
	slices := map[string]*[]string{"--include-resources": &o.include, "--exclude-resources": &o.exclude}
	bools := map[string]*bool{"--list-resources": &o.list, "--include-upstream": &o.upstream, "--include-imports": &o.imports, "--include-values": &o.values, "--skip-dependencies": &o.skipDeps}
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, val, hasVal := strings.Cut(a, "=")
		switch {
		case bools[name] != nil:
			*bools[name] = true
		case strs[name] != nil || slices[name] != nil:
			if !hasVal {
				if i+1 >= len(args) {
					return o, fmt.Errorf("flag needs an argument: %s", name)
				}
				i++
				val = args[i]
			}
			if strs[name] != nil {
				*strs[name] = val
			} else {
				// pflag StringSlice splits on commas.
				*slices[name] = append(*slices[name], strings.Split(val, ",")...)
			}
		default:
			return o, fmt.Errorf("unknown flag: %s", a)
		}
	}
	return o, nil
}

type block struct {
	typ, label string
	text       []byte
}

var refRE = regexp.MustCompile(`\b(pingone_[a-z_]+)\.(pingcli__[A-Za-z0-9_\-]+)`)

var varRE = regexp.MustCompile(`\bvar\.([A-Za-z0-9_]+)`)

// idRefRE is an attribute reference to another resource's ID.
var idRefRE = regexp.MustCompile(`\b(pingone_[a-z_]+)\.(pingcli__[A-Za-z0-9_\-]+)\.id\b`)

// variableName is utils.SanitizeVariableName.
func variableName(s string) string {
	return regexp.MustCompile(`[^A-Za-z0-9_]`).ReplaceAllString(s, "_")
}

// fallback is a variable terraformer declares for a reference to a resource
// that is not in the export (core.resolveOneReference at v0.6.0: the name is
// SanitizeVariableName(<type>_<label>_<field>), the description "ID of <type>
// resource (excluded from export)", and the original ID is the value written
// with --include-values).
type fallback struct{ name, typ, id string }

// rewriteFallbacks replaces ATTRIBUTE references (core.resolveAttrs) to
// resources outside the output with var.<name>. References inside a
// "${...}" template are the embedded references of core.ResolveEmbeddedReferences
// (a flow's subFlowId); v0.6.0 resolves those against the full graph without
// looking at the excluded set, so they stay resource references. The
// neutralizer repairs them.
func rewriteFallbacks(text []byte, byAddr map[string]block, sel map[string]bool, ids map[string]string, out map[string]fallback) []byte {
	var b bytes.Buffer
	last := 0
	for _, m := range idRefRE.FindAllSubmatchIndex(text, -1) {
		addr := string(text[m[2]:m[3]]) + "." + string(text[m[4]:m[5]])
		inTemplate := m[0] >= 2 && string(text[m[0]-2:m[0]]) == "${"
		if _, exists := byAddr[addr]; !exists || sel[addr] || inTemplate {
			continue
		}
		name := variableName(addr + "_id")
		out[name] = fallback{name: name, typ: string(text[m[2]:m[3]]), id: ids[addr]}
		b.Write(text[last:m[0]])
		b.WriteString("var." + name)
		last = m[1]
	}
	b.Write(text[last:])
	return b.Bytes()
}

func load() ([]block, map[string]string, error) {
	var blocks []block
	files, _ := filepath.Glob(filepath.Join(fixtureDir, "ping-export-module", "pingone_*.tf"))
	sort.Strings(files)
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			return nil, nil, err
		}
		bs, err := hclblocks.Scan(src, f)
		if err != nil {
			return nil, nil, err
		}
		for _, b := range bs {
			if b.Type == "resource" {
				blocks = append(blocks, block{typ: b.Labels[0], label: b.Labels[1], text: b.Text(src)})
			}
		}
	}
	ids := map[string]string{}
	raw, err := os.ReadFile(filepath.Join(fixtureDir, "ids.txt"))
	if err != nil {
		return nil, nil, err
	}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) == 2 {
			ids[f[0]] = f[1]
		}
	}
	return blocks, ids, nil
}

func match(patterns []string, addr string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(strings.ToLower(p), strings.ToLower(addr)); ok {
			return true
		}
	}
	return false
}

func export(args []string) error {
	o, err := parse(args)
	if err != nil {
		return err
	}
	worker := os.Getenv("PINGCLI_PINGONE_ENVIRONMENT_ID")
	if worker == "" {
		return fmt.Errorf("worker environment ID is required: use --pingone-worker-environment-id flag or PINGCLI_PINGONE_ENVIRONMENT_ID env var")
	}
	if os.Getenv("PINGCLI_PINGONE_CLIENT_CREDENTIALS_CLIENT_ID") == "" || os.Getenv("PINGCLI_PINGONE_CLIENT_CREDENTIALS_CLIENT_SECRET") == "" {
		return fmt.Errorf("client ID is required")
	}
	envID := os.Getenv("PINGCLI_PINGONE_EXPORT_ENVIRONMENT_ID")
	if envID == "" {
		envID = worker
	}
	if _, err := os.Stat(filepath.Join(fixtureDir, "fail-duplicate-labels")); err == nil {
		return fmt.Errorf("orchestrator export failed: duplicate resource labels:\n  label \"pingcli__counter_flow\" produced by 2 resources (names: counter, counter, IDs: d1, d2)")
	}
	blocks, ids, err := load()
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Exporting PingOne resources from environment: %s (Region: %s)\n", envID, os.Getenv("PINGCLI_PINGONE_REGION_CODE"))
	if o.list {
		for _, b := range blocks {
			fmt.Printf("%s.%s\n", b.typ, b.label)
		}
		return nil
	}
	if o.out == "" {
		return fmt.Errorf("fake: --out is required")
	}
	if o.format != "hcl" {
		return fmt.Errorf("fake: only hcl")
	}
	byAddr := map[string]block{}
	for _, b := range blocks {
		byAddr[b.typ+"."+b.label] = b
	}
	sel := map[string]bool{}
	var walk func(string)
	walk = func(a string) {
		if sel[a] || match(o.exclude, a) {
			return
		}
		b, ok := byAddr[a]
		if !ok {
			return
		}
		sel[a] = true
		if o.upstream {
			for _, m := range refRE.FindAllSubmatch(b.text, -1) {
				walk(string(m[1]) + "." + string(m[2]))
			}
		}
	}
	for _, b := range blocks {
		a := b.typ + "." + b.label
		if len(o.include) == 0 || match(o.include, a) {
			walk(a)
		}
	}
	return write(o, envID, blocks, byAddr, sel, ids)
}

func write(o opts, envID string, blocks []block, byAddr map[string]block, sel map[string]bool, ids map[string]string) error {
	mod := filepath.Join(o.out, o.moduleDir)
	if err := os.MkdirAll(mod, 0o755); err != nil {
		return err
	}
	byType := map[string][]string{}
	usedVars := map[string]bool{}
	var addrs []string
	fallbacks := map[string]fallback{}
	tailID := map[string]string{}
	for a, id := range ids {
		tailID[a] = id[strings.LastIndex(id, "/")+1:]
	}
	for _, b := range blocks {
		a := b.typ + "." + b.label
		if !sel[a] {
			continue
		}
		addrs = append(addrs, a)
		text := rewriteFallbacks(b.text, byAddr, sel, tailID, fallbacks)
		byType[b.typ] = append(byType[b.typ], string(text))
		for _, m := range varRE.FindAllSubmatch(text, -1) {
			usedVars[string(m[1])] = true
		}
	}
	for typ, texts := range byType {
		content := "\n" + strings.Join(texts, "\n\n\n") + "\n"
		if err := os.WriteFile(filepath.Join(mod, typ+".tf"), []byte(strings.TrimSuffix(content, "\n")), 0o644); err != nil {
			return err
		}
	}
	for _, f := range []string{"versions.tf", "outputs.tf"} {
		b, err := os.ReadFile(filepath.Join(fixtureDir, "ping-export-module", f))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(mod, f), b, 0o644); err != nil {
			return err
		}
	}
	// variables.tf: keep declarations of used variables.
	vsrc, err := os.ReadFile(filepath.Join(fixtureDir, "ping-export-module", "variables.tf"))
	if err != nil {
		return err
	}
	vblocks, err := hclblocks.Scan(vsrc, "variables.tf")
	if err != nil {
		return err
	}
	var decls, rootDecls, inputs []string
	var names []string
	for _, b := range vblocks {
		n := b.Labels[0]
		if n != "pingone_environment_id" && !usedVars[n] {
			continue
		}
		decls = append(decls, string(b.Text(vsrc)))
		if n != "pingone_environment_id" {
			names = append(names, n)
			inputs = append(inputs, "  "+n+" = var."+n)
			rootDecls = append(rootDecls, strings.Replace(string(b.Text(vsrc)), "\n\n  validation", "\n  validation", 1))
		}
	}
	var fbNames []string
	for n := range fallbacks {
		fbNames = append(fbNames, n)
	}
	sort.Strings(fbNames)
	for _, n := range fbNames {
		fb := fallbacks[n]
		decl := fmt.Sprintf("variable %q {\n  type        = string\n  description = %q\n}\n", n, "ID of "+fb.typ+" resource (excluded from export)")
		decls = append(decls, decl)
		names = append(names, n)
		inputs = append(inputs, "  "+n+" = var."+n)
		rootDecls = append(rootDecls, decl)
	}
	if err := os.WriteFile(filepath.Join(mod, "variables.tf"), []byte(strings.Join(decls, "\n\n")+"\n"), 0o644); err != nil {
		return err
	}
	root := func(name, content string) error {
		return os.WriteFile(filepath.Join(o.out, o.moduleName+name), []byte(content), 0o644)
	}
	if err := root("-module.tf", "module \""+o.moduleName+"\" {\n  source = \"./"+o.moduleDir+"\"\n\n  pingone_environment_id = var.pingone_environment_id\n\n"+strings.Join(inputs, "\n")+"\n\n}\n"); err != nil {
		return err
	}
	envDecl := string(vblocks[0].Text(vsrc))
	if err := root("-variables.tf", envDecl+"\n\n"+strings.Join(rootDecls, "\n\n")+"\n"); err != nil {
		return err
	}
	if o.imports {
		var comments, blocksOut strings.Builder
		sort.Strings(addrs)
		for _, a := range addrs {
			if id, ok := ids[a]; ok {
				to := "module." + o.moduleName + "." + a
				fmt.Fprintf(&comments, "# terraform import %s %q\n", to, envID+"/"+id)
				fmt.Fprintf(&blocksOut, "import {\n  to = %s\n  id = %q\n}\n\n", to, envID+"/"+id)
			}
		}
		if err := root("-imports.tf", comments.String()+"\n"+blocksOut.String()); err != nil {
			return err
		}
	}
	// auto.tfvars, as generateTFVarsFile writes it.
	var tv strings.Builder
	tv.WriteString("# Terraform variable values for DaVinci export\n# Generated by pingcli tf export\n\n")
	vals := map[string]string{}
	if raw, err := os.ReadFile(filepath.Join(fixtureDir, "values.tfvars")); err == nil {
		ov, err := overlay.ParseOverlay(raw, "values.tfvars")
		if err != nil {
			return err
		}
		for k, v := range ov.Values {
			vals[k] = v.AsString()
		}
	}
	if o.values {
		fmt.Fprintf(&tv, "pingone_environment_id = %q\n\n", envID)
	} else {
		tv.WriteString("pingone_environment_id = \"\"\n\n")
	}
	for n, fb := range fallbacks {
		vals[n] = fb.id
	}
	sort.Strings(names)
	for _, n := range names {
		v := vals[n]
		if !o.values {
			v = ""
		}
		fmt.Fprintf(&tv, "%s = %q\n", n, v)
	}
	return root("-terraform.auto.tfvars", tv.String())
}
