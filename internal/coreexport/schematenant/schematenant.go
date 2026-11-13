// Package schematenant generates a PingOne tenant from the provider schema
// for tests: every type the catalog exports through the API, every
// configurable attribute filled at every nesting level.
package schematenant

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"pingone-promote/internal/catalog"
	"pingone-promote/internal/coreexport"
	"pingone-promote/internal/pingone"
)

// Tenant is a PingOne reader that answers every API path the catalog
// reads with objects generated from the provider schema: every configurable
// attribute filled, at every nesting level, in each variant the catalog
// tells apart (hoisted protocol options, built-in or not, the parent types
// children are read under). It stands in for tenants whose data our hand
// written fixture does not cover.
type Tenant struct {
	mu     sync.Mutex
	defs   []*catalog.Def
	pats   map[string]*regexp.Regexp
	cache  map[string][]map[string]any
	refIDs map[string]string // type -> ID of its first top-level object
	reads  int
	// Missing are request paths no definition answers.
	Missing map[string]bool
}

// New returns a generated tenant.
func New() *Tenant {
	st := &Tenant{pats: map[string]*regexp.Regexp{}, cache: map[string][]map[string]any{}, refIDs: map[string]string{}, Missing: map[string]bool{}}
	ph := regexp.MustCompile(`\\\{[a-z]+\\\}`) // QuoteMeta escapes the braces
	for _, d := range catalog.All() {
		if d.Exporter != catalog.ExporterAPI || d.Path == "" {
			continue
		}
		st.defs = append(st.defs, d)
		st.pats[d.Type] = regexp.MustCompile("^" + ph.ReplaceAllString(regexp.QuoteMeta(d.Path), `[^/]+`) + "$")
	}
	// References point at a type's built-in object where it has one (the
	// default population, the openid resource), else at its first object.
	for _, d := range st.defs {
		i := 0
		if d.Builtin != "" {
			i = len(st.variants(d)) - 1
		}
		st.refIDs[d.Type] = ID(d.Type, i, "")
	}
	return st
}

// ID is the generated ID of a type's variant within a scope.
func ID(typ string, variant int, scope string) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s/%d/%s", typ, variant, scope)))
	x := fmt.Sprintf("%x", h[:16])
	return x[0:8] + "-" + x[8:12] + "-4" + x[13:16] + "-8" + x[17:20] + "-" + x[20:32]
}

func (st *Tenant) Get(_ context.Context, _, path string) (map[string]any, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.reads++
	for _, d := range st.defs {
		if (d.Singleton || d.Embedded == "") && st.pats[d.Type].MatchString(path) {
			objs := st.objects(d, path)
			if len(objs) > 0 {
				return objs[0], nil
			}
		}
	}
	st.Missing["GET "+path] = true
	return nil, &pingone.APIError{Status: 404, Path: path}
}

func (st *Tenant) List(_ context.Context, _, path, embedded string) ([]map[string]any, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.reads++
	var out []map[string]any
	found := false
	for _, d := range st.defs {
		if d.Singleton || d.Embedded == "" || !st.pats[d.Type].MatchString(path) {
			continue
		}
		found = true
		out = append(out, st.objects(d, path)...)
	}
	if !found {
		switch path {
		case "templates":
			return []map[string]any{{"id": "general", "name": "general"}}, nil
		case "schemas":
			return []map[string]any{{"id": ID("schema", 0, ""), "name": "User"}}, nil
		}
		st.Missing["LIST "+path] = true
	}
	return out, nil
}

// objects generates the variants of d for one request path (stable across
// requests).
func (st *Tenant) objects(d *catalog.Def, path string) []map[string]any {
	key := d.Type + " " + path
	if objs, ok := st.cache[key]; ok {
		return objs
	}
	attrs, _ := catalog.Schema(d.Type)
	var objs []map[string]any
	for i, v := range st.variants(d) {
		raw := map[string]any{}
		st.fill(d, attrs, raw, "", v.hoist)
		// Derived child types read their attributes from this object, and
		// types sharing its collection read the same objects.
		for _, c := range catalog.All() {
			if c.Exporter == catalog.ExporterAPI && c.Type != d.Type && ((c.Path == "" && c.Parent == d.Type) || (c.Path == d.Path && c.Parent == d.Parent)) {
				ca, _ := catalog.Schema(c.Type)
				st.fill(c, ca, raw, "", "")
			}
		}
		for f, val := range v.set {
			setPath(raw, f, val)
		}
		scope := ""
		if strings.Contains(d.Path, "{") {
			scope = path
		}
		raw["id"] = ID(d.Type, i, scope)
		if !coreexport.Cond(d.Match, raw) {
			continue
		}
		// Copy through JSON so the object has the shape the client returns.
		b, _ := json.Marshal(raw)
		dec := json.NewDecoder(strings.NewReader(string(b)))
		dec.UseNumber()
		var m map[string]any
		_ = dec.Decode(&m)
		objs = append(objs, m)
	}
	st.cache[key] = objs
	return objs
}

type variant struct {
	set   map[string]any
	hoist string // the hoisted attribute this variant fills ("" for none)
}

// variants are the field settings that make the catalog treat objects of a
// type differently.
func (st *Tenant) variants(d *catalog.Def) []variant {
	base := map[string]any{}
	if d.Match != "" {
		f, v := satisfy(d.Match, true)
		base[f] = v
	}
	var out []variant
	if len(d.Hoist) > 0 {
		for _, h := range sortedKeysS(d.Hoist) {
			set := clone(base)
			f, v := satisfy(d.Hoist[h], true)
			set[f] = v
			out = append(out, variant{set: set, hoist: h})
		}
	} else {
		out = append(out, variant{set: base})
	}
	// Children read only under some parents: one variant per value.
	for _, c := range catalog.All() {
		if c.Parent == d.Type && c.Path == "" && c.Match != "" {
			// A derived child kept only for some parents (the default theme).
			set := clone(base)
			f, v := satisfy(c.Match, true)
			set[f] = v
			out = append(out, variant{set: set})
		}
		if c.Parent != d.Type || c.ParentMatch == "" {
			continue
		}
		f, vals := splitCond(c.ParentMatch)
		for _, val := range vals {
			set := clone(base)
			set[f] = typedVal(val)
			out = append(out, variant{set: set})
		}
	}
	if d.Builtin != "" {
		set := clone(base)
		f, v := satisfy(d.Builtin, true)
		set[f] = v
		out = append(out, variant{set: set})
		// And make sure the plain variants are not accidentally built in.
		for i := range out[:len(out)-1] {
			if _, ok := out[i].set[f]; !ok {
				fv, nv := satisfy(d.Builtin, false)
				out[i].set[fv] = nv
			}
		}
	}
	return out
}

func clone(m map[string]any) map[string]any {
	o := map[string]any{}
	for k, v := range m {
		o[k] = v
	}
	return o
}

func splitCond(c string) (string, []string) {
	f, vals, _ := strings.Cut(strings.Replace(c, "!=", "=", 1), "=")
	return f, strings.Split(vals, "|")
}

// satisfy returns a field setting that makes condition c evaluate to want.
func satisfy(c string, want bool) (string, any) {
	f, vals := splitCond(c)
	neg := strings.Contains(c, "!=")
	if want != neg {
		return f, typedVal(vals[0])
	}
	if vals[0] == "true" || vals[0] == "false" {
		return f, vals[0] != "true"
	}
	return f, "FUZZ_OTHER"
}

func typedVal(s string) any {
	switch s {
	case "true":
		return true
	case "false":
		return false
	}
	return s
}

func setPath(m map[string]any, path string, v any) {
	parts := strings.Split(path, ".")
	for _, p := range parts[:len(parts)-1] {
		n, ok := m[p].(map[string]any)
		if !ok {
			n = map[string]any{}
			m[p] = n
		}
		m = n
	}
	m[parts[len(parts)-1]] = v
}

// fill writes every configurable, non-secret attribute of one schema level
// into dst under its API field name.
func (st *Tenant) fill(d *catalog.Def, schema map[string]catalog.Attr, dst map[string]any, path, hoist string) {
	for _, n := range sortedAttrNames(schema) {
		a := schema[n]
		full := path + n
		if !a.Configurable() || a.Secret() || (path == "" && (n == "id" || n == "environment_id")) {
			continue
		}
		if _, ok := d.Hoist[full]; ok {
			if full == hoist {
				st.fill(d, a.Attrs, dst, full+".", "")
			}
			continue
		}
		key := camelName(n)
		if rn, ok := d.Rename[full]; ok {
			if rn == "-" {
				continue
			}
			key = rn
		}
		if _, ok := lookupPath(dst, key); ok {
			continue // a sibling (a hoisted option or a derived child) set it
		}
		setPath(dst, key, st.gen(d, a, n, full))
	}
}

func lookupPath(m map[string]any, path string) (any, bool) {
	var cur any = m
	for _, p := range strings.Split(path, ".") {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = mm[p]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

func (st *Tenant) gen(d *catalog.Def, a catalog.Attr, name, full string) any {
	if a.Nesting != "" {
		obj := map[string]any{}
		st.fill(d, a.Attrs, obj, full+".", "")
		switch a.Nesting {
		case "list", "set":
			return []any{obj}
		case "map":
			return map[string]any{"k": obj}
		}
		return obj
	}
	return st.genType(a.Type, name)
}

func (st *Tenant) genType(t json.RawMessage, name string) any {
	var simple string
	if json.Unmarshal(t, &simple) == nil {
		switch simple {
		case "string":
			if name == "id" {
				return ID("nested", 0, name)
			}
			if base, ok := strings.CutSuffix(name, "_id"); ok {
				if id, ok := st.refIDs["pingone_"+base]; ok {
					return id
				}
				return ID("unknown", 0, name)
			}
			return "v-" + name
		case "number":
			return 1
		case "bool":
			return true
		}
		return "v-" + name
	}
	var complex []json.RawMessage
	_ = json.Unmarshal(t, &complex)
	var kind string
	_ = json.Unmarshal(complex[0], &kind)
	switch kind {
	case "list", "set":
		return []any{st.genType(complex[1], strings.TrimSuffix(name, "s"))}
	case "map":
		return map[string]any{"k": st.genType(complex[1], name)}
	case "object":
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(complex[1], &fields)
		o := map[string]any{}
		for f, ft := range fields {
			o[camelName(f)] = st.genType(ft, f)
		}
		return o
	}
	return "v"
}

func camelName(s string) string {
	parts := strings.Split(s, "_")
	for i := 1; i < len(parts); i++ {
		if parts[i] != "" {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}

func sortedAttrNames(m map[string]catalog.Attr) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeysS(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// structural are terraform validate summaries that mean the generated
// configuration is wrong, whatever the values. Provider value validators
// (enums, conflicting options) also fire on generated values; those are
// counted, not failed.
var structural = regexp.MustCompile(`(?i)unsupported argument|unsupported block type|incorrect attribute value type|missing required argument|insufficient .* blocks|too many .* blocks|invalid reference|undeclared|duplicate|argument or block definition required|invalid expression|missing item separator|unsupported attribute|invalid character|unclosed|extraneous|attribute redefined|missing newline|invalid multi-line|invalid block definition|inconsistent|cycle`)

type tfDiag struct {
	Severity string `json:"severity"`
	Summary  string `json:"summary"`
	Detail   string `json:"detail"`
	Range    *struct {
		Filename string `json:"filename"`
		Start    struct {
			Line int `json:"line"`
		} `json:"start"`
	} `json:"range"`
}

// Validate runs terraform init and validate in dir with the binary and
// plugin directory named by PINGONE_PROMOTE_TERRAFORM and
// PINGONE_PROMOTE_PLUGIN_DIR (nil when unset).
func Validate(t testing.TB, dir string) []tfDiag {
	t.Helper()
	bin := os.Getenv("PINGONE_PROMOTE_TERRAFORM")
	if bin == "" {
		return nil
	}
	run := func(args ...string) ([]byte, error) {
		cmd := exec.Command(bin, args...)
		cmd.Dir = dir
		cmd.Env = append(tfEnv(t.TempDir()), "TF_DATA_DIR="+filepath.Join(dir, ".terraform"))
		return cmd.CombinedOutput()
	}
	initArgs := []string{"init", "-backend=false", "-input=false", "-no-color"}
	if pd := os.Getenv("PINGONE_PROMOTE_PLUGIN_DIR"); pd != "" {
		initArgs = append(initArgs, "-plugin-dir="+pd)
	}
	if out, err := run(initArgs...); err != nil {
		t.Fatalf("terraform init: %v\n%s", err, out)
	}
	out, _ := run("validate", "-json", "-no-color")
	var res struct {
		Diagnostics []tfDiag `json:"diagnostics"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("terraform validate: %v\n%s", err, out)
	}
	return res.Diagnostics
}

// Report fails on structural diagnostics and logs the value ones.
func Report(t testing.TB, dir string, diags []tfDiag) {
	t.Helper()
	other := map[string]int{}
	for _, d := range diags {
		where := ""
		if d.Range != nil {
			where = fmt.Sprintf("%s:%d", d.Range.Filename, d.Range.Start.Line)
			if b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(d.Range.Filename))); err == nil {
				lines := strings.Split(string(b), "\n")
				if l := d.Range.Start.Line; l > 0 && l <= len(lines) {
					where += " `" + strings.TrimSpace(lines[l-1]) + "`"
				}
			}
		}
		if structural.MatchString(d.Summary) {
			t.Errorf("%s: %s: %s\n  %s", where, d.Summary, firstLine(d.Detail), d.Severity)
			continue
		}
		other[d.Summary]++
		if os.Getenv("FUZZ_VERBOSE") != "" {
			t.Logf("value: %s: %s: %s", where, d.Summary, firstLine(d.Detail))
		}
	}
	for s, n := range other {
		t.Logf("value diagnostic (expected for generated values) x%d: %s", n, s)
	}
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}

func tfEnv(home string) []string {
	env := []string{"HOME=" + home, "TF_IN_AUTOMATION=1", "CHECKPOINT_DISABLE=1", "PATH=" + os.Getenv("PATH")}
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "NO_PROXY", "no_proxy", "SSL_CERT_FILE", "TF_CLI_CONFIG_FILE", "TF_PLUGIN_CACHE_DIR", "SYSTEMROOT", "APPDATA", "LOCALAPPDATA", "USERPROFILE"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}
