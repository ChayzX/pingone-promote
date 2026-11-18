package promote

import (
	"maps"
	"sort"
	"strings"

	"github.com/zclconf/go-cty/cty"

	"pingone-promote/internal/catalog"
	"pingone-promote/internal/neutralize"
	"pingone-promote/internal/overlay"
)

// mergeImportIDs applies source adoption, rollout hints, and explicit import
// choices in precedence order. Shared rollout targets must agree with the export.
func (m *merger) mergeImportIDs(env string, cur *overlay.Overlay, set map[string]cty.Value) {
	source := env == m.req.SourceEnv
	shared := !source && m.targetsExportedEnvironment(env, cur.Values)
	extra := map[string]string{}
	if !source {
		known := m.knownIDs(cur)
		target, overridden := m.req.EnvValues[env][neutralize.EnvironmentIDVariable]
		if overridden && !sameEnvironmentID(cur.Values[neutralize.EnvironmentIDVariable], target) {
			m.retargetHints(env, cur, set, known, shared)
			for addr := range known {
				value := m.req.EnvValues[env][neutralize.ExcludedVariableName(addr)]
				if value.Type() == cty.String && !value.IsNull() && value.AsString() != "" {
					extra[addr] = value.AsString()
				}
			}
		} else {
			for addr, id := range known {
				if _, exported := m.req.Export.SourceIDs[addr]; shared && exported {
					continue
				}
				extra[addr] = id
				m.adoptedByID[addr] = append(m.adoptedByID[addr], env)
			}
		}
	}
	maps.Copy(extra, m.req.ImportIDs[env])
	if !source && !shared && len(extra) == 0 {
		return
	}

	ids := map[string]cty.Value{}
	saved := cur.Values["import_ids"]
	if !saved.IsNull() && saved.CanIterateElements() {
		for it := saved.ElementIterator(); it.Next(); {
			k, v := it.Element()
			ids[k.AsString()] = v
		}
	}
	changed := false
	if source {
		changed = m.addSourceImports(ids, "")
	}
	for addr, id := range extra {
		key := m.l.ModuleAddress(addr)
		value := cty.StringVal(id)
		if old, ok := ids[key]; !ok || !old.RawEquals(value) {
			ids[key] = value
			changed = true
		}
	}
	if shared && m.addSourceImports(ids, env) {
		changed = true
	}
	if changed {
		set["import_ids"] = cty.ObjectVal(ids)
	}
}

// A target edit must not leave an old tenant's adoption hints in Git for
// a later request. Resolve them from verified source IDs for a shared
// tenant, or block until the operator supplies target-specific values.
func (m *merger) retargetHints(env string, cur *overlay.Overlay, set map[string]cty.Value, known map[string]string, sharesSource bool) {
	hints := map[string]string{}
	if sharesSource {
		for addr, id := range m.req.Export.SourceIDs {
			if simpleAdoptionID(addr) {
				hints[neutralize.ExcludedVariableName(addr)] = id
			}
		}
	}
	addresses := map[string]string{}
	for addr := range known {
		addresses[neutralize.ExcludedVariableName(addr)] = addr
	}
	// Without a saved target, an existing explicit import entry can
	// confirm the corresponding hint. Never reuse it across a target change.
	var savedImports map[string]cty.Value
	ids := cur.Values["import_ids"]
	if cur.Values[neutralize.EnvironmentIDVariable].IsNull() && !ids.IsNull() && (ids.Type().IsObjectType() || ids.Type().IsMapType()) {
		savedImports = ids.AsValueMap()
	}
	for name, old := range cur.Values {
		if name == neutralize.EnvironmentIDVariable || !strings.HasPrefix(name, "pingone_") || !strings.HasSuffix(name, "_id") ||
			old.Type() != cty.String || old.IsNull() || old.AsString() == "" {
			continue
		}
		addr := addresses[name]
		supplied, confirmed := m.req.EnvValues[env][name]
		if confirmed && supplied.Type() == cty.String && !supplied.IsNull() && supplied.AsString() != "" {
			continue
		}
		if id := hints[name]; id != "" {
			set[name] = cty.StringVal(id)
			continue
		}
		if addr != "" {
			if id := m.req.ImportIDs[env][addr]; id != "" {
				set[name] = cty.StringVal(id)
				continue
			}
			id := savedImports[m.l.ModuleAddress(addr)]
			if !id.IsNull() && id.Type() == cty.String && id.AsString() != "" {
				set[name] = id
				continue
			}
		}
		m.res.Blockers = append(m.res.Blockers, Blocker{Env: env, Code: BlockMissingValue, Address: addr, Variable: name,
			Message: "Confirm the target-specific ID for " + name + " in " + env + " before changing the rollout target. Remove an obsolete hint from the overlay in Git."})
	}
}

// targetsExportedEnvironment compares the effective rollout overlay with the
// environment actually exported. Pipeline-supplied IDs are unknown here.
func (m *merger) targetsExportedEnvironment(env string, values map[string]cty.Value) bool {
	const key = neutralize.EnvironmentIDVariable
	if m.l.FromCI(key) {
		return false
	}
	actual := values[key]
	if override, ok := m.req.EnvValues[env][key]; ok {
		actual = override
	}
	return sameEnvironmentID(actual, m.req.Export.SourceValues[key])
}

func sameEnvironmentID(actual, source cty.Value) bool {
	return actual.Type() == cty.String && actual.IsKnown() && !actual.IsNull() &&
		source.Type() == cty.String && source.IsKnown() && !source.IsNull() &&
		source.AsString() != "" && strings.EqualFold(actual.AsString(), source.AsString())
}

// addSourceImports fills missing imports and reports shared-target conflicts.
func (m *merger) addSourceImports(ids map[string]cty.Value, rollout string) bool {
	changed := false
	for a := range m.written {
		id, ok := m.req.Export.SourceIDs[a]
		key := m.l.ModuleAddress(a)
		if !ok || !importable(a) {
			continue
		}
		if old, exists := ids[key]; exists {
			if rollout != "" && !old.RawEquals(cty.StringVal(id)) {
				fix := "Correct import_ids in the rollout overlay in Git and preview again."
				if m.req.ImportIDs[rollout][a] != "" {
					fix = "Correct or clear the import ID supplied in this request and preview again."
				}
				m.res.Blockers = append(m.res.Blockers, Blocker{Env: rollout, Code: BlockConflict, Address: a,
					Message: "The rollout targets the exported environment, but its import ID for " + a + " differs from the exported ID. " + fix})
			}
		} else {
			ids[key] = cty.StringVal(id)
			changed = true
		}
	}
	return changed
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

// adoption reports built-in objects that an environment has no import ID
// for. The source environment's IDs come from the export.
func (m *merger) adoption() ([]Blocker, error) {
	var addresses []string
	for _, addr := range m.req.Adopt {
		if importable(addr) && m.written[addr] {
			addresses = append(addresses, addr)
		}
	}
	if len(addresses) == 0 {
		return nil, nil
	}
	var out []Blocker
	for _, env := range m.envs() {
		ids, err := m.savedImportIDs(env)
		if err != nil {
			return nil, err
		}
		for _, addr := range addresses {
			if ids[m.l.ModuleAddress(addr)] == "" {
				out = append(out, Blocker{Env: env, Code: BlockMissingImport, Address: addr, Variable: "import_ids",
					Message: addr + " already exists in every environment (a built-in object); enter its ID for " + env + " so it is adopted, not created"})
			}
		}
	}
	return out, nil
}

// savedImportIDs reads each environment overlay once for the adoption check.
func (m *merger) savedImportIDs(env string) (map[string]string, error) {
	p, err := m.l.Overlay(env)
	if err != nil {
		return nil, err
	}
	src, exists, err := m.view.Read(p)
	if err != nil || !exists {
		return nil, err
	}
	o, err := overlay.ParseOverlay(src, p)
	if err != nil {
		return nil, err
	}
	ids := o.Values["import_ids"]
	if ids.IsNull() || !ids.CanIterateElements() {
		return nil, nil
	}
	out := map[string]string{}
	for it := ids.ElementIterator(); it.Next(); {
		k, v := it.Element()
		if v.Type() == cty.String && !v.IsNull() {
			out[k.AsString()] = v.AsString()
		}
	}
	return out, nil
}

// A scalar reference can supply an import ID only for single-ID resources.
func simpleAdoptionID(addr string) bool {
	typ, _, _ := strings.Cut(addr, ".")
	format := catalog.ImportFormat(typ)
	return typ != neutralize.TypeFlowPolicy && (format == catalog.ImportID || format == "")
}

// knownIDs lists newly added addresses whose ID the environment's settings
// already hold in the ID variable an earlier request used for them, for
// types imported by that ID alone.
func (m *merger) knownIDs(cur *overlay.Overlay) map[string]string {
	out := map[string]string{}
	for a := range m.written {
		if _, inHead := m.head.texts[a]; inHead || !importable(a) {
			continue
		}
		if !simpleAdoptionID(a) {
			continue
		}
		v, ok := cur.Values[neutralize.ExcludedVariableName(a)]
		if ok && !v.IsNull() && v.Type() == cty.String && v.AsString() != "" {
			out[a] = v.AsString()
		}
	}
	return out
}
