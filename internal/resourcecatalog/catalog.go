// Package resourcecatalog is the configuration allow-list for the pinned
// PingOne provider. The embedded inventory includes excluded types so the
// coverage matrix can account for every registered provider resource.
package resourcecatalog

import (
	_ "embed"
	"encoding/json"
	"sort"
)

//go:embed resources.json
var inventory []byte

type Resource struct {
	Type    string `json:"type"`
	Family  string `json:"family"`
	Allowed bool   `json:"allowed"`
	Import  string `json:"import"`
	Reason  string `json:"reason"`
}
type manifest struct {
	ProviderVersion string     `json:"providerVersion"`
	ProviderCommit  string     `json:"providerCommit"`
	Resources       []Resource `json:"resources"`
}

var pinned = func() manifest {
	var m manifest
	if err := json.Unmarshal(inventory, &m); err != nil {
		panic("invalid embedded provider inventory")
	}
	return m
}()
var byType = func() map[string]Resource {
	out := map[string]Resource{}
	for _, r := range pinned.Resources {
		if _, ok := out[r.Type]; ok {
			panic("duplicate provider inventory type")
		}
		out[r.Type] = r
	}
	return out
}()

func Allowed(typ string) bool            { r, ok := byType[typ]; return ok && r.Allowed }
func Lookup(typ string) (Resource, bool) { r, ok := byType[typ]; return r, ok }
func All() []Resource {
	out := append([]Resource(nil), pinned.Resources...)
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}
func ProviderVersion() string { return pinned.ProviderVersion }
