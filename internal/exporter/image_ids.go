package exporter

import (
	"context"
	"errors"
	"strings"
)

// TypedResourceID carries an explicitly requested configuration identity. Only
// images need this supplementation because PingOne has no image list endpoint.
type TypedResourceID struct{ Type, ID string }
type resourceIDsKey struct{}
type resourceIDs struct {
	ids     []TypedResourceID
	invalid bool
}

// WithResourceIDs copies request-scoped selectors. Invalid or excessive input
// is remembered as a closed failure without retaining caller-provided data.
func WithResourceIDs(ctx context.Context, ids []TypedResourceID) context.Context {
	value := resourceIDs{}
	if len(ids) > 512 {
		value.invalid = true
	} else {
		seen := map[TypedResourceID]bool{}
		for _, id := range ids {
			if id.Type != "pingone_image" || !uuidRE.MatchString(id.ID) {
				value = resourceIDs{invalid: true}
				break
			}
			id.ID = strings.ToLower(id.ID)
			if !seen[id] {
				value.ids = append(value.ids, id)
				seen[id] = true
			}
		}
	}
	return context.WithValue(ctx, resourceIDsKey{}, value)
}

type resourceIDLookup interface {
	Lookup(context.Context, Credentials, string, string) (SourceResource, error)
}

// ErrExplicitResourceNotFound identifies only a missing explicitly requested
// image. Search callers can retain their ordinary catalog on this result.
var ErrExplicitResourceNotFound = errors.New("exporter: explicit image not found")

func supplementResourceIDs(ctx context.Context, creds Credentials, inventory InventoryReader, rs []SourceResource) ([]SourceResource, error) {
	selected, _ := ctx.Value(resourceIDsKey{}).(resourceIDs)
	fail := func() ([]SourceResource, error) { return nil, errors.New("exporter: explicit image lookup failed") }
	if selected.invalid {
		return fail()
	}
	if len(selected.ids) == 0 {
		return rs, nil
	}
	seen := map[TypedResourceID]bool{}
	for _, r := range rs {
		seen[TypedResourceID{r.Type, strings.ToLower(r.ID)}] = true
	}
	out := append([]SourceResource(nil), rs...)
	for _, id := range selected.ids {
		if seen[id] {
			continue
		}
		lookup, ok := inventory.(resourceIDLookup)
		if !ok {
			return fail()
		}
		r, err := lookup.Lookup(ctx, creds, id.Type, id.ID)
		var httpError *InventoryHTTPError
		if errors.As(err, &httpError) && httpError.ResourceType == "pingone_image" && httpError.StatusCode == 404 {
			return nil, ErrExplicitResourceNotFound
		}
		if err != nil || r.Type != id.Type || !strings.EqualFold(r.ID, id.ID) || r.ImportID != creds.ExportEnvironment()+"/"+id.ID {
			return fail()
		}
		if actual := inventoryString(r.Values, "id"); actual != "" && !strings.EqualFold(actual, id.ID) {
			return fail()
		}
		if env := inventoryString(r.Values, "environment.id"); env != "" && env != creds.ExportEnvironment() {
			return fail()
		}
		out = append(out, r)
		seen[id] = true
	}
	return out, nil
}
