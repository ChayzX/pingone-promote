package pingone

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

func (c *Client) Inventory(ctx context.Context, env string) (Inventory, error) {
	inv := Inventory{APIOrigin: c.BaseURL, EnvironmentID: env, Resources: []Resource{}}
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
	var snap Snapshot
	if err := c.Do(ctx, "POST", EnvPath(env)+"/snapshots?expand=all", "", map[string]string{"baseResourceURL": c.BaseURL + EnvPath(env)}, &snap); err != nil {
		return inv, fmt.Errorf("could not create environment inventory snapshot: %w", err)
	}
	if snap.SnapshotID == "" || snap.Resource.ID != env {
		return inv, errors.New("snapshot response omitted pinned environment/version identity")
	}
	snapshotPath := EnvPath(env) + "/snapshots/" + url.PathEscape(env) + "/versions/" + url.PathEscape(snap.SnapshotID)
	for attempts := 0; snap.Status != "COMPLETE"; attempts++ {
		if snap.Status == "ERROR" {
			return inv, errors.New("PingOne inventory snapshot failed; inspect snapshot errors in PingOne")
		}
		if attempts >= 90 {
			return inv, errors.New("inventory snapshot is still preparing; load resources again later")
		}
		if attempts > 0 {
			select {
			case <-ctx.Done():
				return inv, ctx.Err()
			case <-time.After(time.Second):
			}
		}
		if err := c.Do(ctx, "GET", snapshotPath, "", nil, &snap); err != nil {
			return inv, err
		}
	}
	inv.SnapshotID = snap.SnapshotID
	raw, err := c.Pages(ctx, snapshotPath+"/embeddedSnapshots", "referencedResources")
	if err != nil {
		return inv, err
	}
	seen := map[string]bool{}
	for _, r := range raw {
		var s Snapshot
		if err := json.Unmarshal(r, &s); err != nil {
			return inv, err
		}
		if s.Configuration == nil {
			return inv, errors.New("inventory contains deleted or unavailable configuration; resolve it before promotion")
		}
		if s.Status != "" && s.Status != "COMPLETE" {
			return inv, errors.New("inventory contains an incomplete resource snapshot")
		}
		u, err := c.apiURL(s.URL)
		if err != nil {
			return inv, err
		}
		prefix := EnvPath(env) + "/"
		if !strings.HasPrefix(u.Path, prefix) || s.Resource.ID == "" || u.RawQuery != "" {
			return inv, errors.New("inventory has an invalid resource identity")
		}
		if seen[s.Resource.ID] {
			return inv, fmt.Errorf("ambiguous resource ID %s; this API response needs an explicit adapter", s.Resource.ID)
		}
		seen[s.Resource.ID] = true
		name, _ := s.Configuration["name"].(string)
		if name == "" {
			name, _ = s.Configuration["displayName"].(string)
		}
		if name == "" {
			name = s.Resource.ID
		}
		relative := strings.TrimPrefix(u.Path, prefix)
		parts := strings.Split(strings.Trim(relative, "/"), "/")
		typ := parts[0]
		if len(parts) > 2 {
			var kinds []string
			for i := 0; i < len(parts); i += 2 {
				kinds = append(kinds, parts[i])
			}
			typ = strings.Join(kinds, " / ")
		}
		inv.Resources = append(inv.Resources, Resource{ID: s.Resource.ID, URL: u.String(), Name: name, Type: typ, Configuration: s.Configuration, Dependencies: s.Dependencies})
	}
	return inv, nil
}
