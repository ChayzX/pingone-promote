// Package neutralize turns a pingcli-terraformer export of ONE source
// environment into environment-neutral shared definitions (the child module
// in definitions/) plus the values that belong to the source environment
// (ADR 0015).
//
// Rules:
//   - Environment-local literals become variables: the source environment ID
//     everywhere (literal occurrences become ${var.pingone_environment_id}),
//     attributes chosen by Lift rules (application redirect and logout URIs by
//     default), connector properties chosen by the operator, and DaVinci
//     variable values (variable-eligible in terraformer already).
//   - Every other connector property variable terraformer emitted is inlined
//     back as a literal, because it is the same in every environment.
//   - Secret variables (sensitive in terraformer's variables.tf) are renamed
//     onto customer_secrets.yml names through SecretNames; terraformer's own
//     names (davinci_variable_<name>_value, davinci_connection_<label>_<prop>)
//     are the default.
//   - Literal import blocks and every *.auto.tfvars file are dropped. An
//     auto.tfvars with secret = "" would override the pipeline's TF_VAR_.
//     Import IDs are kept as an address-to-source-ID map instead.
//   - Label collisions fail closed. Flow-scoped variables that share a name
//     across flows are relabelled with their flow (terraformer's
//     label_fields [name, context] cannot tell them apart).
//   - {{global.variables.x}} references in flows are scanned so a read-only
//     worker suffices; variables the export lacks are reported.
package neutralize

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Terraformer defaults (cmd/export.go in pingcli-plugin-terraformer v0.6.0).
const (
	DefaultExportModuleDir  = "ping-export-module"
	DefaultExportModuleName = "ping-export"
	// LabelPrefix is what utils.SanitizeResourceName prepends.
	LabelPrefix = "pingcli__"
)

// Input is a terraformer output directory held in memory.
type Input struct {
	// Files maps slash-separated paths relative to the output directory
	// to their content.
	Files      map[string][]byte
	ModuleDir  string // default ping-export-module
	ModuleName string // default ping-export
	// Shared are value variables terraformer declares for several
	// resources of the whole source; each user gets its own even when the
	// export has one (exporter.Export.SharedValues).
	Shared []string
	// Values are DaVinci variable values by resource address
	// (exporter.Export.Values), for value variables split per resource.
	Values map[string]string
}

func (in *Input) moduleDir() string {
	if in.ModuleDir == "" {
		return DefaultExportModuleDir
	}
	return in.ModuleDir
}

func (in *Input) moduleName() string {
	if in.ModuleName == "" {
		return DefaultExportModuleName
	}
	return in.ModuleName
}

// LoadDir reads a terraformer output directory (tests and local use).
func LoadDir(dir string) (*Input, error) {
	in := &Input{Files: map[string][]byte{}}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != dir && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s is not a regular file", p)
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		in.Files[filepath.ToSlash(rel)] = b
		return nil
	})
	return in, err
}

// resourceFiles returns the child-module resource files (one per type).
func (in *Input) resourceFiles() []string {
	var out []string
	for p := range in.Files {
		if path.Dir(p) != in.moduleDir() || !strings.HasSuffix(p, ".tf") {
			continue
		}
		switch path.Base(p) {
		case "variables.tf", "versions.tf", "outputs.tf":
			continue
		}
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
