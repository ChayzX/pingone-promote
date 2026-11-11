package promote

import (
	"fmt"
	"path"
	"pingone-promote/internal/catalog"
	"sort"
	"strings"

	"github.com/zclconf/go-cty/cty"

	"pingone-promote/internal/hclblocks"
)

type rootVar struct {
	file       string
	hasDefault bool
	sensitive  bool
}

// index describes the Terraform files of a snapshot.
type index struct {
	layout Layout
	// defs maps a definitions address to the file holding it.
	defs map[string]string
	// texts holds each definitions block's text.
	texts        map[string][]byte
	childVars    map[string]bool
	rootVars     map[string]rootVar
	moduleFile   string // file with module "<name>", "" if none
	moduleInputs map[string]bool
	imports      map[string]bool // module addresses with an import block at the root
	rootTF       []string
	defFiles     []string
}

func listTF(r Repo, dir string) ([]string, error) {
	all, err := r.List(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range all {
		if strings.HasSuffix(p, ".tf") {
			out = append(out, p)
		}
	}
	return out, nil
}

func loadIndex(r Repo, l Layout) (*index, error) {
	ix := &index{layout: l, defs: map[string]string{}, texts: map[string][]byte{}, childVars: map[string]bool{},
		rootVars: map[string]rootVar{}, moduleInputs: map[string]bool{}, imports: map[string]bool{}}
	var err error
	if ix.defFiles, err = listTF(r, l.ModuleDir); err != nil {
		return nil, err
	}
	for _, p := range ix.defFiles {
		src, _, err := r.Read(p)
		if err != nil {
			return nil, err
		}
		body, err := hclblocks.Parse(src, p)
		if err != nil {
			return nil, err
		}
		for _, b := range body.Blocks {
			switch {
			case b.Type == "resource" && len(b.Labels) == 2:
				a := b.Labels[0] + "." + b.Labels[1]
				if prev, dup := ix.defs[a]; dup {
					return nil, fmt.Errorf("%s is declared in both %s and %s", a, prev, p)
				}
				ix.defs[a] = p
				rg := b.Range()
				ix.texts[a] = src[rg.Start.Byte:rg.End.Byte]
			case b.Type == "variable" && len(b.Labels) == 1:
				ix.childVars[b.Labels[0]] = true
			case b.Type == "import":
				return nil, fmt.Errorf("%s: literal import blocks do not belong in the shared definitions; use import_ids (ADR 0016)", p)
			}
		}
	}
	if ix.rootTF, err = listTF(r, "."); err != nil {
		return nil, err
	}
	for _, p := range ix.rootTF {
		src, _, err := r.Read(p)
		if err != nil {
			return nil, err
		}
		body, err := hclblocks.Parse(src, p)
		if err != nil {
			return nil, err
		}
		for _, b := range body.Blocks {
			switch {
			case b.Type == "variable" && len(b.Labels) == 1:
				rv := rootVar{file: p}
				_, rv.hasDefault = b.Body.Attributes["default"]
				if a, ok := b.Body.Attributes["sensitive"]; ok {
					if v, d := a.Expr.Value(nil); !d.HasErrors() && v.Type() == cty.Bool && v.True() {
						rv.sensitive = true
					}
				}
				ix.rootVars[b.Labels[0]] = rv
			case b.Type == "module" && len(b.Labels) == 1 && b.Labels[0] == l.ModuleName:
				ix.moduleFile = p
				for k := range b.Body.Attributes {
					ix.moduleInputs[k] = true
				}
			case b.Type == "import":
				if to, ok := b.Body.Attributes["to"]; ok {
					rg := to.Expr.Range()
					ix.imports[string(src[rg.Start.Byte:rg.End.Byte])] = true
				}
			}
		}
	}
	return ix, nil
}

// secretVars are root variables that the pipeline injects: those declared
// in the secrets file, or declared sensitive.
func (ix *index) secretVars() map[string]bool {
	out := map[string]bool{}
	for n, rv := range ix.rootVars {
		if rv.file == ix.layout.SecretsFile || rv.sensitive {
			out[n] = true
		}
	}
	return out
}

// requiredOverlayVars are the non-secret root variables without defaults.
func (ix *index) requiredOverlayVars() []string {
	sec := ix.secretVars()
	var out []string
	for n, rv := range ix.rootVars {
		if !rv.hasDefault && !sec[n] {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func (ix *index) addresses() []string {
	out := make([]string, 0, len(ix.defs))
	for a := range ix.defs {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// overlayFiles lists overlay paths and their environments.
func overlayFiles(r Repo, l Layout) (map[string]string, error) {
	dir := path.Dir(strings.ReplaceAll(l.OverlayPattern, "{env}", "x"))
	all, err := r.List(dir)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, p := range all {
		if env, ok := l.OverlayEnv(p); ok {
			out[env] = p
		}
	}
	return out, nil
}

// importable reports whether the provider supports importing a type
// (every type terraformer exports has an import_id_format).
// importable reports addresses adopted through var.import_ids: every
// pingone type except singletons, whose import ID is the environment ID
// (see singleton).
func importable(addr string) bool {
	typ, _, _ := strings.Cut(addr, ".")
	return strings.HasPrefix(typ, "pingone_") && !singleton(addr)
}

// singleton reports types with one object per environment.
func singleton(addr string) bool {
	typ, _, _ := strings.Cut(addr, ".")
	return catalog.ImportFormat(typ) == catalog.ImportEnvironment
}

// ManagedAddresses lists the resource addresses the repository's shared
// definitions already manage.
func ManagedAddresses(r Repo, l Layout) (map[string]bool, error) {
	l = l.WithDefaults()
	ix, err := loadIndex(r, l)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(ix.defs))
	for a := range ix.defs {
		out[a] = true
	}
	return out, nil
}
