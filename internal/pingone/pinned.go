package pingone

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// InventoryVersion reads an immutable snapshot without creating a new version.
func (c *Client) InventoryVersion(ctx context.Context, env, snapshotID string) (Inventory, error) {
	inv := Inventory{APIOrigin: c.BaseURL, EnvironmentID: env, SnapshotID: snapshotID, Resources: []Resource{}}
	if !pinnedID(env) || !pinnedID(snapshotID) {
		return inv, errors.New("invalid pinned environment or snapshot identity")
	}
	var meta struct {
		ID           string `json:"id"`
		Organization Ref    `json:"organization"`
	}
	if err := c.Do(ctx, "GET", EnvPath(env), "", nil, &meta); err != nil {
		return inv, err
	}
	if meta.ID != env || meta.Organization.ID == "" {
		return inv, errors.New("environment response omitted its identity or organization")
	}
	inv.OrganizationID = meta.Organization.ID
	path := EnvPath(env) + "/snapshots/" + url.PathEscape(env) + "/versions/" + url.PathEscape(snapshotID)
	var snap Snapshot
	if err := c.Do(ctx, "GET", path, "", nil, &snap); err != nil {
		return inv, err
	}
	if snap.SnapshotID != snapshotID || snap.Resource.ID != env || snap.Status != "COMPLETE" {
		return inv, errors.New("pinned snapshot identity or COMPLETE status did not match")
	}
	raw, err := c.Pages(ctx, path+"/embeddedSnapshots", "referencedResources")
	if err != nil {
		return inv, err
	}
	seen := map[string]bool{}
	for _, r := range raw {
		var s Snapshot
		if err = json.Unmarshal(r, &s); err != nil {
			return inv, errors.New("invalid pinned resource snapshot")
		}
		if s.Configuration == nil || (s.Status != "" && s.Status != "COMPLETE") {
			return inv, errors.New("pinned inventory contains unavailable or incomplete configuration")
		}
		u, err := c.apiURL(s.URL)
		if err != nil {
			return inv, err
		}
		prefix := EnvPath(env) + "/"
		if !strings.HasPrefix(u.Path, prefix) || !pinnedID(s.Resource.ID) || u.RawQuery != "" || strings.Contains(u.Path, "//") {
			return inv, errors.New("pinned inventory has an invalid resource identity")
		}
		if seen[s.Resource.ID] {
			return inv, fmt.Errorf("ambiguous resource ID %s", s.Resource.ID)
		}
		seen[s.Resource.ID] = true
		parts := strings.Split(strings.Trim(strings.TrimPrefix(u.Path, prefix), "/"), "/")
		if len(parts) < 2 || len(parts)%2 != 0 || parts[len(parts)-1] != s.Resource.ID {
			return inv, errors.New("pinned resource URL does not match its original identity")
		}
		kinds := []string{}
		for i := 0; i < len(parts); i += 2 {
			if !pinnedID(parts[i]) {
				return inv, errors.New("invalid pinned resource path")
			}
			kinds = append(kinds, parts[i])
		}
		name, _ := s.Configuration["name"].(string)
		if name == "" {
			name, _ = s.Configuration["displayName"].(string)
		}
		if name == "" {
			name = s.Resource.ID
		}
		inv.Resources = append(inv.Resources, Resource{ID: s.Resource.ID, URL: u.String(), Name: name, Type: strings.Join(kinds, " / "), Configuration: s.Configuration, Dependencies: s.Dependencies})
	}
	return inv, nil
}
func pinnedID(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
