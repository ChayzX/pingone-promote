package catalog

import (
	"os"
	"strings"
	"testing"
)

// Every resource of the pinned provider has exactly one definition, and
// every definition names a real provider resource.
func TestEveryProviderResourceIsClassified(t *testing.T) {
	types := SchemaTypes()
	if len(types) < 100 {
		t.Fatalf("schema has only %d resources", len(types))
	}
	for _, typ := range types {
		d, ok := Get(typ)
		if !ok {
			t.Errorf("%s has no catalog entry", typ)
			continue
		}
		switch d.Status {
		case Covered, Limited:
			if d.Exporter == "" {
				t.Errorf("%s is %s but has no exporter", typ, d.Status)
			}
		case NotPossible:
			if d.Exporter != "" || d.Reason == "" {
				t.Errorf("%s is not possible but has an exporter or no reason", typ)
			}
		default:
			t.Errorf("%s has status %q", typ, d.Status)
		}
		if d.Status == Limited && d.Reason == "" {
			t.Errorf("%s has limits without a reason", typ)
		}
	}
	schema := map[string]bool{}
	for _, typ := range types {
		schema[typ] = true
	}
	for _, d := range All() {
		if !schema[d.Type] {
			t.Errorf("catalog entry %s is not a provider %s resource", d.Type, ProviderVersion)
		}
	}
	if SchemaVersion() != ProviderVersion {
		t.Errorf("embedded schema is %s, pin is %s", SchemaVersion(), ProviderVersion)
	}
}

func TestIdentityDataIsNeverExportable(t *testing.T) {
	for _, d := range All() {
		name := strings.TrimPrefix(d.Type, "pingone_")
		if strings.HasPrefix(name, "user") || strings.Contains(name, "_user") {
			if Exportable(d.Type) || !IdentityData(d.Type) {
				t.Errorf("%s must be identity data and not exportable", d.Type)
			}
		}
	}
	for _, k := range []string{"pingone_user", "pingone_user_group_assignment"} {
		if _, ok := Resolve(k); ok {
			t.Errorf("Resolve(%s) accepted identity data", k)
		}
	}
}

func TestDefinitionsAreConsistent(t *testing.T) {
	for _, d := range All() {
		if d.Exporter != ExporterAPI {
			continue
		}
		attrs, _ := Schema(d.Type)
		if d.Parent != "" {
			if _, ok := Get(d.Parent); !ok {
				t.Errorf("%s: parent %s unknown", d.Type, d.Parent)
			}
			if d.ParentAttr != "" {
				if _, ok := attrs[d.ParentAttr]; !ok {
					t.Errorf("%s: parentAttr %s is not a schema attribute", d.Type, d.ParentAttr)
				}
			}
		}
		for _, p := range d.EnvLocal {
			if !hasPath(attrs, p) {
				t.Errorf("%s: envLocal %s is not a schema attribute path", d.Type, p)
			}
		}
		for p := range d.Hoist {
			if !hasPath(attrs, p) {
				t.Errorf("%s: hoist %s is not a schema attribute", d.Type, p)
			}
		}
		for p := range d.Rename {
			if !hasPath(attrs, p) {
				t.Errorf("%s: rename %s is not a schema attribute path", d.Type, p)
			}
		}
		if !d.Singleton && d.Path != "" && len(d.NameAttrs) == 0 && d.Parent == "" {
			t.Errorf("%s: a collection type needs name attributes for its label", d.Type)
		}
	}
}

func hasPath(attrs map[string]Attr, p string) bool {
	parts := strings.Split(p, ".")
	for i, part := range parts {
		a, ok := attrs[part]
		if !ok {
			return false
		}
		if i == len(parts)-1 {
			return true
		}
		attrs = a.Attrs
	}
	return false
}

// docs/coverage.md is generated from this registry and must be current.
func TestCoverageDocIsCurrent(t *testing.T) {
	want := CoverageMarkdown()
	got, err := os.ReadFile("../../docs/coverage.md")
	if os.Getenv("PINGONE_PROMOTE_WRITE_COVERAGE") == "1" {
		if err := os.WriteFile("../../docs/coverage.md", []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err != nil || strings.ReplaceAll(string(got), "\r\n", "\n") != want {
		t.Fatal("docs/coverage.md is out of date; run PINGONE_PROMOTE_WRITE_COVERAGE=1 go test ./internal/catalog")
	}
}
