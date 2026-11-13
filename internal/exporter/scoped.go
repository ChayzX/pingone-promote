package exporter

import (
	"context"
	"errors"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/zclconf/go-cty/cty"

	"pingone-promote/internal/hclblocks"
	"pingone-promote/internal/neutralize"
)

// Resource is a searchable resource of the source environment.
type Resource struct {
	Address
	// Name is decoded from the label (exact when IDs were fetched, because
	// it is then read from the block's name attribute).
	Name string `json:"name"`
	// ID is the DaVinci ID, when the catalog was built with IDs.
	ID string `json:"id,omitempty"`
}

// Catalog lists the source environment's resources. --list-resources
// returns addresses only, so names are decoded from labels. With withIDs,
// a second, flows-only export with --include-imports supplies flow IDs and
// exact names.
func (r *Runner) Catalog(ctx context.Context, creds Credentials, withIDs bool) ([]Resource, error) {
	return r.catalog(ctx, creds, withIDs, false)
}

// CatalogAllIDs is Catalog with IDs and exact names for every resource type
// (one whole-environment export with import blocks, values skipped). The
// request job needs it only to resolve an exclusion by ID for a type other
// than a flow.
func (r *Runner) CatalogAllIDs(ctx context.Context, creds Credentials) ([]Resource, error) {
	return r.catalog(ctx, creds, true, true)
}

func (r *Runner) catalog(ctx context.Context, creds Credentials, withIDs, allTypes bool) ([]Resource, error) {
	if allTypes {
		// The whole export lists every resource itself: no separate
		// --list-resources run (a second full read of DaVinci).
		exp, err := r.Export(ctx, creds, ExportOptions{All: true, Imports: true, SkipValues: true})
		if err != nil {
			return nil, err
		}
		return resourcesOf(exp)
	}
	list, err := r.ListResources(ctx, creds)
	if err != nil {
		return nil, err
	}
	out := make([]Resource, 0, len(list))
	for _, a := range list {
		name, _ := neutralize.DecodeLabel(a.Label)
		out = append(out, Resource{Address: a, Name: name})
	}
	if !withIDs {
		return out, nil
	}
	opts := ExportOptions{Include: []string{neutralize.TypeFlow + ".*"}, Imports: true, SkipValues: true}
	if allTypes {
		opts = ExportOptions{All: true, Imports: true, SkipValues: true}
	}
	exp, err := r.Export(ctx, creds, opts)
	if err != nil {
		return nil, err
	}
	ids, names, err := ExportIDs(exp)
	if err != nil {
		return nil, err
	}
	for i := range out {
		a := out[i].String()
		if id, ok := ids[a]; ok {
			out[i].ID = id
		}
		if n, ok := names[a]; ok {
			out[i].Name = n
		}
	}
	return out, nil
}

// resourcesOf lists an export's resources with their IDs and exact names.
func resourcesOf(exp *Export) ([]Resource, error) {
	addrs, err := Addresses(exp)
	if err != nil {
		return nil, err
	}
	ids, names, err := ExportIDs(exp)
	if err != nil {
		return nil, err
	}
	out := make([]Resource, 0, len(addrs))
	for _, s := range addrs {
		typ, label, _ := strings.Cut(s, ".")
		a := Address{Type: typ, Label: label}
		name, ok := names[s]
		if !ok {
			name, _ = neutralize.DecodeLabel(label)
		}
		out = append(out, Resource{Address: a, Name: name, ID: ids[s]})
	}
	return out, nil
}

// ExportIDs reads source IDs (without the environment prefix) from an
// export's import blocks and names from its resource blocks.
func ExportIDs(e *Export) (ids, names map[string]string, err error) {
	ids, names = map[string]string{}, map[string]string{}
	prefix := "module." + e.ModuleName + "."
	for p, src := range e.Files {
		if !strings.HasSuffix(p, ".tf") || p == e.ModuleName+"-module.tf" {
			// The module call only passes variables (terraformer can
			// repeat an input there); nothing here is read from it.
			continue
		}
		body, err := hclblocks.Parse(src, p)
		if err != nil {
			return nil, nil, err
		}
		for _, b := range body.Blocks {
			switch {
			case b.Type == "import":
				to, ok1 := b.Body.Attributes["to"]
				id, ok2 := b.Body.Attributes["id"]
				if !ok1 || !ok2 {
					continue
				}
				r := to.Expr.Range()
				v, d := id.Expr.Value(nil)
				if d.HasErrors() || v.Type() != cty.String {
					continue
				}
				_, raw, _ := strings.Cut(v.AsString(), "/")
				ids[strings.TrimPrefix(string(src[r.Start.Byte:r.End.Byte]), prefix)] = raw
			case b.Type == "resource" && len(b.Labels) == 2:
				if a, ok := b.Body.Attributes["name"]; ok {
					if v, d := a.Expr.Value(nil); !d.HasErrors() && v.Type() == cty.String {
						names[b.Labels[0]+"."+b.Labels[1]] = v.AsString()
					}
				}
			}
		}
	}
	return ids, names, nil
}

var hexIDRE = regexp.MustCompile(`^[0-9a-fA-F]{32}$|^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// LooksLikeID reports whether a query is a DaVinci or PingOne ID.
func LooksLikeID(q string) bool { return hexIDRE.MatchString(strings.TrimSpace(q)) }

// Search finds flows by exact ID or case-insensitive name substring. An
// exact ID match wins. Results are sorted by name then address; callers
// must treat more than one result as ambiguous and never guess.
func Search(resources []Resource, query string) []Resource {
	return SearchType(resources, neutralize.TypeFlow, query)
}

// SearchType is Search for any resource type.
func SearchType(resources []Resource, typ, query string) []Resource {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil
	}
	var byID, byName []Resource
	lq := strings.ToLower(q)
	for _, r := range resources {
		if r.Type != typ {
			continue
		}
		if r.ID != "" && strings.EqualFold(r.ID, q) {
			byID = append(byID, r)
			continue
		}
		if strings.Contains(strings.ToLower(r.Name), lq) || strings.EqualFold(r.Label, q) {
			byName = append(byName, r)
		}
	}
	if len(byID) > 0 {
		return byID
	}
	// An exact (case-insensitive) name match beats substring matches.
	var exact []Resource
	for _, r := range byName {
		if strings.EqualFold(r.Name, q) {
			exact = append(exact, r)
		}
	}
	if len(exact) > 0 {
		byName = exact
	}
	sort.Slice(byName, func(i, j int) bool {
		if byName[i].Name != byName[j].Name {
			return byName[i].Name < byName[j].Name
		}
		return byName[i].String() < byName[j].String()
	})
	return byName
}

// Addresses lists the resource addresses in an export.
func Addresses(e *Export) ([]string, error) {
	var out []string
	for p, src := range e.Files {
		if path.Dir(p) != e.ModuleDir || !strings.HasSuffix(p, ".tf") {
			continue
		}
		blocks, err := hclblocks.Scan(src, p)
		if err != nil {
			return nil, err
		}
		for _, b := range blocks {
			if b.Type == "resource" {
				out = append(out, b.Address())
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

var globalVarRE = regexp.MustCompile(`\{\{\s*global\.variables\.([A-Za-z0-9_\-]+)\s*\}\}`)
var companyVarRE = regexp.MustCompile(`\{\{\s*global\.company\.variables\.([A-Za-z0-9_\-]+)\s*\}\}`)

// Companions are a flow's deploy and enable resources (same label).
func Companions(flowAddr string) []string {
	label := strings.TrimPrefix(flowAddr, neutralize.TypeFlow+".")
	return []string{neutralize.TypeFlowDeploy + "." + label, neutralize.TypeFlowEnable + "." + label}
}

// matchesAny reports whether an address matches a terraformer exclude
// pattern (case-insensitive glob, as internal/filter does).
func matchesAny(patterns []string, addr string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(strings.ToLower(p), strings.ToLower(addr)); ok {
			return true
		}
	}
	return false
}

// WholeExport exports every supported resource of the source environment
// (no --include-resources), minus the excluded patterns. The flow
// companions (deploy, enable) are ordinary resources of the full list, so
// no second pass is needed.
func (r *Runner) WholeExport(ctx context.Context, creds Credentials, exclude []string) (*Export, error) {
	return r.Export(ctx, creds, ExportOptions{All: true, Exclude: exclude, Imports: true})
}

// ScopedExport exports the seed resources and their upstream dependencies.
// Resources matching exclude are never exported, even when a seed refers to
// them: terraformer's explicit excludes override upstream expansion, and
// the references to them become variables.
//
// terraformer's --include-upstream follows references only upstream, so
// it never adds a flow's flow_deploy / flow_enable (they reference the
// flow), and it may miss DaVinci variables referenced only through
// {{global.variables.x}}. A second pass therefore adds the companions of
// every flow in the closure and the referenced global variables. Flow
// policies and applications are included only when they are seeds.
func (r *Runner) ScopedExport(ctx context.Context, creds Credentials, seeds, exclude []string) (*Export, error) {
	if len(seeds) == 0 {
		return nil, errors.New("exporter: nothing selected")
	}
	include := map[string]bool{}
	for _, s := range seeds {
		if !addressRE.MatchString(s) {
			return nil, errors.New("exporter: seeds must be resource addresses")
		}
		if matchesAny(exclude, s) {
			return nil, errors.New("exporter: " + s + " is both selected and excluded")
		}
		include[s] = true
		if strings.HasPrefix(s, neutralize.TypeFlow+".") {
			for _, c := range Companions(s) {
				if !matchesAny(exclude, c) {
					include[c] = true
				}
			}
		}
	}
	var exp *Export
	for pass := 0; pass < 3; pass++ {
		var err error
		exp, err = r.Export(ctx, creds, ExportOptions{Include: sortedSet(include), Exclude: exclude, IncludeUpstream: true, Imports: true})
		if err != nil {
			return nil, err
		}
		have, err := Addresses(exp)
		if err != nil {
			return nil, err
		}
		got := map[string]bool{}
		for _, a := range have {
			got[a] = true
		}
		added := false
		want := func(a string) {
			if !include[a] && !got[a] && !matchesAny(exclude, a) {
				include[a] = true
				added = true
			}
		}
		for _, a := range have {
			if strings.HasPrefix(a, neutralize.TypeFlow+".") {
				for _, c := range Companions(a) {
					want(c)
				}
			}
		}
		for p, src := range exp.Files {
			if path.Base(p) != neutralize.TypeFlow+".tf" {
				continue
			}
			// {{global.variables.x}} is a flow instance variable (terraformer
			// already follows it through depends_on); company variables are
			// {{global.company.variables.x}}.
			for _, m := range globalVarRE.FindAllSubmatch(src, -1) {
				want(neutralize.TypeVariable + "." + neutralize.Label(string(m[1]), "flowInstance"))
			}
			for _, m := range companyVarRE.FindAllSubmatch(src, -1) {
				want(neutralize.TypeVariable + "." + neutralize.Label(string(m[1]), "company"))
			}
		}
		if !added {
			return exp, nil
		}
	}
	return exp, nil
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// SelectedExport exports exactly the selected items and flow companions, without expanding dependencies.
func (r *Runner) SelectedExport(ctx context.Context, creds Credentials, seeds, exclude []string) (*Export, error) {
	if len(seeds) == 0 {
		return nil, errors.New("exporter: nothing selected")
	}
	include := map[string]bool{}
	for _, a := range seeds {
		if !addressRE.MatchString(a) || matchesAny(exclude, a) {
			return nil, errors.New("exporter: invalid or excluded selection")
		}
		include[a] = true
		if strings.HasPrefix(a, neutralize.TypeFlow+".") {
			for _, c := range Companions(a) {
				if !matchesAny(exclude, c) {
					include[c] = true
				}
			}
		}
	}
	return r.Export(ctx, creds, ExportOptions{Include: sortedSet(include), Exclude: exclude, Imports: true})
}
