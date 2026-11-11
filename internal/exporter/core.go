package exporter

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"pingone-promote/internal/resourcecatalog"
)

// StateReader imports configuration resources into disposable provider state.
// Implementations must never persist source state or secret values.
type StateReader interface {
	Read(context.Context, Credentials, []SourceResource) (*Export, error)
}

// CoreExporter inventories configuration through the PingOne API and renders it
// through the pinned provider. DaVinci resources are handled by Composite.
type CoreExporter struct {
	Inventory InventoryReader
	Reader    StateReader
}

func (c *CoreExporter) inventory(ctx context.Context, creds Credentials) ([]SourceResource, error) {
	if c == nil || c.Inventory == nil || c.Reader == nil {
		return nil, errors.New("exporter: core inventory and provider state reader are required")
	}
	rs, err := c.Inventory.List(ctx, creds)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, r := range rs {
		if !resourcecatalog.Allowed(r.Type) || strings.HasPrefix(r.Type, "pingone_davinci_") {
			return nil, fmt.Errorf("exporter: inventory contains unsupported configuration type %q", r.Type)
		}
		if !addressRE.MatchString(r.String()) || r.ImportID == "" || r.ID == "" {
			return nil, errors.New("exporter: inventory contains an invalid resource identity")
		}
		if seen[r.String()] {
			return nil, fmt.Errorf("exporter: duplicate inventory address %s", r.String())
		}
		seen[r.String()] = true
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i].String() < rs[j].String() })
	return rs, nil
}
func (c *CoreExporter) Catalog(ctx context.Context, creds Credentials, withIDs bool) ([]Resource, error) {
	rs, err := c.inventory(ctx, creds)
	if err != nil {
		return nil, err
	}
	out := make([]Resource, 0, len(rs))
	for _, r := range rs {
		v := r.Resource
		if !withIDs {
			v.ID = ""
		}
		out = append(out, v)
	}
	return out, nil
}
func (c *CoreExporter) CatalogAllIDs(ctx context.Context, creds Credentials) ([]Resource, error) {
	return c.Catalog(ctx, creds, true)
}
func (c *CoreExporter) ScopedExport(ctx context.Context, creds Credentials, seeds, exclude []string) (*Export, error) {
	return (&Composite{Core: c}).ScopedExport(ctx, creds, seeds, exclude)
}
func (c *CoreExporter) SelectedExport(ctx context.Context, creds Credentials, seeds, exclude []string) (*Export, error) {
	return (&Composite{Core: c}).SelectedExport(ctx, creds, seeds, exclude)
}
func (c *CoreExporter) WholeExport(ctx context.Context, creds Credentials, exclude []string) (*Export, error) {
	return (&Composite{Core: c}).WholeExport(ctx, creds, exclude)
}
