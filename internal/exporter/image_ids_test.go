package exporter

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

const explicitImageID = "22222222-2222-4222-8222-222222222222"

type imageInventoryFixture struct {
	coreInventoryFixture
	lookups int
	result  SourceResource
	err     error
}

func TestExplicitImageNotFoundIsOnlyMissingImage(t *testing.T) {
	ctx := WithResourceIDs(context.Background(), []TypedResourceID{{Type: "pingone_image", ID: explicitImageID}})
	for _, tc := range []struct {
		typ     string
		status  int
		missing bool
	}{{"pingone_image", 404, true}, {"authentication", 404, false}, {"pingone_image", 403, false}, {"pingone_image", 500, false}} {
		inv := &imageInventoryFixture{err: fmt.Errorf("private upstream: %w", &InventoryHTTPError{ResourceType: tc.typ, StatusCode: tc.status})}
		_, err := supplementResourceIDs(ctx, creds, inv, nil)
		if err == nil || errors.Is(err, ErrExplicitResourceNotFound) != tc.missing || strings.Contains(err.Error(), "private") {
			t.Fatalf("type=%s status=%d: %v", tc.typ, tc.status, err)
		}
	}
}

func (f *imageInventoryFixture) Lookup(context.Context, Credentials, string, string) (SourceResource, error) {
	f.lookups++
	return f.result, f.err
}
func explicitImage() SourceResource {
	r := coreObject("pingone_image", "logo", explicitImageID, nil)
	r.ImportID = devEnv + "/" + explicitImageID
	return r
}
func TestExplicitImageSelectedScopedAndExcluded(t *testing.T) {
	for _, mode := range []string{"selected", "scoped", "whole-excluded"} {
		t.Run(mode, func(t *testing.T) {
			inv := &imageInventoryFixture{result: explicitImage()}
			reader := &coreReaderFixture{reads: map[string]int{}}
			c := &CoreExporter{Inventory: inv, Reader: reader}
			ctx := WithResourceIDs(context.Background(), []TypedResourceID{{Type: "pingone_image", ID: explicitImageID}})
			var e *Export
			var err error
			switch mode {
			case "selected":
				e, err = c.SelectedExport(ctx, creds, []string{"pingone_image.logo"}, nil)
			case "scoped":
				e, err = c.ScopedExport(ctx, creds, []string{"pingone_image.logo"}, nil)
			default:
				e, err = c.WholeExport(ctx, creds, []string{"pingone_image.*"})
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "whole-excluded" {
				if reader.reads["pingone_image.logo"] != 0 {
					t.Fatal("excluded image imported")
				}
			} else {
				assertCoreAddresses(t, e, "pingone_image.logo")
			}
			if inv.lookups != 1 {
				t.Fatalf("lookups=%d", inv.lookups)
			}
		})
	}
}
func TestExplicitImageContextCopyDedupAndIsolation(t *testing.T) {
	ids := []TypedResourceID{{Type: "pingone_image", ID: explicitImageID}, {Type: "pingone_image", ID: explicitImageID}}
	ctx := WithResourceIDs(context.Background(), ids)
	ids[0].Type = "pingone_user"
	ids[1].ID = "bad"
	inv := &imageInventoryFixture{result: explicitImage()}
	c := &CoreExporter{Inventory: inv, Reader: &coreReaderFixture{reads: map[string]int{}}}
	if got, err := c.CatalogAllIDs(ctx, creds); err != nil || len(got) != 1 || inv.lookups != 1 {
		t.Fatalf("got=%v err=%v lookups=%d", got, err, inv.lookups)
	}
	if got, err := c.CatalogAllIDs(context.Background(), creds); err != nil || len(got) != 0 {
		t.Fatalf("context leaked: %v %v", got, err)
	}
	inv.coreInventoryFixture = coreInventoryFixture{explicitImage()}
	if _, err := c.CatalogAllIDs(ctx, creds); err != nil || inv.lookups != 1 {
		t.Fatalf("listed image looked up: %v %d", err, inv.lookups)
	}
}
func TestExplicitImageRejectsUnsafeSelectorsAndLookup(t *testing.T) {
	tooMany := make([]TypedResourceID, 513)
	for i := range tooMany {
		tooMany[i] = TypedResourceID{Type: "pingone_image", ID: explicitImageID}
	}
	for _, ids := range [][]TypedResourceID{{{Type: "pingone_user", ID: explicitImageID}}, {{Type: "pingone_image", ID: "../../users"}}, tooMany} {
		inv := &imageInventoryFixture{result: explicitImage()}
		c := &CoreExporter{Inventory: inv, Reader: &coreReaderFixture{reads: map[string]int{}}}
		if _, err := c.CatalogAllIDs(WithResourceIDs(context.Background(), ids), creds); err == nil || inv.lookups != 0 {
			t.Fatalf("unsafe selection accepted: %v lookups=%d", err, inv.lookups)
		}
	}
	ctx := WithResourceIDs(context.Background(), []TypedResourceID{{Type: "pingone_image", ID: explicitImageID}})
	for _, kind := range []string{"unsupported", "mismatch", "import", "collision", "upstream-error"} {
		t.Run(kind, func(t *testing.T) {
			inv := &imageInventoryFixture{result: explicitImage()}
			var inventory InventoryReader = inv
			switch kind {
			case "unsupported":
				inventory = coreInventoryFixture{}
			case "mismatch":
				inv.result.ID = "wrong"
			case "import":
				inv.result.ImportID = "other/" + explicitImageID
			case "collision":
				r := explicitImage()
				r.ID = "33333333-3333-4333-8333-333333333333"
				inv.coreInventoryFixture = coreInventoryFixture{r}
			case "upstream-error":
				inv.err = errors.New("SECRET_FROM_UPSTREAM")
			}
			c := &CoreExporter{Inventory: inventory, Reader: &coreReaderFixture{reads: map[string]int{}}}
			_, err := c.CatalogAllIDs(ctx, creds)
			if err == nil || strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("unsafe lookup result: %v", err)
			}
		})
	}
}
