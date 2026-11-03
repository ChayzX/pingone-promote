// Package promote merges a neutralized single-environment export into a
// config repository at the resource-block level, keeps environment overlays
// complete, and validates a repository root (ADR 0014, 0015, 0016).
//
// The merge is ADDITIVE and SURGICAL:
//   - only blocks whose addresses are in the export are inserted or
//     replaced; every other block, comment and file keeps its bytes;
//   - a resource missing from a partial export is never deleted; removal is
//     an explicit de-manage operation that emits
//     removed { lifecycle { destroy = false } };
//   - root variables, secrets.tf, imports.tf, overlays and
//     customer_secrets.yml only receive what the written blocks need.
package promote

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Layout names the files of a config repository. The defaults match the
// reference root (terraform/root); a terraformer-generated repository uses
// TerraformerLayout.
type Layout struct {
	ModuleName          string `json:"moduleName"`          // module "<name>" in ModuleFile
	ModuleDir           string `json:"moduleDir"`           // the shared definitions (child module)
	ModuleFile          string `json:"moduleFile"`          // root file with the module call
	VariablesFile       string `json:"variablesFile"`       // root variable declarations
	SecretsFile         string `json:"secretsFile"`         // one sensitive variable per secret
	ImportsFile         string `json:"importsFile"`         // conditional import blocks (ADR 0016)
	OverlayPattern      string `json:"overlayPattern"`      // "{env}.tfvars" relative to the root
	CustomerSecretsFile string `json:"customerSecretsFile"` // GitLab CI secrets: declarations
}

// DefaultLayout is the reference root. Overlays are <env>.tfvars at the
// root, as in the enterprise config repository.
var DefaultLayout = Layout{
	ModuleName:          "davinci",
	ModuleDir:           "definitions",
	ModuleFile:          "main.tf",
	VariablesFile:       "variables.tf",
	SecretsFile:         "secrets.tf",
	ImportsFile:         "imports.tf",
	OverlayPattern:      "{env}.tfvars",
	CustomerSecretsFile: "customer_secrets.yml",
}

// TerraformerLayout matches `pingcli-terraformer export` defaults.
var TerraformerLayout = Layout{
	ModuleName:          "ping-export",
	ModuleDir:           "ping-export-module",
	ModuleFile:          "ping-export-module.tf",
	VariablesFile:       "ping-export-variables.tf",
	SecretsFile:         "secrets.tf",
	ImportsFile:         "ping-export-imports.tf",
	OverlayPattern:      "{env}.tfvars",
	CustomerSecretsFile: "customer_secrets.yml",
}

var (
	envNameRE  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,62}$`)
	safePathRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.\-/]*$`)
	moduleRE   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)
)

// WithDefaults fills empty fields from DefaultLayout.
func (l Layout) WithDefaults() Layout {
	d := DefaultLayout
	set := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	set(&d.ModuleName, l.ModuleName)
	set(&d.ModuleDir, l.ModuleDir)
	set(&d.ModuleFile, l.ModuleFile)
	set(&d.VariablesFile, l.VariablesFile)
	set(&d.SecretsFile, l.SecretsFile)
	set(&d.ImportsFile, l.ImportsFile)
	set(&d.OverlayPattern, l.OverlayPattern)
	set(&d.CustomerSecretsFile, l.CustomerSecretsFile)
	return d
}

func safeRel(p string) bool {
	return safePathRE.MatchString(p) && !strings.Contains(p, "..") && !strings.HasPrefix(p, "/") && !strings.Contains(p, "//")
}

// Validate checks the layout.
func (l Layout) Validate() error {
	if !moduleRE.MatchString(l.ModuleName) {
		return errors.New("layout: module name is invalid")
	}
	for _, p := range []string{l.ModuleDir, l.ModuleFile, l.VariablesFile, l.SecretsFile, l.ImportsFile, l.CustomerSecretsFile} {
		if !safeRel(p) {
			return errors.New("layout: path " + p + " is invalid")
		}
	}
	for _, p := range []string{l.ModuleFile, l.VariablesFile, l.SecretsFile, l.ImportsFile} {
		if path.Dir(p) != "." || !strings.HasSuffix(p, ".tf") {
			return errors.New("layout: root files must be .tf files at the root: " + p)
		}
	}
	if strings.Count(l.OverlayPattern, "{env}") != 1 || !strings.HasSuffix(l.OverlayPattern, ".tfvars") ||
		strings.HasSuffix(l.OverlayPattern, ".auto.tfvars") || !safeRel(strings.ReplaceAll(l.OverlayPattern, "{env}", "x")) {
		return errors.New("layout: overlay pattern must contain {env} once and end in .tfvars (not .auto.tfvars)")
	}
	return nil
}

// Overlay returns the overlay path for env.
func (l Layout) Overlay(env string) (string, error) {
	if !envNameRE.MatchString(env) {
		return "", errors.New("environment name " + env + " is invalid")
	}
	return strings.ReplaceAll(l.OverlayPattern, "{env}", env), nil
}

// OverlayEnv returns the environment an overlay path belongs to.
func (l Layout) OverlayEnv(p string) (string, bool) {
	pre, post, _ := strings.Cut(l.OverlayPattern, "{env}")
	if !strings.HasPrefix(p, pre) || !strings.HasSuffix(p, post) || len(p) <= len(pre)+len(post) {
		return "", false
	}
	env := p[len(pre) : len(p)-len(post)]
	if !envNameRE.MatchString(env) || strings.HasSuffix(p, ".auto.tfvars") {
		return "", false
	}
	return env, true
}

// ModuleAddress is the root-relative address of a definitions address.
func (l Layout) ModuleAddress(addr string) string { return "module." + l.ModuleName + "." + addr }

// Repo reads a repository snapshot.
type Repo interface {
	// Read returns a file's content; ok is false when it does not exist.
	Read(p string) (content []byte, ok bool, err error)
	// List returns the slash paths of regular files directly in dir
	// ("." is the root), sorted.
	List(dir string) ([]string, error)
}

// DirRepo is a working tree on disk.
type DirRepo string

// Read implements Repo.
func (d DirRepo) Read(p string) ([]byte, bool, error) {
	if !safeRel(p) {
		return nil, false, errors.New("unsafe path " + p)
	}
	full := filepath.Join(string(d), filepath.FromSlash(p))
	fi, err := os.Lstat(full)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !fi.Mode().IsRegular() {
		return nil, false, errors.New(p + " is not a regular file")
	}
	if fi.Size() > 64<<20 {
		return nil, false, errors.New(p + " exceeds the size limit")
	}
	b, err := os.ReadFile(full)
	return b, err == nil, err
}

// List implements Repo.
func (d DirRepo) List(dir string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(string(d), filepath.FromSlash(dir)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.Type().IsRegular() {
			out = append(out, path.Join(dir, e.Name()))
		}
	}
	sort.Strings(out)
	return out, nil
}

// MapRepo is an in-memory snapshot.
type MapRepo map[string][]byte

// Read implements Repo.
func (m MapRepo) Read(p string) ([]byte, bool, error) {
	b, ok := m[p]
	return b, ok, nil
}

// List implements Repo.
func (m MapRepo) List(dir string) ([]string, error) {
	var out []string
	for p := range m {
		if path.Dir(p) == path.Clean(dir) {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out, nil
}

// overlayRepo layers pending writes and deletions over a base snapshot.
type overlayRepo struct {
	base    Repo
	writes  map[string][]byte
	deleted map[string]bool
}

func newView(base Repo) *overlayRepo {
	return &overlayRepo{base: base, writes: map[string][]byte{}, deleted: map[string]bool{}}
}

func (o *overlayRepo) Read(p string) ([]byte, bool, error) {
	if o.deleted[p] {
		return nil, false, nil
	}
	if b, ok := o.writes[p]; ok {
		return b, true, nil
	}
	return o.base.Read(p)
}

func (o *overlayRepo) List(dir string) ([]string, error) {
	base, err := o.base.List(dir)
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, p := range base {
		if !o.deleted[p] {
			set[p] = true
		}
	}
	for p := range o.writes {
		if path.Dir(p) == path.Clean(dir) {
			set[p] = true
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

func (o *overlayRepo) write(p string, b []byte) {
	delete(o.deleted, p)
	o.writes[p] = b
}
