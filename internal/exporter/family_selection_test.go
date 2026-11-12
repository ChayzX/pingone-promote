package exporter

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

// recordingCoreSchemaReader preserves the pinned-schema/state proof while
// exposing which resources the exporter hands to the import/render step.
type recordingCoreSchemaReader struct {
	coreSchemaReader
	readAddresses []string
}

func (r *recordingCoreSchemaReader) Read(ctx context.Context, c Credentials, rs []SourceResource) (*Export, error) {
	for _, resource := range rs {
		r.readAddresses = append(r.readAddresses, resource.String())
	}
	return r.coreSchemaReader.Read(ctx, c, rs)
}

func TestCoreEachFamilyCanBeSelectedIndividually(t *testing.T) {
	schema, err := os.ReadFile("exportertest/fixtures/core/schema.json")
	if err != nil {
		t.Fatal(err)
	}
	state, err := os.ReadFile("exportertest/fixtures/core/state.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Values struct {
			Root struct {
				Resources []stateResource `json:"resources"`
			} `json:"root_module"`
		} `json:"values"`
	}
	if err := json.Unmarshal(state, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Values.Root.Resources) != 22 {
		t.Fatalf("pinned state contains %d resources, want 22", len(doc.Values.Root.Resources))
	}
	var inv coreInventoryFixture
	for _, resource := range doc.Values.Root.Resources {
		id, ok := resource.Values["id"].(string)
		if !ok || id == "" {
			t.Fatalf("fixture resource %s has no string ID", resource.Address)
		}
		inv = append(inv, coreObject(resource.Type, "fixture", id, resource.Values))
	}

	// The fixture's reference-looking values use the label "fixture", while
	// each inventory ID is source-XX. Thus these real provider fixtures contain
	// no resolvable cross-resource references; dependency closure is covered by
	// TestCoreDependencyClosureAndExclusions with an explicit synthetic graph.
	for _, resource := range doc.Values.Root.Resources {
		address := resource.Address
		t.Run(address, func(t *testing.T) {
			for _, mode := range []struct {
				name string
				run  func(*CoreExporter) (*Export, error)
			}{
				{"selected", func(c *CoreExporter) (*Export, error) {
					return c.SelectedExport(context.Background(), Credentials{}, []string{address}, nil)
				}},
				{"scoped", func(c *CoreExporter) (*Export, error) {
					return c.ScopedExport(context.Background(), Credentials{}, []string{address}, nil)
				}},
			} {
				t.Run(mode.name, func(t *testing.T) {
					reader := &recordingCoreSchemaReader{coreSchemaReader: coreSchemaReader{schema: schema, state: state}}
					c := &CoreExporter{Inventory: inv, Reader: reader}
					export, err := mode.run(c)
					if err != nil {
						t.Fatal(err)
					}
					assertCoreAddresses(t, export, address)
					if len(reader.readAddresses) != 1 || reader.readAddresses[0] != address {
						t.Fatalf("reader received %v, want only %s", reader.readAddresses, address)
					}
				})
			}

			t.Run("whole exclusion precedes import", func(t *testing.T) {
				reader := &recordingCoreSchemaReader{coreSchemaReader: coreSchemaReader{schema: schema, state: state}}
				c := &CoreExporter{Inventory: inv, Reader: reader}
				export, err := c.WholeExport(context.Background(), Credentials{}, []string{address})
				if err != nil {
					t.Fatal(err)
				}
				for _, imported := range reader.readAddresses {
					if imported == address {
						t.Fatalf("excluded resource %s reached provider state reader", address)
					}
				}
				if len(reader.readAddresses) != len(inv)-1 {
					t.Fatalf("reader received %d resources, want %d", len(reader.readAddresses), len(inv)-1)
				}
				addresses, err := Addresses(export)
				if err != nil {
					t.Fatal(err)
				}
				for _, got := range addresses {
					if got == address {
						t.Fatalf("excluded resource %s appeared in export", address)
					}
				}
			})
		})
	}
}
