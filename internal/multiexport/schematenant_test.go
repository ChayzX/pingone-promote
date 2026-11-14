package multiexport

import (
	"context"
	"sync"
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

// TestConcurrentCallersShareOneRead: the item list read at startup and a
// preview that start together read the source once, not once each (a full
// read is hundreds of API calls and a terraformer run).
func TestConcurrentCallersShareOneRead(t *testing.T) {
	st := schematenant.New()
	fake := exportertest.Build(t, t.TempDir(), exportertest.FixtureDir("cross"))
	var mu sync.Mutex
	readers := 0
	e := &Exporter{
		DaVinci: &exporter.Runner{Config: exporter.Config{Path: fake.Path, SHA256: fake.SHA256, Version: fake.Version}},
		NewReader: func(exporter.Credentials) (coreexport.Reader, error) {
			mu.Lock()
			readers++
			mu.Unlock()
			return st, nil
		},
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var err error
			if i%2 == 0 {
				_, err = e.CatalogAllIDs(context.Background(), creds)
			} else {
				_, err = e.WholeExport(context.Background(), creds, nil)
			}
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if readers != 1 {
		t.Fatalf("the source was read %d times", readers)
	}
}

// TestOneTerraformerRunPerRead: the item list runs terraformer once, and
// previews (alone, with dependencies, whole) are cut from that run.
func TestOneTerraformerRunPerRead(t *testing.T) {
	fake := exportertest.Build(t, t.TempDir(), exportertest.FixtureDir("cross"))
	st := schematenant.New()
	e := &Exporter{
		DaVinci:   &exporter.Runner{Config: exporter.Config{Path: fake.Path, SHA256: fake.SHA256, Version: fake.Version}},
		NewReader: func(exporter.Credentials) (coreexport.Reader, error) { return st, nil },
	}
	items, err := e.CatalogAllIDs(context.Background(), creds)
	if err != nil {
		t.Fatal(err)
	}
	var flow string
	for _, it := range items {
		if it.Type == "pingone_davinci_flow" {
			flow = it.String()
			break
		}
	}
	if _, err := e.SelectedExport(context.Background(), creds, []string{flow}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ScopedExport(context.Background(), creds, []string{flow}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.WholeExport(context.Background(), creds, nil); err != nil {
		t.Fatal(err)
	}
	runs := 0
	for _, inv := range fake.Invocations(t) {
		if len(inv.Argv) > 1 && inv.Argv[1] == "export" {
			runs++
		}
	}
	if runs != 1 {
		t.Fatalf("terraformer exported %d times, want 1", runs)
	}
}
