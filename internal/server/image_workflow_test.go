package server

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"pingone-promote/internal/exporter"
	"pingone-promote/internal/exporter/exportertest"
	"pingone-promote/internal/gitlab"
)

const explicitImageID = "99999999-9999-4999-8999-999999999999"
const explicitImageAddress = "pingone_image.brand_logo"

type explicitImageInventory struct{ lookups atomic.Int32 }

func (*explicitImageInventory) List(context.Context, exporter.Credentials) ([]exporter.SourceResource, error) {
	return nil, nil
}
func (i *explicitImageInventory) Lookup(_ context.Context, c exporter.Credentials, typ, id string) (exporter.SourceResource, error) {
	i.lookups.Add(1)
	if typ != "pingone_image" || id != explicitImageID {
		return exporter.SourceResource{}, errors.New("unexpected image lookup")
	}
	return exporter.SourceResource{Resource: exporter.Resource{Address: exporter.Address{Type: typ, Label: "brand_logo"}, Name: "Brand logo", ID: id}, ImportID: c.ExportEnvironment() + "/" + id, Values: map[string]any{"id": id, "environment": map[string]any{"id": c.ExportEnvironment()}}}, nil
}

type explicitImageReader struct{ reads atomic.Int32 }

func (r *explicitImageReader) Read(_ context.Context, c exporter.Credentials, rs []exporter.SourceResource) (*exporter.Export, error) {
	r.reads.Add(1)
	if len(rs) != 1 || rs[0].String() != explicitImageAddress {
		return nil, errors.New("image selection widened")
	}
	return &exporter.Export{EnvironmentID: c.ExportEnvironment(), ModuleDir: exporter.ModuleDir, ModuleName: exporter.ModuleName, Files: map[string][]byte{
		exporter.ModuleDir + "/variables.tf":     []byte("variable \"pingone_environment_id\" { type=string }\n"),
		exporter.ModuleDir + "/pingone_image.tf": []byte("resource \"pingone_image\" \"brand_logo\" {\n environment_id=var.pingone_environment_id\n image_file_base64=\"iVBORw0KGgoAAAANSUhEUgAAAAEAAAAB\"\n}\n"),
		"imports.tf":                             []byte("import {\n to=module." + exporter.ModuleName + "." + explicitImageAddress + "\n id=\"" + rs[0].ImportID + "\"\n}\n"),
	}}, nil
}

func TestHostedUnreferencedImageIDSurvivesSearchAndPreview(t *testing.T) {
	fake := exportertest.Build(t, t.TempDir(), exportertest.FixtureDir("dev"))
	for _, upstream := range []bool{false, true} {
		t.Run(map[bool]string{false: "selected_only", true: "upstream"}[upstream], func(t *testing.T) {
			h := newHTTPHarness(t, fake)
			inventory := &explicitImageInventory{}
			reader := &explicitImageReader{}
			h.s.deps.Exporter = &exporter.Composite{Core: &exporter.CoreExporter{Inventory: inventory, Reader: reader}}
			form := url.Values{"source": {"dev"}, "name": {"Image operator"}, "scope": {"selected"}, "mode": {"branch"}}
			if upstream {
				form.Set("upstream", "yes")
			}
			w := h.post("/drafts", form)
			id := strings.TrimPrefix(w.Header().Get("Location"), "/drafts/")
			h.action(id, "search", url.Values{"query": {explicitImageID}})
			d := h.wait(id)
			if d.State != "editing" || len(d.Catalog) != 1 || d.Catalog[0].String() != explicitImageAddress {
				t.Fatalf("explicit image search: state=%s message=%s catalog=%v", d.State, d.Message, d.Catalog)
			}
			h.action(id, "selection", url.Values{"resource": {explicitImageAddress}})
			d = h.draft(id)
			if len(d.Request.Resources) != 1 || d.Request.Resources[0].Type != "pingone_image" || d.Request.Resources[0].ID != explicitImageID {
				t.Fatal("image selector lost ID at form boundary")
			}
			h.action(id, "preview", nil)
			d = h.wait(id)
			if d.State != "ready" || len(d.Report.Changes) != 1 || d.Report.Changes[0].Address != explicitImageAddress {
				t.Fatalf("image preview: state=%s report=%+v", d.State, d.Report)
			}
			if reader.reads.Load() != 1 || inventory.lookups.Load() < 2 {
				t.Fatal("image selection was not reconstructed through the request engine")
			}
			h.action(id, "submit", nil)
			d = h.wait(id)
			if d.State != "submitted" || len(h.gl.MRs) != 0 {
				t.Fatal("image branch publication failed")
			}
			commit := h.gl.Commits[d.Report.CommitID]
			if commit == nil {
				t.Fatal("image commit missing")
			}
			for _, a := range commit.Actions {
				if a["file_path"] == gitlab.RequestFile || strings.HasSuffix(a["file_path"], ".auto.tfvars") {
					t.Fatal("image publication included transport data")
				}
			}
		})
	}
}

func TestHostedImageSelectorsSurviveLaterSearch(t *testing.T) {
	fake := exportertest.Build(t, t.TempDir(), exportertest.FixtureDir("dev"))
	h := newHTTPHarness(t, fake)
	inventory := &explicitImageInventory{}
	h.s.deps.Exporter = &exporter.Composite{Core: &exporter.CoreExporter{Inventory: inventory, Reader: &explicitImageReader{}}}
	// An exclusion must be retained just as an inclusion is, including for a
	// whole-environment request whose ordinary list cannot enumerate images.
	for _, field := range []string{"resource", "exclude"} {
		form := url.Values{"source": {"dev"}, "name": {"Image operator"}, "scope": {"selected"}, "mode": {"branch"}}
		if field == "exclude" {
			form.Set("scope", "all")
			form.Set("confirmAll", "yes")
		}
		w := h.post("/drafts", form)
		id := strings.TrimPrefix(w.Header().Get("Location"), "/drafts/")
		h.action(id, "search", url.Values{"query": {explicitImageID}})
		h.wait(id)
		h.action(id, "selection", url.Values{field: {explicitImageAddress}})
		h.action(id, "search", url.Values{"query": {"different name"}})
		d := h.wait(id)
		if d.State != "editing" || len(d.Catalog) != 1 {
			t.Fatal("later search lost explicitly selected image")
		}
		refs := d.Request.Resources
		if field == "exclude" {
			refs = d.Request.Exclude
		}
		if !selectionContains(d, refs, explicitImageAddress) {
			t.Fatal("image checkbox state lost typed selector")
		}
		page := h.call("GET", "/drafts/"+id, nil)
		if !strings.Contains(page.Body.String(), `type="hidden" name="`+field+`" value="`+explicitImageAddress+`"`) {
			t.Fatal("filtered selection was not retained in form")
		}
		h.action(id, "selection", url.Values{field: {explicitImageAddress}})
	}
}

type missingImageInventory struct{ explicitImageInventory }

func (*missingImageInventory) List(_ context.Context, c exporter.Credentials) ([]exporter.SourceResource, error) {
	return []exporter.SourceResource{{Resource: exporter.Resource{Address: exporter.Address{Type: "pingone_population", Label: "customers"}, Name: "Customers", ID: "33333333-3333-4333-8333-333333333333"}, ImportID: c.ExportEnvironment() + "/33333333-3333-4333-8333-333333333333"}}, nil
}

func (*missingImageInventory) Lookup(context.Context, exporter.Credentials, string, string) (exporter.SourceResource, error) {
	return exporter.SourceResource{}, &exporter.InventoryHTTPError{ResourceType: "pingone_image", StatusCode: 404}
}
func TestHostedUnknownImageIDKeepsOrdinaryCatalog(t *testing.T) {
	fake := exportertest.Build(t, t.TempDir(), exportertest.FixtureDir("dev"))
	h := newHTTPHarness(t, fake)
	h.s.deps.Exporter = &exporter.Composite{Core: &exporter.CoreExporter{Inventory: &missingImageInventory{}, Reader: &explicitImageReader{}}}
	id := h.create("branch")
	h.action(id, "search", url.Values{"query": {explicitImageID}})
	d := h.wait(id)
	if d.State != "editing" || len(d.Catalog) != 1 || d.Catalog[0].String() != "pingone_population.customers" {
		t.Fatal("unknown image ID should retain the ordinary catalog")
	}
}
