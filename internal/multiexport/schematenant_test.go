package multiexport

import (
	"context"
	"testing"

	"pingone-promote/internal/catalog"
	"pingone-promote/internal/coreexport"
	"pingone-promote/internal/coreexport/schematenant"
	"pingone-promote/internal/exporter"
	"pingone-promote/internal/exporter/exportertest"
)

// TestItemListHasEveryTypeOfAFullTenant: the item list a person searches
// has every core type a full tenant holds (built-ins included, so they can
// be picked) and the DaVinci items.
func TestItemListHasEveryTypeOfAFullTenant(t *testing.T) {
	st := schematenant.New()
	fake := exportertest.Build(t, t.TempDir(), exportertest.FixtureDir("cross"))
	e := &Exporter{
		DaVinci:   &exporter.Runner{Config: exporter.Config{Path: fake.Path, SHA256: fake.SHA256, Version: fake.Version}},
		NewReader: func(exporter.Credentials) (coreexport.Reader, error) { return st, nil },
	}
	items, err := e.CatalogAllIDs(context.Background(), creds)
	if err != nil {
		t.Fatal(err)
	}
	if p := e.DaVinciProblem(creds); p != nil {
		t.Fatalf("DaVinci items missing: %v", p)
	}
	got := map[string]int{}
	for _, it := range items {
		got[it.Type]++
	}
	for _, d := range catalog.All() {
		if d.Exporter == catalog.ExporterAPI && got[d.Type] == 0 {
			t.Errorf("%s is not in the item list", d.Type)
		}
	}
	if got["pingone_davinci_flow"] == 0 {
		t.Error("no DaVinci flows in the item list")
	}
	t.Logf("%d items of %d types", len(items), len(got))
}

// TestEverySingleItemExports picks each item of a full tenant alone, with
// and without what it depends on, and neutralizes the result: no item may
// fail to export on its own.
func TestEverySingleItemExports(t *testing.T) {
	st := schematenant.New()
	fake := exportertest.Build(t, t.TempDir(), exportertest.FixtureDir("cross"))
	e := &Exporter{
		DaVinci:   &exporter.Runner{Config: exporter.Config{Path: fake.Path, SHA256: fake.SHA256, Version: fake.Version}},
		NewReader: func(exporter.Credentials) (coreexport.Reader, error) { return st, nil },
	}
	items, err := e.CatalogAllIDs(context.Background(), creds)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		a := it.Type + "." + it.Label
		for _, upstream := range []bool{false, true} {
			exp, err := e.export(context.Background(), creds, []string{a}, nil, upstream, false)
			if err != nil {
				t.Errorf("%s (upstream %v): %v", a, upstream, err)
				continue
			}
			neutralized(t, exp)
		}
	}
}
