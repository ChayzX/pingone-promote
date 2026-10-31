package pingone

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	"strconv"

	"time"
)

// embeddedPageLimit is the page size requested for embeddedSnapshots.
const embeddedPageLimit = 1000

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
	pinnedSnapshotID := snap.SnapshotID
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
		var polled Snapshot
		if err := c.Do(ctx, "GET", snapshotPath, "", nil, &polled); err != nil {
			return inv, err
		}
		if polled.SnapshotID != pinnedSnapshotID || polled.Resource.ID != env {
			return inv, errors.New("snapshot polling changed or omitted pinned environment/version identity")
		}
		snap = polled
	}

	if snap.Status != "COMPLETE" || snap.SnapshotID != pinnedSnapshotID || snap.Resource.ID != env {
		return inv, errors.New("completed snapshot identity did not match the requested environment/version")
	}
	inv.SnapshotID = pinnedSnapshotID
	raw, err := c.Pages(ctx, snapshotPath+"/embeddedSnapshots?limit="+strconv.Itoa(embeddedPageLimit), "referencedResources")

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
		typ, err := snapshotResourceType(u, env, s.Resource.ID)
		if err != nil {
			return inv, err
		}
		// The identity adapter permits only known read-expansion queries.
		u.RawQuery, u.ForceQuery = "", false
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
		inv.Resources = append(inv.Resources, Resource{ID: s.Resource.ID, URL: u.String(), Name: name, Type: typ, Configuration: s.Configuration, Dependencies: s.Dependencies})
	}
	return inv, nil
}
