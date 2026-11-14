package neutralize

import (
	"bytes"
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
	"pingone-promote/internal/overlay"
	"pingone-promote/internal/secretscan"
)

// EnvironmentIDVariable is the root and child variable for the managed
// environment, named as terraformer names it.
const EnvironmentIDVariable = "pingone_environment_id"

// Resource types (pingidentity/pingone provider).
const (
	TypeApplication = "pingone_davinci_application"
	TypeFlowPolicy  = "pingone_davinci_application_flow_policy"
	TypeConnector   = "pingone_davinci_connector_instance"
	TypeFlow        = "pingone_davinci_flow"
	TypeFlowDeploy  = "pingone_davinci_flow_deploy"
	TypeFlowEnable  = "pingone_davinci_flow_enable"
	TypeVariable    = "pingone_davinci_variable"
)

// LiftRule lifts one attribute of a resource type into an environment-local
// variable. Attribute is a dot path through attributes, object items and
// nested blocks, for example "oauth.redirect_uris".
type LiftRule struct {
	ResourceType string `json:"resourceType"`
	Attribute    string `json:"attribute"`
	// Label limits the rule to one resource label; "" applies to all.
	Label string `json:"label,omitempty"`
}

// DefaultLiftRules lift application redirect and logout URIs.
var DefaultLiftRules = []LiftRule{
	{ResourceType: TypeApplication, Attribute: "oauth.redirect_uris"},
	{ResourceType: TypeApplication, Attribute: "oauth.logout_uris"},
}

// Options configure neutralization.
type Options struct {
	// SourceEnvironmentID is the exported environment. Required.
	SourceEnvironmentID string
	// Lift rules; nil means DefaultLiftRules.
	Lift []LiftRule
	// ConnectorEnvLocal keeps connector properties as environment-local
	// variables, by connector label then property name. Every other
	// non-secret connector property is inlined as a literal.
	ConnectorEnvLocal map[string][]string
	// EnvLocalVariables keeps any terraformer variable environment-local.
	EnvLocalVariables map[string]bool
	// InlineVariables inlines DaVinci variable values that are the same in
	// every environment ("same in all envs").
	InlineVariables map[string]bool
	// SecretNames maps terraformer secret variable names onto names
	// declared in customer_secrets.yml.
	SecretNames map[string]string
	// Excluded lists resources left out of the export on purpose
	// (--exclude-resources). References to them never stay resource
	// references to a block that is not in the output.
	Excluded []Excluded
}

// Excluded is one resource left out of the export on purpose.
type Excluded struct {
	Address string
	// Managed is true when the resource is already managed in Git:
	// references to it keep pointing at its address (like keeping the Git
	// version). Otherwise they become a per-environment ID variable.
	Managed bool
	// ID is the source environment's ID, when known. It values the ID
	// variable when terraformer did not (an embedded reference carries no
	// default).
	ID string
}

// ExcludedRef reports what became of an excluded resource.
type ExcludedRef struct {
	Address string `json:"address"`
	Managed bool   `json:"managed,omitempty"`
	// Variable is the per-environment ID variable that replaced the
	// references to it ("" when nothing references it, or it is managed).
	Variable string `json:"variable,omitempty"`
	// Referenced is true when an exported block refers to it.
	Referenced bool `json:"referenced,omitempty"`
}

// ExcludedVariableName is the name terraformer v0.6.0 gives the variable
// that replaces a reference to an excluded resource
// (core.resolveOneReference: SanitizeVariableName(<type>_<label>_<field>)).
func ExcludedVariableName(address string) string { return VariableName(address + "_id") }

// Kind classifies a variable of the shared definitions.
type Kind string

const (
	KindEnvironmentID Kind = "environment_id"
	KindEnvLocal      Kind = "env_local"
	KindSecret        Kind = "secret"
	// KindExcludedRef is the ID of a resource left out of the export. It is
	// environment-local like KindEnvLocal: every overlay sets the existing
	// resource's ID in that environment.
	KindExcludedRef Kind = "excluded_ref"
)

// PerEnvironment reports whether every environment overlay sets the
// variable.
func (v *Variable) PerEnvironment() bool { return v.Kind == KindEnvLocal || v.Kind == KindExcludedRef }

// Variable is a variable of the shared definitions, declared in the child
// module and at the root.
type Variable struct {
	Name        string `json:"name"`
	Type        string `json:"type"` // HCL type expression
	Description string `json:"description"`
	Kind        Kind   `json:"kind"`
	// Origin is terraformer's name before any renaming.
	Origin string `json:"origin,omitempty"`
	// Excluded is the address of the excluded resource whose ID this
	// variable carries (KindExcludedRef).
	Excluded string `json:"excluded,omitempty"`
}

// Block is one neutralized resource block.
type Block struct {
	Address string   `json:"address"` // <type>.<label>, module-relative
	Type    string   `json:"type"`
	Label   string   `json:"label"`
	File    string   `json:"file"` // <type>.tf, as terraformer names it
	Text    []byte   `json:"-"`    // fmt-clean, no surrounding blank lines
	Refs    []string `json:"refs,omitempty"`
	Vars    []string `json:"vars,omitempty"`
	// Name is the resource's name attribute, when it is a literal.
	Name string `json:"name,omitempty"`
}

// Rename is a label change made by the neutralizer; the merge emits a
// moved {} block when the old address exists in Git.
type Rename struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason"`
}

// Result is the neutralized export.
type Result struct {
	Blocks    []Block              `json:"blocks"`
	Variables map[string]*Variable `json:"variables"`
	// SourceValues are the source environment's values for environment-
	// local variables (secrets are never included), plus the environment ID.
	SourceValues map[string]cty.Value `json:"-"`
	// SourceIDs maps addresses to the source environment's import IDs
	// without the "<environment_id>/" prefix.
	SourceIDs map[string]string `json:"sourceIds"`
	Renames   []Rename          `json:"renames,omitempty"`
	// Excluded reports each excluded resource, sorted by address.
	Excluded []ExcludedRef `json:"excluded,omitempty"`
	// GlobalVariableRefs lists {{global.variables.x}} names per flow.
	GlobalVariableRefs map[string][]string `json:"globalVariableRefs,omitempty"`
	// MissingDependencies are addresses referenced (by a reference or a
	// {{global.variables.x}} template, whose alternatives are joined by "|")
	// {{global.variables.x}} expression) that the export does not contain.
	MissingDependencies []string             `json:"missingDependencies,omitempty"`
	Findings            []secretscan.Finding `json:"findings,omitempty"`
	Warnings            []string             `json:"warnings,omitempty"`
}

// Block returns the block with the address, or nil.
func (r *Result) Block(addr string) *Block {
	i := sort.Search(len(r.Blocks), func(i int) bool { return r.Blocks[i].Address >= addr })
	if i < len(r.Blocks) && r.Blocks[i].Address == addr {
		return &r.Blocks[i]
	}
	return nil
}

// Closure returns seeds plus every block they reference, transitively,
// within the result, sorted.
func (r *Result) Closure(seeds []string) []string {
	seen := map[string]bool{}
	var walk func(string)
	walk = func(a string) {
		if seen[a] {
			return
		}
		b := r.Block(a)
		if b == nil {
			return
		}
		seen[a] = true
		for _, ref := range b.Refs {
			walk(ref)
		}
	}
	for _, s := range seeds {
		walk(s)
	}
	out := make([]string, 0, len(seen))
	for a := range seen {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// CollisionError is a label or variable-name collision. Nothing is
// produced, because guessing would bind the wrong resource.
type CollisionError struct {
	What      string   // "label" or "variable"
	Name      string   // the colliding label or variable name
	Addresses []string // the resources involved
	Hint      string
}

func (e *CollisionError) Error() string {
	msg := fmt.Sprintf("neutralize: %s collision on %q (%s)", e.What, e.Name, strings.Join(e.Addresses, ", "))
	if e.Hint != "" {
		msg += "; " + e.Hint
	}
	return msg
}

var (
	uuidRE            = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	secretNameRE      = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,127}$`)
	excludedAddressRE = regexp.MustCompile(`^pingone_[a-z0-9_]+\.[A-Za-z0-9_\-]{1,512}$`)
	globalVarRE       = regexp.MustCompile(`\{\{\s*global\.variables\.([A-Za-z0-9_\-]+)\s*\}\}`)
	companyVarRE      = regexp.MustCompile(`\{\{\s*global\.company\.variables\.([A-Za-z0-9_\-]+)\s*\}\}`)
)

type rawBlock struct {
	typ, label, file string
	text             []byte
	start            int                             // offset of text in its file
	attrs            map[string]hclsyntax.Expression // top-level attributes
	body             *hclsyntax.Body
}

type decl struct {
	typ, desc string
	sensitive bool
}

type state struct {
	in   *Input
	opts Options
	res  *Result

	decls  map[string]decl
	values map[string]cty.Value
	raw    []*rawBlock
	// relabel maps a raw block to its new label.
	relabel map[*rawBlock]string
	// refRename resolves a reference from a block to a renamed address.
	refRename func(from *rawBlock, addr string) string
	// varRename maps a raw block's variable reference to a new name
	// (flow-scoped collisions).
	varRenameBy map[*rawBlock]map[string]string
	// disposition per final variable name
	inline  map[string]cty.Value
	rename  map[string]string // terraformer var -> final name (global)
	lifted  map[*rawBlock][]lift
	noValue map[string]bool
	// shared are variables terraformer declared twice, identically, for two
	// resources (DaVinci variables with one name in two contexts).
	shared map[string]bool
	// desc overrides a variable's description (split shared variables
	// name their context).
	desc map[string]string
	// excl is Options.Excluded by address; exclVar maps terraformer's
	// variable name for an excluded reference to the address; mapBack maps
	// such a variable of a Git-managed resource to its address.
	excl    map[string]Excluded
	exclVar map[string]string
	mapBack map[string]string
}

type lift struct {
	rng  hcl.Range
	name string
}

// Neutralize converts a terraformer export.
func Neutralize(in *Input, opts Options) (*Result, error) {
	if !uuidRE.MatchString(opts.SourceEnvironmentID) {
		return nil, errors.New("neutralize: SourceEnvironmentID must be a PingOne environment ID")
	}
	if opts.Lift == nil {
		opts.Lift = DefaultLiftRules
	}
	s := &state{in: in, opts: opts, res: &Result{
		Variables:          map[string]*Variable{},
		SourceValues:       map[string]cty.Value{EnvironmentIDVariable: cty.StringVal(opts.SourceEnvironmentID)},
		SourceIDs:          map[string]string{},
		GlobalVariableRefs: map[string][]string{},
	}, relabel: map[*rawBlock]string{}, varRenameBy: map[*rawBlock]map[string]string{},
		inline: map[string]cty.Value{}, rename: map[string]string{}, lifted: map[*rawBlock][]lift{}, noValue: map[string]bool{}, shared: map[string]bool{}, desc: map[string]string{},
		excl: map[string]Excluded{}, exclVar: map[string]string{}, mapBack: map[string]string{}}
	for _, e := range opts.Excluded {
		if !excludedAddressRE.MatchString(e.Address) {
			return nil, fmt.Errorf("neutralize: excluded %q is not a resource address", e.Address)
		}
		s.excl[e.Address] = e
		s.exclVar[ExcludedVariableName(e.Address)] = e.Address
	}
	s.res.Variables[EnvironmentIDVariable] = &Variable{Name: EnvironmentIDVariable, Type: "string",
		Description: "The PingOne environment ID to configure DaVinci resources in", Kind: KindEnvironmentID}
	steps := []func() error{s.readDecls, s.readValues, s.readImports, s.readBlocks, s.resolveCollisions, s.splitShared, s.classifyVariables, s.liftAttributes, s.rewrite, s.scan}
	for _, step := range steps {
		if err := step(); err != nil {
			return nil, err
		}
	}
	return s.res, nil
}

func (s *state) readDecls() error {
	s.decls = map[string]decl{}
	p := path.Join(s.in.moduleDir(), "variables.tf")
	src, ok := s.in.Files[p]
	if !ok {
		return fmt.Errorf("neutralize: %s is missing; is this a terraformer export?", p)
	}
	body, err := hclblocks.Parse(src, p)
	if err != nil {
		return err
	}
	for _, b := range body.Blocks {
		if b.Type != "variable" || len(b.Labels) != 1 {
			continue
		}
		name := b.Labels[0]
		d := decl{typ: "string"}
		if a, ok := b.Body.Attributes["type"]; ok {
			r := a.Expr.Range()
			d.typ = string(src[r.Start.Byte:r.End.Byte])
		}
		if a, ok := b.Body.Attributes["description"]; ok {
			if v, diags := a.Expr.Value(nil); !diags.HasErrors() && v.Type() == cty.String {
				d.desc = v.AsString()
			}
		}
		if a, ok := b.Body.Attributes["sensitive"]; ok {
			if v, diags := a.Expr.Value(nil); !diags.HasErrors() && v.Type() == cty.Bool {
				d.sensitive = v.True()
			}
		}
		if prev, dup := s.decls[name]; dup {
			// terraformer 0.6.0 names a DaVinci variable's value variable
			// after the name alone, so one name in two contexts declares
			// it twice. Identical declarations are split per resource by
			// splitShared; different ones cannot be told apart.
			if prev != d {
				return &CollisionError{What: "variable", Name: name, Addresses: []string{p}, Hint: "terraformer declared it twice, differently; two resources produced the same variable name"}
			}
			s.shared[name] = true
			continue
		}
		s.decls[name] = d
	}
	for _, name := range s.in.Shared {
		if _, ok := s.decls[name]; ok {
			s.shared[name] = true
		}
	}
	return nil
}

// readValues reads terraformer's *.auto.tfvars (written with
// --include-values). The file is never copied to the output.
func (s *state) readValues() error {
	s.values = map[string]cty.Value{}
	for p, src := range s.in.Files {
		if !strings.HasSuffix(p, ".auto.tfvars") {
			continue
		}
		if path.Dir(p) != "." {
			s.res.Warnings = append(s.res.Warnings, "ignored "+p+" (not at the export root)")
			continue
		}
		o, err := overlay.ParseOverlay(src, p)
		if err != nil {
			vals, ok := s.valuesWithSharedKeys(src, p)
			if !ok {
				return fmt.Errorf("neutralize: %w (a duplicate key means two resources share a variable name)", err)
			}
			o = &overlay.Overlay{Values: vals}
		}
		for k, v := range o.Values {
			s.values[k] = v
		}
	}
	return nil
}

var redefinedRE = regexp.MustCompile(`argument "([^"]+)" was already set`)

// valuesWithSharedKeys reads an auto.tfvars that sets a shared variable
// twice (see readDecls). Only such repeats are tolerated; the shared
// values themselves are dropped, since which belongs to which resource is
// lost.
func (s *state) valuesWithSharedKeys(src []byte, p string) (map[string]cty.Value, bool) {
	f, diags := hclsyntax.ParseConfig(src, p, hcl.InitialPos)
	if f == nil {
		return nil, false
	}
	for _, d := range diags {
		if d.Severity != hcl.DiagError {
			continue
		}
		m := redefinedRE.FindStringSubmatch(d.Detail)
		if d.Summary != "Attribute redefined" || m == nil || !s.shared[m[1]] {
			return nil, false
		}
	}
	body, ok := f.Body.(*hclsyntax.Body)
	if !ok || len(body.Blocks) > 0 {
		return nil, false
	}
	out := map[string]cty.Value{}
	for name, attr := range body.Attributes {
		if s.shared[name] {
			continue
		}
		v, d := attr.Expr.Value(nil)
		if d.HasErrors() {
			return nil, false
		}
		out[name] = v
	}
	return out, true
}

func (s *state) readImports() error {
	prefix := "module." + s.in.moduleName() + "."
	for p, src := range s.in.Files {
		if path.Dir(p) != "." || !strings.HasSuffix(p, ".tf") || p == s.in.moduleName()+"-module.tf" {
			// The module call is not read: it only passes variables, and
			// terraformer can repeat an input there.
			continue
		}
		body, err := hclblocks.Parse(src, p)
		if err != nil {
			return err
		}
		for _, b := range body.Blocks {
			if b.Type != "import" {
				continue
			}
			to, ok := b.Body.Attributes["to"]
			id, ok2 := b.Body.Attributes["id"]
			if !ok || !ok2 {
				continue
			}
			r := to.Expr.Range()
			addr := strings.TrimPrefix(string(src[r.Start.Byte:r.End.Byte]), prefix)
			v, diags := id.Expr.Value(nil)
			if diags.HasErrors() || v.Type() != cty.String {
				return fmt.Errorf("neutralize: %s: import id for %s is not a literal", p, addr)
			}
			raw := v.AsString()
			envPrefix := s.opts.SourceEnvironmentID + "/"
			if !strings.HasPrefix(raw, envPrefix) {
				s.res.Warnings = append(s.res.Warnings, "import ID for "+addr+" does not start with the source environment ID; skipped")
				continue
			}
			s.res.SourceIDs[addr] = strings.TrimPrefix(raw, envPrefix)
		}
	}
	return nil
}

func (s *state) readBlocks() error {
	for _, p := range s.in.resourceFiles() {
		src := s.in.Files[p]
		body, err := hclblocks.Parse(src, p)
		if err != nil {
			return err
		}
		for _, b := range body.Blocks {
			if b.Type != "resource" || len(b.Labels) != 2 {
				s.res.Warnings = append(s.res.Warnings, fmt.Sprintf("%s: ignored a %s block", p, b.Type))
				continue
			}
			r := b.Range()
			s.raw = append(s.raw, &rawBlock{typ: b.Labels[0], label: b.Labels[1], file: path.Base(p),
				text: append([]byte(nil), src[r.Start.Byte:r.End.Byte]...), start: r.Start.Byte, attrs: exprs(b.Body), body: b.Body})
		}
	}
	return nil
}

func exprs(b *hclsyntax.Body) map[string]hclsyntax.Expression {
	out := map[string]hclsyntax.Expression{}
	for k, a := range b.Attributes {
		out[k] = a.Expr
	}
	return out
}

func literalString(e hclsyntax.Expression) (string, bool) {
	if e == nil {
		return "", false
	}
	v, diags := e.Value(nil)
	if diags.HasErrors() || v.IsNull() || v.Type() != cty.String {
		return "", false
	}
	return v.AsString(), true
}

// flowOf returns the flow label a flow-scoped variable belongs to, from
// flow = { id = pingone_davinci_flow.<label>.id }.
func flowOf(rb *rawBlock) string {
	obj, ok := rb.attrs["flow"].(*hclsyntax.ObjectConsExpr)
	if !ok {
		return ""
	}
	for _, it := range obj.Items {
		if k, ok := objectKey(it.KeyExpr); ok && k == "id" {
			if st, ok := it.ValueExpr.(*hclsyntax.ScopeTraversalExpr); ok && len(st.Traversal) >= 2 && st.Traversal.RootName() == TypeFlow {
				if a, ok := st.Traversal[1].(hcl.TraverseAttr); ok {
					return a.Name
				}
			}
		}
	}
	return ""
}

func objectKey(e hclsyntax.Expression) (string, bool) {
	k, ok := e.(*hclsyntax.ObjectConsKeyExpr)
	if !ok {
		return "", false
	}
	if st, ok := k.Wrapped.(*hclsyntax.ScopeTraversalExpr); ok && len(st.Traversal) == 1 {
		return st.Traversal.RootName(), true
	}
	return literalString(k.Wrapped)
}

func (rb *rawBlock) addr() string { return rb.typ + "." + rb.label }

// resolveCollisions fails closed on duplicate addresses, except for
// flow-scoped variables that share a name: those are relabelled with
// their flow, "pingcli__<name>_flow_<flow label>".
func (s *state) resolveCollisions() error {
	byAddr := map[string][]*rawBlock{}
	type vkey struct{ name, context string }
	byVar := map[vkey][]*rawBlock{}
	for _, rb := range s.raw {
		byAddr[rb.addr()] = append(byAddr[rb.addr()], rb)
		if rb.typ == TypeVariable {
			n, _ := literalString(rb.attrs["name"])
			c, _ := literalString(rb.attrs["context"])
			byVar[vkey{n, c}] = append(byVar[vkey{n, c}], rb)
		}
	}
	// ambiguous: old address -> members (for reference resolution)
	ambiguous := map[string][]*rawBlock{}
	renamedAddr := map[string]string{}
	var keys []vkey
	for k := range byVar {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].name+"\x00"+keys[i].context < keys[j].name+"\x00"+keys[j].context })
	for _, k := range keys {
		members := byVar[k]
		if len(members) < 2 {
			continue
		}
		var addrs []string
		for _, m := range members {
			addrs = append(addrs, m.addr())
		}
		sort.Strings(addrs)
		if k.context != "flow" {
			return &CollisionError{What: "label", Name: Label(k.name, k.context), Addresses: addrs, Hint: "two DaVinci variables share a name and context"}
		}
		flows := map[string]bool{}
		for _, m := range members {
			f := flowOf(m)
			if f == "" || flows[f] {
				return &CollisionError{What: "label", Name: Label(k.name, k.context), Addresses: addrs,
					Hint: "flow-scoped variables share a name and their flows cannot be told apart"}
			}
			flows[f] = true
		}
		oldVar := "davinci_variable_" + VariableName(k.name) + "_value"
		sharedVar := 0
		for _, m := range members {
			if bytes.Contains(m.text, []byte("var."+oldVar)) {
				sharedVar++
			}
		}
		for _, m := range members {
			f := flowOf(m)
			newLabel := Label(k.name, k.context) + "_" + stripPrefix(f)
			s.relabel[m] = newLabel
			ambiguous[m.addr()] = append(ambiguous[m.addr()], m)
			renamedAddr[m.addr()+"\x00"+f] = TypeVariable + "." + newLabel
			s.res.Renames = append(s.res.Renames, Rename{From: m.addr(), To: TypeVariable + "." + newLabel,
				Reason: "flow-scoped variable " + k.name + " exists on several flows; the flow was added to the label (upstream: terraformer label_fields [name, context])"})
			newVar := "davinci_variable_" + VariableName(k.name) + "_" + VariableName(stripPrefix(f)) + "_value"
			if s.varRenameBy[m] == nil {
				s.varRenameBy[m] = map[string]string{}
			}
			s.varRenameBy[m][oldVar] = newVar
			if sharedVar > 1 {
				// terraformer wrote one value for several variables;
				// which belongs to which flow is lost.
				s.noValue[newVar] = true
			} else if v, ok := s.values[oldVar]; ok {
				s.values[newVar] = v
			}
		}
		if sharedVar > 1 {
			s.res.Warnings = append(s.res.Warnings, "flow-scoped variable "+k.name+": terraformer exported one value for several flows; supply each value explicitly")
		}
	}
	for a, list := range byAddr {
		if len(list) > 1 && len(ambiguous[a]) != len(list) {
			var addrs []string
			for _, m := range list {
				addrs = append(addrs, m.file)
			}
			return &CollisionError{What: "label", Name: a, Addresses: addrs, Hint: "two exported resources have the same address"}
		}
	}
	s.refRename = func(from *rawBlock, addr string) string {
		members, ok := ambiguous[addr]
		if !ok {
			return addr
		}
		// A flow refers to its own flow-scoped variable.
		if from.typ == TypeFlow {
			if to, ok := renamedAddr[addr+"\x00"+from.label]; ok {
				return to
			}
		}
		if len(members) == 1 {
			return TypeVariable + "." + s.relabel[members[0]]
		}
		return ""
	}
	sort.Slice(s.res.Renames, func(i, j int) bool { return s.res.Renames[i].To < s.res.Renames[j].To })
	return nil
}

// splitShared gives each resource that uses a shared variable (see
// readDecls) a variable of its own, named after its label, whose value is
// entered per environment.
func (s *state) splitShared() error {
	if len(s.shared) == 0 {
		return nil
	}
	// Each block is lexed once, whatever the number of shared variables.
	usersOf := map[string][]*rawBlock{}
	for _, rb := range s.raw {
		if !bytes.Contains(rb.text, []byte("var.")) {
			continue
		}
		refs, err := varRefs(rb.text)
		if err != nil {
			return err
		}
		for _, n := range refs {
			if s.shared[n] && s.finalVarName(rb, n) == n {
				usersOf[n] = append(usersOf[n], rb)
			}
		}
	}
	for _, name := range sortedSet(s.shared) {
		users := usersOf[name]
		if len(users) == 0 || (len(users) == 1 && !slices.Contains(s.in.Shared, name)) {
			continue
		}
		var made, unknown []string
		for _, rb := range users {
			label := rb.label
			if nl, ok := s.relabel[rb]; ok {
				label = nl
			}
			n := strings.TrimSuffix(name, "_value") + "_" + VariableName(stripPrefix(label)) + "_value"
			if rb.typ == TypeVariable {
				n = "davinci_variable_" + VariableName(stripPrefix(label)) + "_value"
			}
			if s.varRenameBy[rb] == nil {
				s.varRenameBy[rb] = map[string]string{}
			}
			s.varRenameBy[rb][name] = n
			if v, ok := s.in.Values[rb.addr()]; ok {
				s.values[n] = cty.StringVal(v)
			} else {
				s.noValue[n] = true
				unknown = append(unknown, n)
			}
			if d, ok := s.decls[name]; ok && d.desc != "" {
				where := stripPrefix(label)
				if c, ok := literalString(rb.attrs["context"]); ok && c != "" {
					where = c
				}
				s.desc[n] = d.desc + " (" + where + ")"
			}
			made = append(made, n)
		}
		sort.Strings(made)
		if len(unknown) > 0 {
			sort.Strings(unknown)
			s.res.Warnings = append(s.res.Warnings, "terraformer exported one value variable ("+name+") for several resources; each now has its own ("+strings.Join(made, ", ")+"), and the value of "+strings.Join(unknown, ", ")+" is entered for every environment")
		}
	}
	return nil
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// varRefs lists var.<name> references in block text.
func varRefs(text []byte) ([]string, error) {
	toks, diags := hclsyntax.LexConfig(text, "block.tf", hcl.InitialPos)
	if diags.HasErrors() {
		return nil, errors.New("neutralize: block does not lex")
	}
	seen := map[string]bool{}
	for i := 0; i+2 < len(toks); i++ {
		if isTraversal(toks, i, "var") {
			seen[string(toks[i+2].Bytes)] = true
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}

func isTraversal(toks hclsyntax.Tokens, i int, root string) bool {
	if toks[i].Type != hclsyntax.TokenIdent || (root != "" && string(toks[i].Bytes) != root) {
		return false
	}
	if i > 0 && toks[i-1].Type == hclsyntax.TokenDot {
		return false
	}
	return i+2 < len(toks) && toks[i+1].Type == hclsyntax.TokenDot && toks[i+2].Type == hclsyntax.TokenIdent
}

func (s *state) finalVarName(rb *rawBlock, v string) string {
	if m := s.varRenameBy[rb]; m != nil {
		if n, ok := m[v]; ok {
			return n
		}
	}
	return v
}

// connectorVar is terraformer's connector property variable name
// (internal/platform/pingone/resource_connection.go connectorVariableName).
func connectorVar(label, prop string) string {
	return VariableName("davinci_connection_" + stripPrefix(label) + "_" + prop)
}

func (s *state) classifyVariables() error {
	envLocalConnector := map[string]bool{}
	for label, props := range s.opts.ConnectorEnvLocal {
		for _, p := range props {
			envLocalConnector[connectorVar(label, p)] = true
		}
	}
	usedBy := map[string]map[string]bool{} // terraformer var -> resource types
	for _, rb := range s.raw {
		refs, err := varRefs(rb.text)
		if err != nil {
			return err
		}
		for _, v := range refs {
			if usedBy[v] == nil {
				usedBy[v] = map[string]bool{}
			}
			usedBy[v][rb.typ] = true
		}
	}
	names := make([]string, 0, len(usedBy))
	for v := range usedBy {
		names = append(names, v)
	}
	sort.Strings(names)
	final := map[string]string{} // final name -> origin (collision check)
	add := func(v *Variable) error {
		if prev, ok := s.res.Variables[v.Name]; ok {
			if prev.Kind == KindSecret && v.Kind == KindSecret {
				return nil // two sites share one pipeline secret
			}
			return &CollisionError{What: "variable", Name: v.Name, Addresses: []string{final[v.Name], v.Origin},
				Hint: "two values map to one variable name; choose another secret mapping"}
		}
		s.res.Variables[v.Name] = v
		final[v.Name] = v.Origin
		return nil
	}
	for _, origin := range names {
		if origin == EnvironmentIDVariable {
			continue
		}
		if addr, ok := s.exclVar[origin]; ok {
			// terraformer turned a reference to an excluded resource into
			// an ID variable.
			if s.excl[addr].Managed {
				s.mapBack[origin] = addr
				continue
			}
			s.excludedRef(addr)
			continue
		}
		d, ok := s.decls[origin]
		if !ok {
			return fmt.Errorf("neutralize: variable %s is used but not declared in the export", origin)
		}
		// Variables renamed for flow-scoped collisions are handled per block.
		targets := []string{origin}
		for _, m := range s.varRenameBy {
			if n, ok := m[origin]; ok {
				targets = append(targets, n)
			}
		}
		if len(targets) > 1 {
			targets = targets[1:]
			sort.Strings(targets)
		}
		for _, name := range targets {
			// A DaVinci variable of type string that holds a credential
			// (its name or its value says so) is kept out of Git like a
			// secret variable: its value comes from Vault.
			credential := !d.sensitive && usedBy[origin][TypeVariable] && len(usedBy[origin]) == 1 && s.credentialValue(name)
			if credential {
				s.res.Warnings = append(s.res.Warnings, "DaVinci variable value "+name+" looks like a credential, so it is not written to Git: it is a secret (customer_secrets.yml, from Vault). Consider making the variable a secret in DaVinci")
			}
			switch {
			case d.sensitive || credential:
				mapped := name
				if m, ok := s.opts.SecretNames[name]; ok && m != "" {
					mapped = m
				}
				if !secretNameRE.MatchString(mapped) || mapped == EnvironmentIDVariable || mapped == "import_ids" {
					return fmt.Errorf("neutralize: secret name %q is not a valid customer_secrets.yml variable name", mapped)
				}
				s.rename[name] = mapped
				if err := add(&Variable{Name: mapped, Type: "string", Description: "PingOne secret " + mapped + " (pipeline: TF_VAR_" + mapped + ").", Kind: KindSecret, Origin: name}); err != nil {
					return err
				}
			case s.opts.EnvLocalVariables[name] || envLocalConnector[name]:
				if err := add(s.keep(name, d)); err != nil {
					return err
				}
			case usedBy[origin][TypeConnector] && len(usedBy[origin]) == 1, usedBy[origin][TypeVariable] && s.opts.InlineVariables[name]:
				v, ok := s.values[name]
				if !ok || s.noValue[name] {
					return fmt.Errorf("neutralize: no exported value for %s; export with --include-values or keep it environment-local", name)
				}
				if v.Type() == cty.String && secretscan.SuspectLiteral(name, v.AsString()) {
					// Never inline a secret-looking literal into shared
					// definitions: keep it env-local; scan() reports it.
					if err := add(s.keep(name, d)); err != nil {
						return err
					}
					continue
				}
				s.inline[name] = v
			default:
				if err := add(s.keep(name, d)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// excludedRef makes the per-environment ID variable of an excluded resource
// that is not in Git, valued with its source ID (terraformer's value when it
// wrote one, else the catalog's).
func (s *state) excludedRef(addr string) *Variable {
	name := ExcludedVariableName(addr)
	if v, ok := s.res.Variables[name]; ok {
		return v
	}
	id := ""
	if v, ok := s.values[name]; ok && v.Type() == cty.String {
		id = v.AsString()
	}
	if id == "" {
		id = s.excl[addr].ID
	}
	if id != "" {
		s.res.SourceValues[name] = cty.StringVal(id)
	}
	v := &Variable{Name: name, Type: "string", Kind: KindExcludedRef, Origin: name, Excluded: addr,
		Description: "ID of the existing " + addr + " in this environment (excluded from the export)"}
	s.res.Variables[name] = v
	return v
}

// keep makes a variable environment-local and records its source value.
func (s *state) keep(name string, d decl) *Variable {
	desc := d.desc
	if o, ok := s.desc[name]; ok {
		desc = o
	}
	if desc == "" {
		desc = "Environment value " + name
	}
	if v, ok := s.values[name]; ok && !s.noValue[name] {
		s.res.SourceValues[name] = v
	}
	return &Variable{Name: name, Type: d.typ, Description: desc, Kind: KindEnvLocal, Origin: name}
}

// credentialValue reports whether a DaVinci variable's value variable holds
// a credential: by the variable's name (a password, token, secret or
// credentials) or by its source value (a JWT, a private key, or base64 of
// "user:password").
func (s *state) credentialValue(name string) bool {
	base := secretscan.VariableBase(name)
	if secretscan.HasSecretSuffix(base) || secretscan.CredentialName(base) {
		return true
	}
	v, ok := s.values[name]
	if !ok || v.IsNull() || !v.IsKnown() || v.Type() != cty.String {
		return false
	}
	return secretscan.CredentialValue(v.AsString())
}
