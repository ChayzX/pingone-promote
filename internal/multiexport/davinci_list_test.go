package multiexport

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"pingone-promote/internal/coreexport"
	"pingone-promote/internal/coreexport/schematenant"
	"pingone-promote/internal/exporter"
	"pingone-promote/internal/exporter/exportertest"
	"pingone-promote/internal/neutralize"
)

// apiReader answers the DaVinci list endpoints from a fixed list (as the
// API would) and everything else from the generated tenant.
type apiReader struct {
	coreexport.Reader
	flows, vars, conns []map[string]any
	updated            atomic.Value // string: updatedAt of every item
}

func (a *apiReader) List(ctx context.Context, env, path, embedded string) ([]map[string]any, error) {
	stamp := func(items []map[string]any) []map[string]any {
		out := make([]map[string]any, len(items))
		for i, it := range items {
			m := map[string]any{}
			for k, v := range it {
				m[k] = v
			}
			m["updatedAt"] = a.updated.Load()
			out[i] = m
		}
		return out
	}
	switch {
	case path == "flows":
		return stamp(a.flows), nil
	case path == "variables":
		return stamp(a.vars), nil
	case path == "connectorInstances":
		return stamp(a.conns), nil
	case path == "davinciApplications":
		return nil, nil
	}
	return a.Reader.List(ctx, env, path, embedded)
}

// gatedDaVinci holds the terraformer export until released and counts runs.
type gatedDaVinci struct {
	*exporter.Runner
	gate chan struct{}
	runs atomic.Int32
}

func (g *gatedDaVinci) CatalogSource(ctx context.Context, c exporter.Credentials) ([]exporter.Resource, *exporter.Source, error) {
	g.runs.Add(1)
	if g.gate != nil {
		<-g.gate
	}
	return g.Runner.CatalogSource(ctx, c)
}

func quickSetup(t *testing.T) (*Exporter, *apiReader, *gatedDaVinci, []exporter.Resource) {
	t.Helper()
	fake := exportertest.Build(t, t.TempDir(), exportertest.FixtureDir("cross"))
	runner := &exporter.Runner{Config: exporter.Config{Path: fake.Path, SHA256: fake.SHA256, Version: fake.Version}}
	full, _, err := runner.CatalogSource(context.Background(), creds)
	if err != nil {
		t.Fatal(err)
	}
	api := &apiReader{Reader: schematenant.New()}
	api.updated.Store("2026-01-01T00:00:00Z")
	for _, r := range full {
		switch r.Type {
		case neutralize.TypeFlow:
			// The API name gives another label than terraformer's here:
			// selections must still find the flow by ID.
			api.flows = append(api.flows, map[string]any{"id": r.ID, "name": r.Name + " (renamed)"})
		case neutralize.TypeConnector:
			api.conns = append(api.conns, map[string]any{"id": r.ID, "name": r.Name})
		}
	}
	if len(api.flows) == 0 {
		t.Fatal("the fixture has no flows")
	}
	dv := &gatedDaVinci{Runner: runner}
	e := &Exporter{DaVinci: dv, NewReader: func(exporter.Credentials) (coreexport.Reader, error) { return api, nil }}
	return e, api, dv, full
}

// The item list comes from the API while terraformer still runs; a flow
// picked from it is exported (matched by ID) once terraformer is in.
func TestQuickDaVinciListThenExportByID(t *testing.T) {
	e, _, dv, _ := quickSetup(t)
	dv.gate = make(chan struct{})
	start := time.Now()
	items, err := e.CatalogAllIDs(context.Background(), creds)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("the item list waited for terraformer")
	}
	var flow string
	for _, it := range items {
		if it.Type == neutralize.TypeFlow {
			flow = it.String()
		}
	}
	if !strings.Contains(flow, "renamed") {
		t.Fatalf("the item list does not come from the API: %q", flow)
	}
	go func() { time.Sleep(100 * time.Millisecond); close(dv.gate) }()
	exp, err := e.SelectedExport(context.Background(), creds, []string{flow}, nil)
	if err != nil {
		t.Fatal(err)
	}
	addrs, err := exporter.Addresses(exp)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range addrs {
		if strings.HasPrefix(a, neutralize.TypeFlow+".") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the flow picked from the quick list was not exported: %v", addrs)
	}
}

// A refresh with DaVinci unchanged reuses the export; a change reruns it.
func TestUnchangedDaVinciSkipsTerraformer(t *testing.T) {
	e, api, dv, _ := quickSetup(t)
	if _, err := e.CatalogAllIDs(context.Background(), creds); err != nil {
		t.Fatal(err)
	}
	if _, err := e.WholeExport(context.Background(), creds, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Refresh(context.Background(), creds); err != nil {
		t.Fatal(err)
	}
	if _, err := e.WholeExport(context.Background(), creds, nil); err != nil {
		t.Fatal(err)
	}
	if n := dv.runs.Load(); n != 1 {
		t.Fatalf("terraformer ran %d times for an unchanged DaVinci, want 1", n)
	}
	api.updated.Store("2026-02-02T00:00:00Z")
	if _, err := e.Refresh(context.Background(), creds); err != nil {
		t.Fatal(err)
	}
	if _, err := e.WholeExport(context.Background(), creds, nil); err != nil {
		t.Fatal(err)
	}
	if n := dv.runs.Load(); n != 2 {
		t.Fatalf("terraformer ran %d times after a change, want 2", n)
	}
}

// A read again while terraformer still runs shares that run when DaVinci is
// unchanged, and starts another when it changed.
func TestReadAgainSharesTheRunningExport(t *testing.T) {
	e, api, dv, _ := quickSetup(t)
	dv.gate = make(chan struct{})
	if _, err := e.CatalogAllIDs(context.Background(), creds); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Refresh(context.Background(), creds); err != nil {
		t.Fatal(err)
	}
	api.updated.Store("2026-03-03T00:00:00Z")
	if _, err := e.Refresh(context.Background(), creds); err != nil {
		t.Fatal(err)
	}
	close(dv.gate)
	exp, err := e.WholeExport(context.Background(), creds, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(exp.Files) == 0 {
		t.Fatal("the shared export is empty")
	}
	if n := dv.runs.Load(); n != 2 {
		t.Fatalf("terraformer ran %d times, want 2 (one shared, one after the change)", n)
	}
}

// IDs in connector properties become references, or ID variables when the
// item is not in the export; unknown IDs are reported.
func TestConnectorPropertyIDs(t *testing.T) {
	const env = "12e2ce5a-d22b-4ac4-802f-060df245995d"
	src := `resource "pingone_davinci_connector_instance" "pingcli__Http" {
  properties = jsonencode({
    "connectionId" : {
      "value" : "3b55f2ca6689560c64cb5bed5afbe40f"
    },
    "clientId" : {
      "value" : "a2beb5d1-e874-42da-9149-8d1263a1ffdd"
    },
    "envId" : {
      "value" : "` + env + `"
    },
    "other" : {
      "value" : "ffffffffffffffffffffffffffffffff"
    },
    "viaVariable" : {
      "value" : var.davinci_connection_Http_connectionId
    },
    "plain" : {
      "value" : var.davinci_connection_Http_region
    }
  })
}
`
	out := &exporter.Export{EnvironmentID: env, ModuleDir: exporter.ModuleDir, Files: map[string][]byte{exporter.ModuleDir + "/pingone_davinci_connector_instance.tf": []byte(src),
		"ping-export-terraform.auto.tfvars": []byte("davinci_connection_Http_connectionId = \"3b55f2ca6689560c64cb5bed5afbe40f\"\ndavinci_connection_Http_region = \"NA\"\n")}}
	ids := map[string]string{"3b55f2ca6689560c64cb5bed5afbe40f": "pingone_davinci_connector_instance.pingcli__Token", "a2beb5d1-e874-42da-9149-8d1263a1ffdd": "pingone_application.pingcli__App"}
	connectorRefs(out, ids, map[string]bool{"pingone_davinci_connector_instance.pingcli__Token": true})
	got := string(out.Files[exporter.ModuleDir+"/pingone_davinci_connector_instance.tf"])
	for _, want := range []string{`"value" : pingone_davinci_connector_instance.pingcli__Token.id`, `"value" : pingone_application.pingcli__App.id`, `"value" : "` + env + `"`, `"ffffffffffffffffffffffffffffffff"`} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %s:\n%s", want, got)
		}
	}
	if strings.Count(got, "pingone_davinci_connector_instance.pingcli__Token.id") != 2 || !strings.Contains(got, "var.davinci_connection_Http_region") {
		t.Errorf("an ID set through a variable is not a reference, or another variable changed:\n%s", got)
	}
	if out.Referenced["pingone_application.pingcli__App"] != "a2beb5d1-e874-42da-9149-8d1263a1ffdd" {
		t.Errorf("the application outside the export is not referenced by ID variable: %v", out.Referenced)
	}
	if len(out.Warnings) != 1 || !strings.Contains(out.Warnings[0], "ffffffffffffffffffffffffffffffff") {
		t.Errorf("warnings %v", out.Warnings)
	}
}

// A DaVinci object value holding a secret-looking string is not read.
func TestSecretInsideDaVinciValues(t *testing.T) {
	for _, x := range []any{
		map[string]any{"auth": "Bearer abcdefghijklmnopqrstuvwxyz0123"},
		map[string]any{"password": "hunter2hunter2"},
		[]any{map[string]any{"k": "ghp_abcdefghijklmnopqrstuvwxyz0123456789"}},
	} {
		if !secretInside(x) {
			t.Errorf("not caught: %v", x)
		}
	}
	for _, x := range []any{map[string]any{"CSRauth": true, "region": "us"}, 0.0, true, []any{"a", "b"}} {
		if secretInside(x) {
			t.Errorf("flagged: %v", x)
		}
	}
}
