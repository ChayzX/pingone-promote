// Package promote computes the dependency closure of a selection from a
// translated source environment and merges it into a target environment's
// tfvars document.
//
// Rules:
//   - Closure. Selected flows pull in their subflows and their flow-scoped
//     variables as REQUIRED dependencies: those definitions are copied with
//     the flow. Connections and global variables they reference are REUSABLE:
//     the target's own entry is used when it exists, and a source copy is
//     added only when the target has none (or UpdateReusable is set).
//     Selected applications pull in the flows their policies route to.
//   - Exclusion overrides upstream. An excluded dependency is never copied,
//     even when a selected item requires it. Placeholder references to it
//     become @@param:<kind>_<key>_id@@ so the target supplies the ID. A
//     structured reference (node connection, policy flow) to an excluded item
//     that the target does not manage is blocked.
//   - Classification. Each dependency is in_sync, differs, new, unmanaged
//     (referenced but in neither environment) or excluded.
//   - Merge. Definitions copy byte for byte. The target keeps its
//     parameters, import_ids, environment ID and environment-local fields
//     (application redirect and logout URIs).
//   - Blocks. Re-keying an existing resource, or moving a flow to another
//     tier, changes its Terraform address. Those changes are refused with a
//     moved {} snippet for the operator to apply deliberately.
package promote

import (
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"pingone-promote/internal/davinci"
	"pingone-promote/internal/tfvars"
	"pingone-promote/internal/translate"
)

// Kinds used in selections, exclusions and dependencies.
const (
	KindFlow        = "flow"
	KindConnection  = "connection"
	KindVariable    = "variable"
	KindApplication = "application"
)

// Role is why an item is part of the closure.
type Role string

const (
	RoleSelected Role = "selected"
	RoleRequired Role = "required"
	RoleReusable Role = "reusable"
)

// Status compares a closure item with the target.
type Status string

const (
	StatusInSync    Status = "in_sync"
	StatusDiffers   Status = "differs"
	StatusNew       Status = "new"
	StatusUnmanaged Status = "unmanaged"
	StatusExcluded  Status = "excluded"
)

// Selection is what the operator chose to promote.
type Selection struct {
	Flows        []string
	Applications []string
	Connections  []string
	Variables    []string
	// Exclude holds "kind:key" items that must not be copied.
	Exclude map[string]bool
	// UpdateReusable overwrites target connections and global variables that
	// differ from the source. By default the target's own entries are kept.
	UpdateReusable bool
}

// Dependency is one closure item.
type Dependency struct {
	Kind   string   `json:"kind"`
	Key    string   `json:"key"`
	Role   Role     `json:"role"`
	Status Status   `json:"status"`
	Via    []string `json:"via,omitempty"`
}

// Block is a change the engine refuses to make automatically.
type Block struct {
	Code    string `json:"code"` // rekey, tier_change, excluded_reference
	Kind    string `json:"kind"`
	Key     string `json:"key"`
	Message string `json:"message"`
	Moved   string `json:"moved,omitempty"`
}

// Warning is a merge note. Messages never contain values.
type Warning struct {
	Code    string `json:"code"`
	Path    string `json:"path"`
	Message string `json:"message"`
}

// Result is a promotion plan. Doc is nil when Blocks is not empty.
type Result struct {
	Doc                *tfvars.Document `json:"-"`
	Dependencies       []Dependency     `json:"dependencies"`
	RequiredSecrets    []string         `json:"required_secrets"`
	RequiredParameters []string         `json:"required_parameters"`
	MissingParameters  []string         `json:"missing_parameters"`
	Problems           []tfvars.Problem `json:"problems"`
	Warnings           []Warning        `json:"warnings"`
	Blocks             []Block          `json:"blocks"`
}

// Blocked reports whether the plan needs operator action first.
func (r *Result) Blocked() bool { return len(r.Blocks) > 0 }

// NewTarget returns an empty document for an environment with no tfvars yet.
func NewTarget(envID string) *tfvars.Document {
	return &tfvars.Document{SchemaVersion: tfvars.SchemaVersion, EnvironmentID: envID,
		Parameters: map[string]string{}, Connections: map[string]tfvars.Connection{}, Variables: map[string]tfvars.Variable{},
		Flows: map[string]tfvars.Flow{}, Applications: map[string]tfvars.Application{},
		ImportIDs: tfvars.ImportIDs{Connections: map[string]string{}, Flows: map[string]string{}, Variables: map[string]string{},
			Applications: map[string]string{}, FlowPolicies: map[string]string{}}}
}

// TranslateFor translates inv with the target's flow tiers kept sticky.
func TranslateFor(inv *davinci.Inventory, target *tfvars.Document, opts translate.Options) (*translate.Result, error) {
	if opts.ExistingTiers == nil {
		opts.ExistingTiers = map[string]int{}
	}
	for k, f := range target.Flows {
		if _, set := opts.ExistingTiers[k]; !set {
			opts.ExistingTiers[k] = f.Tier
		}
	}
	return translate.Translate(inv, opts)
}

type item struct{ kind, key string }

func (i item) String() string { return i.kind + ":" + i.key }

type planner struct {
	src, dst *tfvars.Document
	sel      Selection
	res      *Result
	deps     map[item]*Dependency
	order    []item
}

// Plan computes the closure of sel in source and merges it into a copy of
// target. target is not modified.
func Plan(source *translate.Result, target *tfvars.Document, sel Selection) (*Result, error) {
	if source == nil || source.Doc == nil || target == nil {
		return nil, fmt.Errorf("promote: source and target documents are required")
	}
	dst, err := clone(target)
	if err != nil {
		return nil, err
	}
	p := &planner{src: source.Doc, dst: dst, sel: sel, res: &Result{}, deps: map[item]*Dependency{}}
	if p.sel.Exclude == nil {
		p.sel.Exclude = map[string]bool{}
	}
	for _, k := range sel.Flows {
		p.add(item{KindFlow, k}, RoleSelected, "")
	}
	for _, k := range sel.Applications {
		p.add(item{KindApplication, k}, RoleSelected, "")
	}
	for _, k := range sel.Connections {
		p.add(item{KindConnection, k}, RoleSelected, "")
	}
	for _, k := range sel.Variables {
		p.add(item{KindVariable, k}, RoleSelected, "")
	}
	for i := 0; i < len(p.order); i++ {
		p.expand(p.order[i])
	}
	for _, c := range source.TierConflicts {
		p.tierConflict(c)
	}
	p.classify()
	p.checkRekeys()
	if !p.res.Blocked() {
		p.merge()
	}
	p.finish()
	return p.res, nil
}

// clone deep-copies a document through the canonical writer and parser.
func clone(d *tfvars.Document) (*tfvars.Document, error) {
	b, err := tfvars.Write(d)
	if err != nil {
		return nil, fmt.Errorf("promote: target document cannot be written: %w", err)
	}
	return tfvars.Parse(b, "target.tfvars")
}

func (p *planner) excluded(it item) bool { return p.sel.Exclude[it.String()] }

func (p *planner) inSource(it item) bool {
	switch it.kind {
	case KindFlow:
		_, ok := p.src.Flows[it.key]
		return ok
	case KindConnection:
		_, ok := p.src.Connections[it.key]
		return ok
	case KindVariable:
		_, ok := p.src.Variables[it.key]
		return ok
	case KindApplication:
		_, ok := p.src.Applications[it.key]
		return ok
	}
	return false
}

func (p *planner) inTarget(it item) bool {
	switch it.kind {
	case KindFlow:
		_, ok := p.dst.Flows[it.key]
		return ok
	case KindConnection:
		_, ok := p.dst.Connections[it.key]
		return ok
	case KindVariable:
		_, ok := p.dst.Variables[it.key]
		return ok
	case KindApplication:
		_, ok := p.dst.Applications[it.key]
		return ok
	}
	return false
}

func (p *planner) add(it item, role Role, via string) {
	d, ok := p.deps[it]
	if !ok {
		d = &Dependency{Kind: it.kind, Key: it.key, Role: role}
		p.deps[it] = d
		p.order = append(p.order, it)
	} else if rank(role) > rank(d.Role) {
		d.Role = role
	}
	if via != "" && !contains(d.Via, via) {
		d.Via = append(d.Via, via)
	}
}

func rank(r Role) int {
	switch r {
	case RoleSelected:
		return 3
	case RoleRequired:
		return 2
	}
	return 1
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

var templateVarRE = regexp.MustCompile(`\{\{\s*global\.(?:company\.)?variables\.([A-Za-z0-9_-]+)`)

var placeholderRE = regexp.MustCompile(`@@(connection|flow|variable):([A-Za-z0-9_-]{1,128})@@`)

// refsIn lists placeholder references in s. "@@@@" escapes are skipped by
// scanning the same way the module resolver splits.
func refsIn(s string) []item {
	var out []item
	parts := strings.Split(s, "@@")
	for i := 1; i < len(parts); i += 2 {
		kind, key, ok := strings.Cut(parts[i], ":")
		if ok && (kind == KindConnection || kind == KindFlow || kind == KindVariable) {
			out = append(out, item{kind, key})
		}
	}
	return out
}

func (p *planner) expand(it item) {
	if p.excluded(it) || !p.inSource(it) {
		return
	}
	via := it.String()
	switch it.kind {
	case KindFlow:
		f := p.src.Flows[it.key]
		for _, n := range sortedNodes(f.Graph.Nodes) {
			node := f.Graph.Nodes[n]
			if node.Connection != nil {
				p.add(item{KindConnection, *node.Connection}, RoleReusable, via)
			}
			if node.Properties != nil {
				// DaVinci templates reference variables by name, for example
				// {{global.variables.brand}} or {{global.company.variables.x}}.
				for _, m := range templateVarRE.FindAllStringSubmatch(*node.Properties, -1) {
					for _, vk := range sortedNodes(p.src.Variables) {
						if v := p.src.Variables[vk]; v.Name == m[1] && v.Context != "flow" {
							p.add(item{KindVariable, vk}, RoleReusable, via)
						}
					}
				}
				for _, r := range refsIn(*node.Properties) {
					role := RoleReusable
					if r.kind == KindFlow {
						role = RoleRequired
					}
					p.add(r, role, via)
				}
			}
		}
		for _, vk := range sortedNodes(p.src.Variables) {
			if v := p.src.Variables[vk]; v.Flow != nil && *v.Flow == it.key {
				p.add(item{KindVariable, vk}, RoleRequired, via)
			}
		}
	case KindApplication:
		a := p.src.Applications[it.key]
		for _, pk := range sortedNodes(a.FlowPolicies) {
			for _, d := range a.FlowPolicies[pk].Distributions {
				p.add(item{KindFlow, d.Flow}, RoleRequired, via)
			}
		}
	}
}

func sortedNodes[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (p *planner) classify() {
	for _, it := range p.order {
		d := p.deps[it]
		switch {
		case p.excluded(it):
			d.Status = StatusExcluded
		case !p.inSource(it) && !p.inTarget(it):
			d.Status = StatusUnmanaged
		case !p.inSource(it):
			d.Status = StatusInSync // target manages it; nothing to copy
		case !p.inTarget(it):
			d.Status = StatusNew
		case p.equal(it):
			d.Status = StatusInSync
		default:
			d.Status = StatusDiffers
		}
	}
}

// equal compares a source entry with the target entry, ignoring
// environment-local application fields.
func (p *planner) equal(it item) bool {
	a, b := NewTarget(p.src.EnvironmentID), NewTarget(p.src.EnvironmentID)
	switch it.kind {
	case KindFlow:
		a.Flows[it.key], b.Flows[it.key] = p.src.Flows[it.key], p.dst.Flows[it.key]
	case KindConnection:
		a.Connections[it.key], b.Connections[it.key] = p.src.Connections[it.key], p.dst.Connections[it.key]
	case KindVariable:
		a.Variables[it.key], b.Variables[it.key] = p.src.Variables[it.key], p.dst.Variables[it.key]
	case KindApplication:
		a.Applications[it.key] = withEnvLocal(p.src.Applications[it.key], p.dst.Applications[it.key])
		b.Applications[it.key] = p.dst.Applications[it.key]
	}
	return len(tfvars.Diff(a, b)) == 0
}

// withEnvLocal returns src with dst's environment-local OAuth fields.
func withEnvLocal(src, dst tfvars.Application) tfvars.Application {
	if src.OAuth == nil {
		return src
	}
	o := *src.OAuth
	if dst.OAuth != nil {
		o.RedirectURIs, o.LogoutURIs = dst.OAuth.RedirectURIs, dst.OAuth.LogoutURIs
	} else {
		o.RedirectURIs, o.LogoutURIs = nil, nil
	}
	src.OAuth = &o
	return src
}

// --- addresses and moved blocks ---

const moduleAddr = "module.davinci."

func addr(resource, key string) string {
	return moduleAddr + resource + "[" + strconv.Quote(key) + "]"
}

func moved(from, to string) string {
	return "moved {\n  from = " + from + "\n  to   = " + to + "\n}\n"
}

func flowAddrs(tier int, key string) []string {
	return []string{
		addr("pingone_davinci_flow.t"+strconv.Itoa(tier), key),
		addr("pingone_davinci_flow_deploy.this", key),
		addr("pingone_davinci_flow_enable.this", key),
	}
}

func variableResource(v tfvars.Variable) string {
	if v.Context == "flow" {
		return "pingone_davinci_variable.flow_scoped"
	}
	return "pingone_davinci_variable.global"
}

func (p *planner) block(b Block) { p.res.Blocks = append(p.res.Blocks, b) }

func (p *planner) tierConflict(c translate.TierConflict) {
	// Prefer raising the parent; lower the child when the parent is already
	// in the top tier.
	if c.ChildTier+1 <= tfvars.MaxTier {
		p.block(Block{Code: "tier_change", Kind: KindFlow, Key: c.Parent,
			Message: fmt.Sprintf("flow %q (tier %d) references %q (tier %d); raise %q to tier %d with this moved block, then retry", c.Parent, c.ParentTier, c.Child, c.ChildTier, c.Parent, c.ChildTier+1),
			Moved:   moved(addr("pingone_davinci_flow.t"+strconv.Itoa(c.ParentTier), c.Parent), addr("pingone_davinci_flow.t"+strconv.Itoa(c.ChildTier+1), c.Parent))})
		return
	}
	p.block(Block{Code: "tier_change", Kind: KindFlow, Key: c.Child,
		Message: fmt.Sprintf("flow %q (tier %d) references %q (tier %d); lower %q to tier %d with this moved block, then retry", c.Parent, c.ParentTier, c.Child, c.ChildTier, c.Child, c.ParentTier-1),
		Moved:   moved(addr("pingone_davinci_flow.t"+strconv.Itoa(c.ChildTier), c.Child), addr("pingone_davinci_flow.t"+strconv.Itoa(c.ParentTier-1), c.Child))})
}

// checkRekeys blocks copies whose identity already exists in the target
// under another key, and flows whose tier differs from the target's.
func (p *planner) checkRekeys() {
	for _, it := range p.order {
		d := p.deps[it]
		if d.Status == StatusExcluded || d.Status == StatusUnmanaged || !p.inSource(it) {
			continue
		}
		switch it.kind {
		case KindFlow:
			sf := p.src.Flows[it.key]
			if tf, ok := p.dst.Flows[it.key]; ok && tf.Tier != sf.Tier {
				var mv strings.Builder
				mv.WriteString(moved(flowAddrs(tf.Tier, it.key)[0], flowAddrs(sf.Tier, it.key)[0]))
				p.block(Block{Code: "tier_change", Kind: KindFlow, Key: it.key,
					Message: fmt.Sprintf("flow %q is tier %d in the target and tier %d in the source; moving tiers changes its address", it.key, tf.Tier, sf.Tier),
					Moved:   mv.String()})
			}
			if p.inTarget(it) {
				continue
			}
			for tk, tf := range p.dst.Flows {
				if tf.Name == sf.Name && tk != it.key {
					var mv strings.Builder
					from, to := flowAddrs(tf.Tier, tk), flowAddrs(sf.Tier, it.key)
					for i := range from {
						mv.WriteString(moved(from[i], to[i]))
					}
					p.block(Block{Code: "rekey", Kind: KindFlow, Key: it.key,
						Message: fmt.Sprintf("the target manages flow %q as %q; adopting the new key %q needs these moved blocks (or a key override)", sf.Name, tk, it.key),
						Moved:   mv.String()})
				}
			}
		case KindConnection:
			if p.inTarget(it) {
				continue
			}
			sc := p.src.Connections[it.key]
			for tk, tc := range p.dst.Connections {
				if tc.Name == sc.Name && tk != it.key {
					p.block(Block{Code: "rekey", Kind: KindConnection, Key: it.key,
						Message: fmt.Sprintf("the target manages connection %q as %q; adopt the new key with this moved block (or a key override)", sc.Name, tk),
						Moved:   moved(addr("pingone_davinci_connector_instance.this", tk), addr("pingone_davinci_connector_instance.this", it.key))})
				}
			}
		case KindVariable:
			if p.inTarget(it) {
				continue
			}
			sv := p.src.Variables[it.key]
			for tk, tv := range p.dst.Variables {
				if tv.Name == sv.Name && tv.Context == sv.Context && reflect.DeepEqual(tv.Flow, sv.Flow) && tk != it.key {
					p.block(Block{Code: "rekey", Kind: KindVariable, Key: it.key,
						Message: fmt.Sprintf("the target manages this %s variable as %q; adopt the new key with this moved block", sv.Context, tk),
						Moved:   moved(addr(variableResource(tv), tk), addr(variableResource(sv), it.key))})
				}
			}
		case KindApplication:
			if p.inTarget(it) {
				continue
			}
			sa := p.src.Applications[it.key]
			for tk, ta := range p.dst.Applications {
				if ta.Name == sa.Name && tk != it.key {
					var mv strings.Builder
					mv.WriteString(moved(addr("pingone_davinci_application.this", tk), addr("pingone_davinci_application.this", it.key)))
					for _, pk := range sortedNodes(ta.FlowPolicies) {
						mv.WriteString(moved(addr("pingone_davinci_application_flow_policy.this", tk+"/"+pk), addr("pingone_davinci_application_flow_policy.this", it.key+"/"+pk)))
					}
					p.block(Block{Code: "rekey", Kind: KindApplication, Key: it.key,
						Message: fmt.Sprintf("the target manages application %q as %q; adopt the new key with these moved blocks", sa.Name, tk),
						Moved:   mv.String()})
				}
			}
		}
	}
}

// --- merge ---

func (p *planner) warn(code, path, msg string) {
	p.res.Warnings = append(p.res.Warnings, Warning{Code: code, Path: path, Message: msg})
}

var paramUnsafe = regexp.MustCompile(`[^a-z0-9_]`)

// ExclusionParam is the parameter that replaces a reference to an excluded
// or unmanaged item.
func ExclusionParam(kind, key string) string {
	return paramUnsafe.ReplaceAllString(strings.ToLower(kind+"_"+key+"_id"), "_")
}

// rewriteRefs replaces references to excluded or unmanaged items with params.
func (p *planner) rewriteRefs(s *string, path string) *string {
	if s == nil {
		return nil
	}
	out := placeholderRE.ReplaceAllStringFunc(*s, func(m string) string {
		sub := placeholderRE.FindStringSubmatch(m)
		it := item{sub[1], sub[2]}
		if !p.excluded(it) && (p.inSource(it) || p.inTarget(it)) {
			return m
		}
		key := ExclusionParam(it.kind, it.key)
		p.warn("reference_to_param", path, fmt.Sprintf("%s %q is %s; the reference now reads parameter %q", it.kind, it.key, p.statusWord(it), key))
		return "@@param:" + key + "@@"
	})
	return &out
}

func (p *planner) statusWord(it item) string {
	if p.excluded(it) {
		return "excluded"
	}
	return "unmanaged"
}

func (p *planner) copies(it item) bool {
	d := p.deps[it]
	if d == nil || !p.inSource(it) {
		return false
	}
	switch d.Status {
	case StatusNew:
		return true
	case StatusDiffers:
		if d.Role == RoleReusable && !p.sel.UpdateReusable {
			p.warn("reusable_differs", it.String(), "target keeps its own definition; set UpdateReusable to overwrite it")
			return false
		}
		return true
	}
	return false
}

func (p *planner) merge() {
	for _, it := range p.order {
		if !p.copies(it) {
			continue
		}
		switch it.kind {
		case KindConnection:
			c := p.src.Connections[it.key]
			c.Properties = p.rewriteRefs(c.Properties, "davinci_connections"+q(it.key))
			p.dst.Connections[it.key] = c
			if p.deps[it].Status == StatusNew {
				p.warn("review_connection", "davinci_connections"+q(it.key), "new connection copied from the source; review environment-specific property values")
			}
		case KindVariable:
			v := p.src.Variables[it.key]
			if v.Value != nil && v.Value.JSONObject != nil {
				cp := *v.Value
				cp.JSONObject = p.rewriteRefs(cp.JSONObject, "davinci_variables"+q(it.key))
				v.Value = &cp
			}
			p.dst.Variables[it.key] = v
		case KindFlow:
			f := p.src.Flows[it.key]
			nodes := make(map[string]tfvars.Node, len(f.Graph.Nodes))
			for nid, n := range f.Graph.Nodes {
				np := "davinci_flows" + q(it.key) + ".graph.nodes" + q(nid)
				if n.Connection != nil {
					ci := item{KindConnection, *n.Connection}
					if p.excluded(ci) && !p.inTarget(ci) || !p.inSource(ci) && !p.inTarget(ci) {
						p.block(Block{Code: "excluded_reference", Kind: KindFlow, Key: it.key,
							Message: fmt.Sprintf("node %q uses connection %q, which is %s and not managed in the target; include it or add it to the target first", nid, ci.key, p.statusWord(ci))})
					}
				}
				n.Properties = p.rewriteRefs(n.Properties, np+".properties")
				nodes[nid] = n
			}
			f.Graph.Nodes = nodes
			p.dst.Flows[it.key] = f
		case KindApplication:
			src := p.src.Applications[it.key]
			dstApp, existed := p.dst.Applications[it.key]
			app := withEnvLocal(src, dstApp)
			if !existed && src.OAuth != nil && (len(src.OAuth.RedirectURIs) > 0 || len(src.OAuth.LogoutURIs) > 0) {
				p.warn("env_local_review", "davinci_applications"+q(it.key)+".oauth", "redirect and logout URIs are environment-local and were not copied; set them for the target")
			}
			policies := map[string]tfvars.FlowPolicy{}
			for k, pol := range dstApp.FlowPolicies {
				policies[k] = pol
			}
			for k, pol := range src.FlowPolicies {
				for i, d := range pol.Distributions {
					fi := item{KindFlow, d.Flow}
					if p.excluded(fi) && !p.inTarget(fi) {
						p.block(Block{Code: "excluded_reference", Kind: KindApplication, Key: it.key,
							Message: fmt.Sprintf("policy %q distribution %d routes to excluded flow %q, which the target does not manage", k, i, d.Flow)})
					}
				}
				policies[k] = pol
			}
			app.FlowPolicies = policies
			p.dst.Applications[it.key] = app
		}
	}
	if p.res.Blocked() {
		return
	}
	p.res.Doc = p.dst
}

func q(k string) string { return "[" + strconv.Quote(k) + "]" }

func (p *planner) finish() {
	for _, it := range p.order {
		d := p.deps[it]
		sort.Strings(d.Via)
		p.res.Dependencies = append(p.res.Dependencies, *d)
	}
	sort.Slice(p.res.Dependencies, func(i, j int) bool {
		a, b := p.res.Dependencies[i], p.res.Dependencies[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Key < b.Key
	})
	sort.SliceStable(p.res.Blocks, func(i, j int) bool {
		a, b := p.res.Blocks[i], p.res.Blocks[j]
		if a.Key != b.Key {
			return a.Key < b.Key
		}
		return a.Code < b.Code
	})
	sort.SliceStable(p.res.Warnings, func(i, j int) bool {
		a, b := p.res.Warnings[i], p.res.Warnings[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Code < b.Code
	})
	p.res.RequiredSecrets, p.res.RequiredParameters, p.res.MissingParameters = []string{}, []string{}, []string{}
	if p.res.Doc == nil {
		return
	}
	r := tfvars.Validate(p.res.Doc)
	p.res.RequiredSecrets = append(p.res.RequiredSecrets, r.RequiredSecrets...)
	p.res.RequiredParameters = append(p.res.RequiredParameters, r.RequiredParameters...)
	for _, k := range r.RequiredParameters {
		if _, ok := p.res.Doc.Parameters[k]; !ok {
			p.res.MissingParameters = append(p.res.MissingParameters, k)
		}
	}
	for _, pr := range r.Problems {
		if pr.Code == tfvars.CodeRefUnknown && strings.Contains(pr.Message, "parameter ") {
			continue // reported as MissingParameters
		}
		p.res.Problems = append(p.res.Problems, pr)
	}
}
