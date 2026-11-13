package promote

import (
	"bytes"
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/zclconf/go-cty/cty"

	"pingone-promote/internal/hclblocks"
	"pingone-promote/internal/neutralize"
	"pingone-promote/internal/overlay"
	"pingone-promote/internal/pipelinesecrets"
)

// Choice is the operator's per-address decision.
type Choice string

const (
	ChoiceUpdate Choice = "update" // use the source version ("Use source")
	ChoiceKeep   Choice = "keep"   // keep the Git version ("Keep Git version")
	// ChoiceExclude leaves the dependency out of the export. The request
	// job passes it to the exporter; a resource that is still in the export
	// is handled like keep.
	ChoiceExclude Choice = "exclude"
)

// Status compares live source, Git head and (optionally) a base commit.
type Status string

const (
	StatusNew             Status = "new"
	StatusChangedInSource Status = "changed-in-source"
	StatusMatches         Status = "matches"
	StatusGitNewer        Status = "git-newer"
	StatusChangedInBoth   Status = "changed-in-both"
	// StatusNotCompared is the status of an excluded resource: it was not
	// exported, so there is nothing to compare.
	StatusNotCompared Status = "not-compared"
)

// Action is what the merge did with an address.
type Action string

const (
	ActionAdded       Action = "added"
	ActionUpdated     Action = "updated"
	ActionKeptDiffers Action = "kept_differs"
	ActionUnchanged   Action = "unchanged"
	ActionMoved       Action = "moved"
	// ActionExcluded: the resource was left out of the export on purpose.
	ActionExcluded Action = "excluded"
)

// Role is why an address is in the export.
type Role string

const (
	RoleSelected   Role = "selected"
	RoleDependency Role = "dependency"
	// RoleEnvironment: a resource of a whole-environment export. There is
	// no selected item, so none is updated unless the operator says so.
	RoleEnvironment Role = "environment"
	// RoleExcluded: an excluded resource that nothing exported refers to.
	RoleExcluded Role = "excluded"
)

// Change is the outcome for one address.
type Change struct {
	Address string `json:"address"`
	Role    Role   `json:"role"`
	Status  Status `json:"status"`
	Action  Action `json:"action"`
	File    string `json:"file,omitempty"`
	From    string `json:"from,omitempty"` // for moved
	Reason  string `json:"reason,omitempty"`
	// Var is the per-environment ID variable that replaced the references
	// to an excluded resource, and SourceID its value in the source
	// environment (an ID, never a secret).
	Var      string `json:"var,omitempty"`
	SourceID string `json:"sourceId,omitempty"`
}

// Blocker stops a submission. Env is "" for request-level blockers.
type Blocker struct {
	Env      string `json:"env,omitempty"`
	Code     string `json:"code"`
	Variable string `json:"variable,omitempty"`
	Address  string `json:"address,omitempty"`
	Message  string `json:"message"`
}

// Blocker codes.
const (
	BlockMissingValue    = "missing_value"
	BlockMissingOverlay  = "missing_overlay"
	BlockUndeclaredKey   = "undeclared_key"
	BlockSecretInOverlay = "secret_in_overlay"
	BlockImportKey       = "invalid_import_key"
	BlockConflict        = "changed_in_both"
	BlockSecretsFile     = "customer_secrets"
	BlockModuleLayout    = "module_layout"
	BlockSecretFinding   = "secret_finding"
	BlockMissingDep      = "missing_dependency"
	BlockAutoTfvars      = "auto_tfvars"
	// BlockMissingImport: a built-in object (default population, default
	// sign-on policy, predefined scope, ...) exists in every environment
	// and must be adopted by its ID there, never created.
	BlockMissingImport = "missing_import"
)

// MergeRequest is one additive merge.
type MergeRequest struct {
	Layout Layout
	Export *neutralize.Result
	Head   Repo
	// Base is the repository at the commit last rolled out to the source
	// environment, for three-way status. nil falls back to two-way.
	Base Repo
	// Selected addresses are always updated (the explicitly chosen flow and
	// its deploy/enable companions). Everything else is a dependency.
	Selected []string
	// WholeEnvironment marks an export of the whole source environment:
	// every resource has the role "environment", and one that differs from
	// Git is kept unless Choices says update.
	WholeEnvironment bool
	// Choices override the default per address.
	Choices   map[string]Choice
	SourceEnv string
	// Rollout lists the environments after the source, in order.
	Rollout []string
	// EnvValues are operator-supplied environment values. They may set
	// keys that already exist (an explicit operation).
	EnvValues map[string]map[string]cty.Value
	// SecretsConfig is the Vault path convention (default
	// pipelinesecrets.DefaultConfig).
	SecretsConfig *pipelinesecrets.Config
	// Adopt lists built-in addresses of the export that every environment
	// must adopt by import ID; one without an import_ids entry in an
	// environment is a missing_import blocker there.
	Adopt []string
	// ImportIDs are operator-supplied adoption IDs: env -> address -> ID
	// (without the environment prefix). Existing entries are replaced.
	ImportIDs map[string]map[string]string
}

// MergeResult is the change set and its report.
type MergeResult struct {
	// Files holds the full new content of every created or changed file.
	Files map[string][]byte `json:"-"`
	// Deleted lists files the merge deletes (none today; reserved).
	Deleted         []string                   `json:"deleted,omitempty"`
	Changes         []Change                   `json:"changes"`
	Blockers        []Blocker                  `json:"blockers"`
	RequiredSecrets []string                   `json:"requiredSecrets"`
	NewSecrets      []pipelinesecrets.NewEntry `json:"-"`
	Warnings        []string                   `json:"warnings,omitempty"`
}

// Blocked reports whether any blocker exists.
func (r *MergeResult) Blocked() bool { return len(r.Blockers) > 0 }

// ChangedPaths lists the files the merge writes, sorted.
func (r *MergeResult) ChangedPaths() []string {
	out := make([]string, 0, len(r.Files))
	for p := range r.Files {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

type merger struct {
	req   MergeRequest
	l     Layout
	head  *index
	base  *index
	view  *overlayRepo
	files map[string]*hclblocks.File
	res   *MergeResult

	written  map[string]bool // addresses added or updated
	selected map[string]bool
	// movedFrom maps a relabelled address to its old address in Git.
	movedFrom map[string]string
}

// Merge performs the additive, block-level merge. It never writes to Head.
func Merge(req MergeRequest) (*MergeResult, error) {
	l := req.Layout.WithDefaults()
	if err := l.Validate(); err != nil {
		return nil, err
	}
	if req.Export == nil || req.Head == nil {
		return nil, errors.New("promote: export and head are required")
	}
	if !envNameRE.MatchString(req.SourceEnv) {
		return nil, errors.New("promote: source environment name is invalid")
	}
	m := &merger{req: req, l: l, view: newView(req.Head), files: map[string]*hclblocks.File{},
		res:     &MergeResult{Files: map[string][]byte{}, Changes: []Change{}, Blockers: []Blocker{}, RequiredSecrets: []string{}},
		written: map[string]bool{}, selected: map[string]bool{}}
	var err error
	if m.head, err = loadIndex(req.Head, l); err != nil {
		return nil, err
	}
	if req.Base != nil {
		if m.base, err = loadIndex(req.Base, l); err != nil {
			return nil, fmt.Errorf("base: %w", err)
		}
	}
	for _, a := range req.Selected {
		m.selected[a] = true
	}
	for a, c := range req.Choices {
		if c != ChoiceUpdate && c != ChoiceKeep && c != ChoiceExclude {
			return nil, fmt.Errorf("promote: choice for %s must be update, keep or exclude", a)
		}
	}
	known := map[string]bool{}
	for _, e := range m.envs() {
		if !envNameRE.MatchString(e) {
			return nil, fmt.Errorf("promote: environment name %q is invalid", e)
		}
		known[e] = true
	}
	for env, vals := range req.EnvValues {
		if !known[env] {
			return nil, fmt.Errorf("promote: values were given for %s, which is not the source or in the rollout", env)
		}
		for k := range vals {
			if v := req.Export.Variables[k]; v != nil && v.Kind == neutralize.KindSecret {
				return nil, fmt.Errorf("promote: %s is a secret; secrets come from customer_secrets.yml, never from environment values", k)
			}
		}
	}
	steps := []func() error{m.renames, m.blocks, m.declarations, m.imports, m.overlays, m.customerSecrets, m.finish}
	for _, s := range steps {
		if err := s(); err != nil {
			return nil, err
		}
	}
	return m.res, nil
}

// file loads (or creates) an editable Terraform file in the view.
func (m *merger) file(p string) (*hclblocks.File, error) {
	if f, ok := m.files[p]; ok {
		return f, nil
	}
	src, _, err := m.view.Read(p)
	if err != nil {
		return nil, err
	}
	f, err := hclblocks.Load(p, src)
	if err != nil {
		return nil, err
	}
	m.files[p] = f
	return f, nil
}

func (m *merger) save(f *hclblocks.File) { m.view.write(f.Name, f.Src) }

func resourceKey(addr string) string { return "resource." + addr }

func (m *merger) status(addr string, live []byte) Status {
	headText, inHead := m.head.texts[addr]
	if !inHead {
		return StatusNew
	}
	if hclblocks.Equal(live, headText) {
		return StatusMatches
	}
	if m.base == nil {
		return StatusChangedInSource
	}
	baseText, inBase := m.base.texts[addr]
	switch {
	case inBase && hclblocks.Equal(baseText, headText):
		return StatusChangedInSource
	case inBase && hclblocks.Equal(baseText, live):
		return StatusGitNewer
	case !inBase:
		// Added in Git after the base and different from the source.
		return StatusChangedInBoth
	}
	return StatusChangedInBoth
}

func (m *merger) blocks() error {
	for _, b := range m.req.Export.Blocks {
		role := RoleDependency
		if m.req.WholeEnvironment {
			role = RoleEnvironment
		}
		if m.selected[b.Address] {
			role = RoleSelected
		}
		st := m.status(b.Address, b.Text)
		ch := Change{Address: b.Address, Role: role, Status: st}
		choice, chosen := m.req.Choices[b.Address]
		update := false
		switch st {
		case StatusNew:
			update = true
		case StatusMatches:
			ch.Action = ActionUnchanged
		case StatusChangedInSource:
			update = role == RoleSelected || choice == ChoiceUpdate
			if role == RoleSelected && chosen && choice == ChoiceKeep {
				update = false
			}
			if !update && role == RoleEnvironment {
				ch.Reason = "differs from Git; kept (choose update to use the source version)"
			} else if !update {
				ch.Reason = "differs from Git; kept because it is a shared dependency (choose update to use the source version)"
			}
		case StatusGitNewer:
			update = chosen && choice == ChoiceUpdate
			if !update {
				ch.Reason = "Git is newer than the source; kept"
			}
		case StatusChangedInBoth:
			update = chosen && choice == ChoiceUpdate
			if !chosen {
				if role == RoleSelected {
					m.res.Blockers = append(m.res.Blockers, Blocker{Code: BlockConflict, Address: b.Address,
						Message: "changed in the source and in Git since the base; choose update (use source) or keep (keep Git version)"})
				}
				ch.Reason = "changed in both; Git version kept until a choice is made"
			}
		}
		if ch.Action == "" {
			if !update {
				ch.Action = ActionKeptDiffers
			} else if err := m.write(b, &ch); err != nil {
				return err
			}
		}
		if ch.File == "" {
			ch.File = m.head.defs[b.Address]
		}
		m.res.Changes = append(m.res.Changes, ch)
	}
	m.excluded()
	// Selected addresses that the export does not contain are reported.
	for _, a := range m.req.Selected {
		if m.req.Export.Block(a) == nil {
			m.res.Blockers = append(m.res.Blockers, Blocker{Code: BlockMissingDep, Address: a, Message: "selected resource is not in the export"})
		}
	}
	for _, alts := range m.req.Export.MissingDependencies {
		// "a|b": any of the addresses satisfies the reference.
		found := false
		for _, a := range strings.Split(alts, "|") {
			if _, ok := m.head.defs[a]; ok || m.inExport(a) {
				found = true
			}
		}
		if !found {
			a, _, _ := strings.Cut(alts, "|")
			m.res.Blockers = append(m.res.Blockers, Blocker{Code: BlockMissingDep, Address: a,
				Message: "referenced by the export but neither exported nor managed in Git"})
		}
	}
	return nil
}

// excluded reports the resources left out of the export. Nothing is written
// for them and their blocks in Git are untouched.
func (m *merger) excluded() {
	for _, e := range m.req.Export.Excluded {
		ch := Change{Address: e.Address, Role: RoleDependency, Status: StatusNotCompared, Action: ActionExcluded, File: m.head.defs[e.Address]}
		switch {
		case e.Managed:
			ch.Reason = "excluded; Git-managed copy kept"
		case e.Variable != "":
			ch.Reason = "excluded; referenced by ID variable `" + e.Variable + "`"
			ch.Var = e.Variable
			if v, ok := m.req.Export.SourceValues[e.Variable]; ok && v.Type() == cty.String {
				ch.SourceID = v.AsString()
			}
		default:
			ch.Reason = "excluded; not exported"
		}
		if !e.Referenced && m.req.Choices[e.Address] != ChoiceExclude {
			ch.Role = RoleExcluded
		}
		m.res.Changes = append(m.res.Changes, ch)
	}
}

func (m *merger) inExport(a string) bool { return m.req.Export.Block(a) != nil }

func (m *merger) write(b neutralize.Block, ch *Change) error {
	p, exists := m.head.defs[b.Address]
	if !exists {
		p = path.Join(m.l.ModuleDir, b.File)
		if moved, ok := m.movedFrom[b.Address]; ok {
			// The moved {} block records the rename; keep the old file.
			p = m.head.defs[moved]
		}
	}
	f, err := m.file(p)
	if err != nil {
		return err
	}
	if exists {
		if err := f.Replace(resourceKey(b.Address), b.Text); err != nil {
			return err
		}
		ch.Action = ActionUpdated
	} else {
		if err := f.Insert(resourceKey(b.Address), b.Text, true); err != nil {
			return err
		}
		ch.Action = ActionAdded
		if from, ok := m.movedFrom[b.Address]; ok {
			ch.Action, ch.From = ActionMoved, from
		}
	}
	ch.File = p
	m.save(f)
	m.written[b.Address] = true
	return nil
}

// --- renames ---

func (m *merger) movedFile() string { return path.Join(m.l.ModuleDir, "moved.tf") }

func movedBlock(from, to string) []byte {
	return []byte("moved {\n  from = " + from + "\n  to   = " + to + "\n}")
}

// renames records neutralizer relabels whose old address is in Git: the
// old block is deleted and a moved {} block keeps the state.
func (m *merger) renames() error {
	m.movedFrom = map[string]string{}
	for _, rn := range m.req.Export.Renames {
		if _, inHead := m.head.defs[rn.From]; !inHead {
			continue
		}
		if _, toInHead := m.head.defs[rn.To]; toInHead {
			continue
		}
		m.movedFrom[rn.To] = rn.From
		f, err := m.file(m.head.defs[rn.From])
		if err != nil {
			return err
		}
		if err := f.Remove(resourceKey(rn.From)); err != nil {
			return err
		}
		m.save(f)
		mf, err := m.file(m.movedFile())
		if err != nil {
			return err
		}
		if !bytes.Contains(mf.Src, []byte("from = "+rn.From+"\n")) {
			if err := mf.AppendText(movedBlock(rn.From, rn.To)); err != nil {
				return err
			}
			m.save(mf)
		}
		if err := m.renameImportKey(m.l.ModuleAddress(rn.From), m.l.ModuleAddress(rn.To)); err != nil {
			return err
		}
		if err := m.dropImportBlock(m.l.ModuleAddress(rn.From)); err != nil {
			return err
		}
	}
	return nil
}

// dropImportBlock removes the root import block for a module address.
func (m *merger) dropImportBlock(full string) error {
	if !m.head.imports[full] {
		return nil
	}
	f, err := m.file(m.l.ImportsFile)
	if err != nil {
		return err
	}
	for _, b := range f.Blocks {
		if b.Type != "import" {
			continue
		}
		text := b.Text(f.Src)
		if bytes.Contains(text, []byte("to       = "+full+"\n")) || bytes.Contains(text, []byte("to = "+full+"\n")) {
			src, err := hclblocks.Apply(f.Src, []hclblocks.Edit{{Start: b.Start, End: trailingBlank(f.Src, b.End)}})
			if err != nil {
				return err
			}
			nf, err := hclblocks.Load(f.Name, src)
			if err != nil {
				return err
			}
			m.files[f.Name] = nf
			m.save(nf)
			return nil
		}
	}
	return nil
}

func trailingBlank(src []byte, end int) int {
	for end < len(src) && (src[end] == '\n' || src[end] == ' ') {
		end++
	}
	return end
}

// renameImportKey renames (or, with to == "", deletes) an import_ids key
// in every overlay that has it.
func (m *merger) renameImportKey(from, to string) error {
	files, err := overlayFiles(m.view, m.l)
	if err != nil {
		return err
	}
	for _, p := range sortedValues(files) {
		src, _, err := m.view.Read(p)
		if err != nil {
			return err
		}
		o, err := overlay.ParseOverlay(src, p)
		if err != nil {
			return err
		}
		ids, ok := o.Values["import_ids"]
		if !ok || !ids.CanIterateElements() {
			continue
		}
		mp := map[string]cty.Value{}
		found := false
		for it := ids.ElementIterator(); it.Next(); {
			k, v := it.Element()
			if k.AsString() == from {
				found = true
				if to != "" {
					mp[to] = v
				}
				continue
			}
			mp[k.AsString()] = v
		}
		if !found {
			continue
		}
		val := cty.EmptyObjectVal
		if len(mp) > 0 {
			val = cty.ObjectVal(mp)
		}
		out, err := overlay.SetOverlay(src, p, map[string]cty.Value{"import_ids": val})
		if err != nil {
			return err
		}
		m.view.write(p, out)
	}
	return nil
}

func sortedValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// --- declarations ---

// neededVars returns the variables the written blocks use.
func (m *merger) neededVars() []*neutralize.Variable {
	set := map[string]bool{}
	for _, b := range m.req.Export.Blocks {
		if !m.written[b.Address] {
			continue
		}
		for _, v := range b.Vars {
			set[v] = true
		}
	}
	var out []*neutralize.Variable
	for name := range set {
		if v, ok := m.req.Export.Variables[name]; ok {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (m *merger) appendBlock(p, key string, text []byte) error {
	f, err := m.file(p)
	if err != nil {
		return err
	}
	if i, _ := f.Find(key); i >= 0 {
		return nil
	}
	if err := f.Insert(key, text, false); err != nil {
		return err
	}
	m.save(f)
	return nil
}

func (m *merger) declarations() error {
	vars := m.neededVars()
	newRepo := m.head.moduleFile == "" && len(m.head.rootTF) == 0
	// Child module: versions.tf and variable declarations.
	if len(m.head.defFiles) == 0 {
		vp := path.Join(m.l.ModuleDir, "versions.tf")
		if _, ok, _ := m.view.Read(vp); !ok {
			m.view.write(vp, []byte(neutralize.ChildVersionsTF))
		}
	}
	childVarsFile := path.Join(m.l.ModuleDir, "variables.tf")
	envVar := m.req.Export.Variables[neutralize.EnvironmentIDVariable]
	all := append([]*neutralize.Variable{envVar}, vars...)
	for _, v := range all {
		if !m.head.childVars[v.Name] {
			if err := m.appendBlock(childVarsFile, "variable."+v.Name, neutralize.ChildVariableBlock(v)); err != nil {
				return err
			}
		}
	}
	// Root declarations.
	for _, v := range all {
		if _, ok := m.head.rootVars[v.Name]; ok {
			continue
		}
		if v.Kind == neutralize.KindSecret {
			if err := m.appendBlock(m.l.SecretsFile, "variable."+v.Name, neutralize.SecretVariableBlock(v.Name)); err != nil {
				return err
			}
		} else if err := m.appendBlock(m.l.VariablesFile, "variable."+v.Name, neutralize.RootVariableBlock(v)); err != nil {
			return err
		}
	}
	if _, ok := m.head.rootVars["import_ids"]; !ok {
		if err := m.appendBlock(m.l.VariablesFile, "variable.import_ids", neutralize.ImportIDsVariableBlock()); err != nil {
			return err
		}
	}
	for _, v := range vars {
		if v.Kind == neutralize.KindSecret {
			m.res.RequiredSecrets = append(m.res.RequiredSecrets, v.Name)
		}
	}
	// Module call. A repository that already keeps its resources in a
	// module of another name must be configured, not given a second one.
	if m.head.moduleFile == "" && len(m.head.localModules) > 0 {
		m.res.Blockers = append(m.res.Blockers, Blocker{Code: BlockModuleLayout, Message: "The repository already calls module " +
			strings.Join(m.head.localModules, ", ") + " but no module \"" + m.l.ModuleName + "\". If the PingOne resources belong in that module, set repository.moduleName and repository.moduleDir in the server configuration (for example \"pingone\" and \"modules/pingone\"). Nothing is added until then"})
		return nil
	}
	if m.head.moduleFile == "" && len(m.head.otherModules) > 0 {
		m.res.Warnings = append(m.res.Warnings, "This adds module \""+m.l.ModuleName+"\" ("+m.l.ModuleDir+"/) next to the repository's module "+
			strings.Join(m.head.otherModules, ", ")+". If the PingOne resources belong in that module instead, set repository.moduleName and repository.moduleDir in the server configuration and preview again")
	}
	if m.head.moduleFile == "" {
		var inputs []string
		for _, v := range vars {
			inputs = append(inputs, v.Name)
		}
		if err := m.appendBlock(m.l.ModuleFile, "module."+m.l.ModuleName, neutralize.ModuleBlock(m.l.ModuleName, m.l.ModuleDir, inputs)); err != nil {
			return err
		}
	} else {
		var add strings.Builder
		var missing []string
		for _, v := range all {
			if !m.head.moduleInputs[v.Name] {
				missing = append(missing, v.Name)
			}
		}
		if len(missing) > 0 {
			for _, n := range missing {
				add.WriteString(neutralize.ModuleInput(n))
			}
			f, err := m.file(m.head.moduleFile)
			if err != nil {
				return err
			}
			body := hclblocksFormatLines(add.String())
			if err := f.AppendToBlock("module."+m.l.ModuleName, body); err != nil {
				return err
			}
			m.save(f)
		}
	}
	if newRepo {
		if _, ok, _ := m.view.Read("versions.tf"); !ok {
			m.view.write("versions.tf", []byte(rootVersionsTF))
		}
	}
	return nil
}

// hclblocksFormatLines aligns a group of "  a = b" lines like terraform fmt.
func hclblocksFormatLines(lines string) []byte {
	wrapped := hclblocks.Normalize([]byte("x {\n" + lines + "}\n"))
	inner := bytes.TrimSuffix(bytes.TrimPrefix(wrapped, []byte("x {\n")), []byte("}"))
	return inner
}

const rootVersionsTF = `terraform {
  required_version = ">= 1.16.0" # conditional imports on static addresses (ADR 0016)

  required_providers {
    pingone = {
      source  = "pingidentity/pingone"
      version = ">= 1.19.1, < 2.0.0"
    }
  }
}

# Worker credentials come from the pipeline environment:
# PINGONE_CLIENT_ID, PINGONE_CLIENT_SECRET, PINGONE_ENVIRONMENT_ID and
# PINGONE_REGION_CODE.
provider "pingone" {}
`

// imports adds a conditional import block for each written address.
func (m *merger) imports() error {
	var addrs []string
	for a := range m.written {
		if importable(a) && !m.head.imports[m.l.ModuleAddress(a)] {
			addrs = append(addrs, a)
		}
	}
	sort.Strings(addrs)
	if len(addrs) == 0 {
		return m.singletonImports()
	}
	f, err := m.file(m.l.ImportsFile)
	if err != nil {
		return err
	}
	for _, a := range addrs {
		if err := f.AppendText(neutralize.ImportBlock(m.l.ModuleName, a)); err != nil {
			return err
		}
	}
	m.save(f)
	return m.singletonImports()
}

// --- overlays ---

func (m *merger) envs() []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range append([]string{m.req.SourceEnv}, m.req.Rollout...) {
		if !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	return out
}

func (m *merger) overlays() error {
	needed := []*neutralize.Variable{m.req.Export.Variables[neutralize.EnvironmentIDVariable]}
	for _, v := range m.neededVars() {
		if v.PerEnvironment() {
			needed = append(needed, v)
		}
	}
	for _, env := range m.envs() {
		p, err := m.l.Overlay(env)
		if err != nil {
			return err
		}
		src, exists, err := m.view.Read(p)
		if err != nil {
			return err
		}
		cur := &overlay.Overlay{Values: map[string]cty.Value{}}
		if exists {
			if cur, err = overlay.ParseOverlay(src, p); err != nil {
				return err
			}
		}
		set := map[string]cty.Value{}
		for _, v := range needed {
			if v == nil || m.l.FromCI(v.Name) {
				continue
			}
			if _, ok := cur.Values[v.Name]; ok {
				continue
			}
			if env == m.req.SourceEnv {
				if val, ok := m.req.Export.SourceValues[v.Name]; ok {
					set[v.Name] = val
				}
			}
		}
		ids := cur.Values["import_ids"]
		changed := false
		if env == m.req.SourceEnv {
			ids, changed = m.sourceImportIDs(ids)
		}
		if extra := m.req.ImportIDs[env]; len(extra) > 0 {
			var more bool
			ids, more = m.withImportIDs(ids, extra)
			changed = changed || more
		}
		if changed {
			set["import_ids"] = ids
		}
		for k, val := range m.req.EnvValues[env] {
			if m.l.FromCI(k) {
				return fmt.Errorf("promote: %s comes from the pipeline (ciVariables); it is never written to %s", k, p)
			}
			if m.req.Export.Variables[k] != nil && m.req.Export.Variables[k].Kind == neutralize.KindSecret {
				return fmt.Errorf("promote: %s is a secret; secrets come from customer_secrets.yml, never from environment values", k)
			}
			if old, ok := cur.Values[k]; ok && old.RawEquals(val) {
				continue
			}
			set[k] = val
		}
		if len(set) == 0 {
			continue
		}
		var out []byte
		if exists {
			out, err = overlay.SetOverlay(src, p, set)
		} else {
			out, err = overlay.RenderOverlay(set)
		}
		if err != nil {
			return err
		}
		m.view.write(p, out)
	}
	return nil
}

// sourceImportIDs adds the source IDs of newly written addresses to the
// source overlay's import_ids, so the source environment's plan adopts the
// resources that already exist there instead of creating them. Existing
// keys are never changed.
func (m *merger) sourceImportIDs(cur cty.Value) (cty.Value, bool) {
	mp := map[string]cty.Value{}
	if !cur.IsNull() && cur.CanIterateElements() {
		for it := cur.ElementIterator(); it.Next(); {
			k, v := it.Element()
			mp[k.AsString()] = v
		}
	}
	changed := false
	for a := range m.written {
		id, ok := m.req.Export.SourceIDs[a]
		key := m.l.ModuleAddress(a)
		if _, exists := mp[key]; ok && importable(a) && !exists {
			mp[key] = cty.StringVal(id)
			changed = true
		}
	}
	if !changed {
		return cur, false
	}
	return cty.ObjectVal(mp), true
}

// customerSecrets declares new secrets in customer_secrets.yml (additions
// only, through internal/pipelinesecrets).
func (m *merger) customerSecrets() error {
	if len(m.res.RequiredSecrets) == 0 {
		return nil
	}
	src, ok, err := m.view.Read(m.l.CustomerSecretsFile)
	if err != nil {
		return err
	}
	if !ok {
		// A repository without the file yet gets one with the documented
		// shape; the merge request shows it for review.
		src, ok = []byte("# GitLab CI secret declarations: each TF_VAR_ variable is read from Vault.\n.customer-secrets:\n  secrets:\n"), true
	}
	pf, err := pipelinesecrets.Parse(src)
	if err != nil {
		m.res.Blockers = append(m.res.Blockers, Blocker{Code: BlockSecretsFile, Message: m.l.CustomerSecretsFile + " cannot be updated (" + err.Error() + "). This request needs the secrets " + strings.Join(m.res.RequiredSecrets, ", ") + " declared there. The expected shape is: .customer-secrets: / secrets: / TF_VAR_<name>: { vault: <path>/<name>@<mount>, file: false }. If your repository declares secrets elsewhere, set customerSecretsFile in the server configuration's repository settings"})
		return nil
	}
	cfg := pipelinesecrets.DefaultConfig
	if m.req.SecretsConfig != nil {
		cfg = *m.req.SecretsConfig
	}
	r := pf.Check(cfg, m.res.RequiredSecrets)
	for _, k := range r.CommentedOut {
		m.res.Blockers = append(m.res.Blockers, Blocker{Code: BlockSecretsFile, Variable: k,
			Message: "TF_VAR_" + k + " is commented out in " + m.l.CustomerSecretsFile + "; uncomment it by hand"})
	}
	var add []pipelinesecrets.NewEntry
	for _, k := range r.Undeclared {
		add = append(add, pipelinesecrets.NewEntry{VarName: k, Field: k})
	}
	if len(add) == 0 {
		return nil
	}
	out, err := pf.Add(cfg, add)
	if err != nil {
		return err
	}
	m.res.NewSecrets = add
	m.view.write(m.l.CustomerSecretsFile, out)
	return nil
}

func (m *merger) finish() error {
	for p, b := range m.view.writes {
		old, ok, err := m.req.Head.Read(p)
		if err != nil {
			return err
		}
		if ok && bytes.Equal(old, b) {
			continue
		}
		m.res.Files[p] = b
	}
	all, err := ValidateOverlays(m.view, m.l, m.envs())
	if err != nil {
		return err
	}
	// A value no variable declares only makes Terraform warn: it must not
	// block a request.
	var blockers []Blocker
	for _, b := range all {
		if b.Code == BlockUndeclaredKey && b.Variable != "" {
			m.res.Warnings = append(m.res.Warnings, b.Env+": "+b.Message+"; Terraform ignores it with a warning")
			continue
		}
		blockers = append(blockers, b)
	}
	m.res.Blockers = append(m.res.Blockers, m.nameExcluded(blockers)...)
	adoptBlockers, err := m.adoption()
	if err != nil {
		return err
	}
	m.res.Blockers = append(m.res.Blockers, adoptBlockers...)
	for _, f := range m.req.Export.Findings {
		m.res.Blockers = append(m.res.Blockers, Blocker{Code: BlockSecretFinding, Address: f.Name,
			Message: "suspected secret (" + f.Kind + ") in the export at " + f.File + lineSuffix(f.Line) + "; secrets must come from customer_secrets.yml"})
	}
	sort.Strings(m.res.RequiredSecrets)
	sort.Slice(m.res.Changes, func(i, j int) bool { return m.res.Changes[i].Address < m.res.Changes[j].Address })
	sortBlockers(m.res.Blockers)
	return nil
}

// nameExcluded rewrites the missing_value blockers of excluded-reference
// variables so they name the dependency whose ID is needed.
func (m *merger) nameExcluded(bs []Blocker) []Blocker {
	for i, b := range bs {
		v := m.req.Export.Variables[b.Variable]
		if b.Code != BlockMissingValue || v == nil || v.Kind != neutralize.KindExcludedRef {
			continue
		}
		p, err := m.l.Overlay(b.Env)
		if err != nil {
			p = b.Env + ".tfvars"
		}
		bs[i].Address = v.Excluded
		bs[i].Message = p + " must set " + v.Name + ": the ID of the existing " + v.Excluded + " in " + b.Env +
			" (it was excluded from the export, so the shared definitions refer to it by ID)"
	}
	return bs
}

func lineSuffix(n int) string {
	if n <= 0 {
		return ""
	}
	return ":" + strconv.Itoa(n)
}

func sortBlockers(b []Blocker) {
	sort.SliceStable(b, func(i, j int) bool {
		if b[i].Env != b[j].Env {
			return b[i].Env < b[j].Env
		}
		if b[i].Code != b[j].Code {
			return b[i].Code < b[j].Code
		}
		return b[i].Variable+b[i].Address < b[j].Variable+b[j].Address
	})
}

// singletonImports adopts written settings resources by environment ID.
func (m *merger) singletonImports() error {
	var addrs []string
	for a := range m.written {
		if singleton(a) && !m.head.imports[m.l.ModuleAddress(a)] {
			addrs = append(addrs, a)
		}
	}
	if len(addrs) == 0 {
		return nil
	}
	sort.Strings(addrs)
	f, err := m.file(m.l.ImportsFile)
	if err != nil {
		return err
	}
	for _, a := range addrs {
		if err := f.AppendText(neutralize.SingletonImportBlock(m.l.ModuleName, a)); err != nil {
			return err
		}
	}
	m.save(f)
	return nil
}

// withImportIDs sets operator-supplied adoption IDs on import_ids.
func (m *merger) withImportIDs(cur cty.Value, extra map[string]string) (cty.Value, bool) {
	mp := map[string]cty.Value{}
	if !cur.IsNull() && cur.CanIterateElements() {
		for it := cur.ElementIterator(); it.Next(); {
			k, v := it.Element()
			mp[k.AsString()] = v
		}
	}
	changed := false
	for a, id := range extra {
		key := m.l.ModuleAddress(a)
		if old, ok := mp[key]; ok && old.Type() == cty.String && old.AsString() == id {
			continue
		}
		mp[key] = cty.StringVal(id)
		changed = true
	}
	if !changed {
		return cur, false
	}
	return cty.ObjectVal(mp), true
}

// adoption reports built-in objects that an environment has no import ID
// for. The source environment's IDs come from the export.
func (m *merger) adoption() ([]Blocker, error) {
	var out []Blocker
	for _, a := range m.req.Adopt {
		if !importable(a) || !m.written[a] {
			continue
		}
		key := m.l.ModuleAddress(a)
		for _, env := range m.envs() {
			p, err := m.l.Overlay(env)
			if err != nil {
				return nil, err
			}
			src, ok, err := m.view.Read(p)
			if err != nil {
				return nil, err
			}
			has := false
			if ok {
				o, err := overlay.ParseOverlay(src, p)
				if err != nil {
					return nil, err
				}
				if ids, ok := o.Values["import_ids"]; ok && !ids.IsNull() && ids.CanIterateElements() {
					for it := ids.ElementIterator(); it.Next(); {
						k, v := it.Element()
						if k.AsString() == key && v.Type() == cty.String && v.AsString() != "" {
							has = true
						}
					}
				}
			}
			if !has {
				out = append(out, Blocker{Env: env, Code: BlockMissingImport, Address: a, Variable: "import_ids",
					Message: a + " already exists in every environment (a built-in object); enter its ID for " + env + " so it is adopted, not created"})
			}
		}
	}
	return out, nil
}
