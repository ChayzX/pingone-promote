package exporter

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"pingone-promote/internal/resourcecatalog"
)

// Source is the resource export contract shared by the DaVinci runner and core.
type Source interface {
	Catalog(context.Context, Credentials, bool) ([]Resource, error)
	CatalogAllIDs(context.Context, Credentials) ([]Resource, error)
	ScopedExport(context.Context, Credentials, []string, []string) (*Export, error)
	WholeExport(context.Context, Credentials, []string) (*Export, error)
}
type selectedSource interface {
	SelectedExport(context.Context, Credentials, []string, []string) (*Export, error)
}

// Composite joins provider configuration and DaVinci into one Terraform module.
// A nil DaVinci explicitly selects core-only operation. Errors from a configured
// DaVinci source, including missing entitlements, are always returned.
type Composite struct {
	Core    *CoreExporter
	DaVinci Source
}

func (c *Composite) Catalog(ctx context.Context, creds Credentials, ids bool) ([]Resource, error) {
	enabled, err := c.davinciEnabled(ctx, creds)
	if err != nil {
		return nil, err
	}
	copy := *c
	if !enabled {
		copy.DaVinci = nil
	}
	c = &copy
	var all []Resource
	if c.Core != nil {
		rs, err := c.Core.Catalog(ctx, creds, ids)
		if err != nil {
			return nil, err
		}
		all = append(all, rs...)
	}
	if c.DaVinci != nil {
		rs, err := c.DaVinci.Catalog(ctx, creds, ids)
		if err != nil {
			return nil, err
		}
		all = append(all, rs...)
	}
	return checkedCatalog(all)
}
func (c *Composite) CatalogAllIDs(ctx context.Context, creds Credentials) ([]Resource, error) {
	enabled, err := c.davinciEnabled(ctx, creds)
	if err != nil {
		return nil, err
	}
	copy := *c
	if !enabled {
		copy.DaVinci = nil
	}
	c = &copy
	var all []Resource
	if c.Core != nil {
		rs, err := c.Core.CatalogAllIDs(ctx, creds)
		if err != nil {
			return nil, err
		}
		all = append(all, rs...)
	}
	if c.DaVinci != nil {
		rs, err := c.DaVinci.CatalogAllIDs(ctx, creds)
		if err != nil {
			return nil, err
		}
		all = append(all, rs...)
	}
	return checkedCatalog(all)
}
func checkedCatalog(rs []Resource) ([]Resource, error) {
	by := map[string]Resource{}
	for _, r := range rs {
		if !resourcecatalog.Allowed(r.Type) || !addressRE.MatchString(r.String()) {
			return nil, fmt.Errorf("exporter: unsupported catalog resource %q", r.String())
		}
		if _, ok := by[r.String()]; ok {
			return nil, fmt.Errorf("exporter: duplicate catalog address %s", r.String())
		}
		by[r.String()] = r
	}
	out := make([]Resource, 0, len(by))
	for _, a := range sortedKeys(by) {
		out = append(out, by[a])
	}
	return out, nil
}
func (c *Composite) ScopedExport(ctx context.Context, creds Credentials, seeds, x []string) (*Export, error) {
	return c.run(ctx, creds, seeds, x, true, false)
}
func (c *Composite) SelectedExport(ctx context.Context, creds Credentials, seeds, x []string) (*Export, error) {
	return c.run(ctx, creds, seeds, x, false, false)
}
func (c *Composite) WholeExport(ctx context.Context, creds Credentials, x []string) (*Export, error) {
	return c.run(ctx, creds, nil, x, true, true)
}
func (c *Composite) run(ctx context.Context, creds Credentials, seeds, exclude []string, upstream, whole bool) (*Export, error) {
	enabled, probeErr := c.davinciEnabled(ctx, creds)
	if probeErr != nil {
		return nil, probeErr
	}
	copy := *c
	if !enabled {
		copy.DaVinci = nil
	}
	c = &copy
	if c.Core == nil && c.DaVinci == nil {
		return nil, errors.New("exporter: no configuration source is configured")
	}
	var raw []SourceResource
	var catalog []Resource
	if c.Core != nil {
		var err error
		raw, err = c.Core.inventory(ctx, creds)
		if err != nil {
			return nil, err
		}
		for _, r := range raw {
			catalog = append(catalog, r.Resource)
		}
	}
	if c.DaVinci != nil {
		rs, err := c.DaVinci.CatalogAllIDs(ctx, creds)
		if err != nil {
			return nil, err
		}
		catalog = append(catalog, rs...)
	}
	var err error
	catalog, err = checkedCatalog(catalog)
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, r := range catalog {
		known[r.String()] = true
	}
	selected := map[string]bool{}
	if whole {
		for _, r := range catalog {
			if !matchesAny(exclude, r.String()) {
				selected[r.String()] = true
			}
		}
	} else {
		if len(seeds) == 0 {
			return nil, errors.New("exporter: nothing selected")
		}
		for _, a := range seeds {
			if !known[a] || matchesAny(exclude, a) {
				return nil, fmt.Errorf("exporter: unknown or excluded selection %q", a)
			}
			selected[a] = true
		}
	}
	exp := &Export{EnvironmentID: creds.ExportEnvironment(), ModuleDir: ModuleDir, ModuleName: ModuleName, Files: map[string][]byte{}}
	done := map[string]bool{}
	// Each iteration must consume at least one catalog node. Cycles and duplicate
	// references cannot extend this bound or cause a repeated core import.
	for iteration := 0; iteration <= len(catalog)+1; iteration++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var coreBatch []SourceResource
		var dvBatch []string
		for _, r := range raw {
			if selected[r.String()] && !done[r.String()] {
				coreBatch = append(coreBatch, r)
				done[r.String()] = true
			}
		}
		for _, a := range sortedSet(selected) {
			if strings.HasPrefix(a, "pingone_davinci_") && !done[a] {
				dvBatch = append(dvBatch, a)
				done[a] = true
			}
		}
		if len(coreBatch) > 0 {
			part, err := c.Core.Reader.Read(ctx, creds, coreBatch)
			if err != nil {
				return nil, err
			}
			markCoreAdoption(part, coreBatch)
			exp, err = mergeExports(exp, part)
			if err != nil {
				return nil, err
			}
		}
		if len(dvBatch) > 0 {
			var part *Export
			var err error
			if whole {
				part, err = c.DaVinci.WholeExport(ctx, creds, exclude)
			} else if upstream {
				part, err = c.DaVinci.ScopedExport(ctx, creds, dvBatch, exclude)
			} else if s, ok := c.DaVinci.(selectedSource); ok {
				part, err = s.SelectedExport(ctx, creds, dvBatch, exclude)
			} else {
				return nil, errors.New("exporter: DaVinci source does not support export without dependencies")
			}
			if err != nil {
				return nil, err
			}
			exp, err = mergeExports(exp, part)
			if err != nil {
				return nil, err
			}
			have, err := Addresses(part)
			if err != nil {
				return nil, err
			}
			for _, a := range have {
				if !known[a] || matchesAny(exclude, a) {
					return nil, fmt.Errorf("exporter: source returned unknown or excluded resource %s", a)
				}
				selected[a] = true
				done[a] = true
			}
		}
		have, err := Addresses(exp)
		if err != nil {
			return nil, err
		}
		present := map[string]bool{}
		for _, a := range have {
			present[a] = true
		}
		rewritten, deps, err := rewriteGraph(exp, catalog, present, exclude)
		if err != nil {
			return nil, err
		}
		if !upstream {
			return rewritten, nil
		}
		index := newReferenceIndex(catalog)
		for _, r := range raw {
			if !selected[r.String()] {
				continue
			}
			refs, err := index.rawDependencies(r.Values, r.Type)
			if err != nil {
				return nil, err
			}
			for _, a := range refs {
				deps[a] = true
			}
		}
		added := false
		for a := range deps {
			if known[a] && !selected[a] && !matchesAny(exclude, a) {
				selected[a] = true
				added = true
			}
		}
		if !added {
			return rewritten, nil
		}
	}
	return nil, errors.New("exporter: dependency graph did not converge within the catalog bound")
}

func markCoreAdoption(e *Export, rs []SourceResource) {
	if e == nil {
		return
	}
	if e.AdoptOnly == nil {
		e.AdoptOnly = map[string]bool{}
	}
	for _, r := range rs {
		builtin, _ := r.Values["default"].(bool)
		kind, _ := r.Values["type"].(string)
		meta, _ := resourcecatalog.Lookup(r.Type)
		if builtin || (r.Type == "pingone_resource" && (kind == "OPENID_CONNECT" || kind == "PINGONE_API")) || r.Type == "pingone_population_default" || r.Type == "pingone_mfa_device_policy_default" || r.Type == "pingone_branding_theme_default" || r.Type == "pingone_system_application" || r.Type == "pingone_environment" || strings.HasSuffix(meta.Import, " <environment_id>") {
			e.AdoptOnly[r.String()] = true
		}
	}
}

type serviceInventory interface {
	ServiceEnabled(context.Context, Credentials, string) (bool, error)
}

func (c *Composite) davinciEnabled(ctx context.Context, creds Credentials) (bool, error) {
	if c.DaVinci == nil {
		return false, nil
	}
	if c.Core != nil {
		if inventory, ok := c.Core.Inventory.(serviceInventory); ok {
			return inventory.ServiceEnabled(ctx, creds, "davinci")
		}
	}
	return true, nil
}
