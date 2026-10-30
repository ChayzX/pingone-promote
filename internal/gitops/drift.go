package gitops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"pingone-promote/internal/pingone"
)

// ErrTargetDrift reports that the live destination no longer matches the target
// snapshot the approved plan was reviewed against.
var ErrTargetDrift = errors.New("live target configuration changed after review; plan and approve a fresh promotion")

// checkTargetDrift compares a freshly captured live target inventory with the
// pinned, reviewed target snapshot for every resource the native plan touches.
//
// PingOne does not document conditional execution of a reviewed plan, and the
// pinned snapshot is immutable, so re-reading it cannot reveal console edits made
// between review and apply. This check narrows that window to the time between
// the live snapshot and the execute call. It fails closed: an unreadable live
// target, a changed or deleted affected resource, or a new resource in a
// collection the plan creates into all block execution.
//
// Affected resources are: every step's explicit target, every mapped destination
// and any existing resource addressed by a step URL. Collections receiving
// create operations are compared by membership so that a concurrently created
// same-named resource also blocks. Comparison uses full unredacted configuration
// in memory only; nothing from the live inventory is persisted.
func (e *Engine) checkTargetDrift(ctx context.Context, a Artifact) error {
	source, pinned, err := e.inventories(ctx, a.Request, a.TargetSnapshotID)
	if err != nil {
		return fmt.Errorf("%w: pinned target unreadable: %v", ErrTargetDrift, err)
	}
	steps, err := e.Client.Steps(ctx, a.Request.Source.ID, a.NativeID)
	if err != nil {
		return fmt.Errorf("%w: native steps unreadable: %v", ErrTargetDrift, err)
	}
	live, err := e.Client.Inventory(ctx, a.Request.Target.ID)
	if err != nil {
		return fmt.Errorf("%w: live target unreadable: %v", ErrTargetDrift, err)
	}
	if live.OrganizationID != pinned.OrganizationID || live.EnvironmentID != pinned.EnvironmentID {
		return fmt.Errorf("%w: live target identity differs", ErrTargetDrift)
	}
	before, after := byID(pinned), byID(live)
	byPath := map[string]string{}
	for id, r := range before {
		if p := resourcePath(r.URL); p != "" {
			byPath[p] = id
		}
	}
	affected := map[string]bool{}
	collections := map[string]bool{}
	for _, tid := range a.Request.Mappings {
		affected[tid] = true
	}
	// An excluded dependency may be reused by the selected resource without a
	// native write step of its own. Its destination configuration is part of the
	// reviewed behavior, so include the transitive reused dependency graph.
	sourceByID := byID(source)
	queue := append([]string(nil), a.Request.Selected...)
	visited := map[string]bool{}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if visited[id] {
			continue
		}
		visited[id] = true
		for _, dependencyID := range sourceByID[id].Dependencies {
			targetID := a.Request.Mappings[dependencyID]
			if targetID == "" {
				targetID = dependencyID
			}
			if _, exists := before[targetID]; exists {
				affected[targetID] = true
			} else if _, exists := after[targetID]; exists {
				affected[targetID] = true
			}
			if _, exists := sourceByID[dependencyID]; exists {
				queue = append(queue, dependencyID)
			}
		}
	}
	for _, st := range steps {
		if st.Target.ID != "" {
			affected[st.Target.ID] = true
		}
		p := resourcePath(st.URL)
		if p == "" {
			return fmt.Errorf("%w: step URL cannot be resolved for comparison", ErrTargetDrift)
		}
		matched := false
		for rp, id := range byPath {
			if p == rp || strings.HasPrefix(p, rp+"/") {
				affected[id] = true
				matched = true
			}
		}
		if !matched && strings.EqualFold(st.Action, "POST") {
			if c := collectionType(p, a.Request.Target.ID); c != "" {
				collections[c] = true
			}
		}
		// Native plans may rewrite a selected resource's dependency IDs to
		// reuse destination resources. Include only exact IDs found in the
		// reviewed target snapshots; this creates drift candidates, not write
		// authorization. Their complete configurations are compared below.
		var payload any
		if len(st.Payload) > 0 {
			if err := json.Unmarshal(st.Payload, &payload); err != nil {
				return fmt.Errorf("%w: step payload cannot be resolved for comparison", ErrTargetDrift)
			}
			if err := collectTargetIDs(payload, before, after, affected); err != nil {
				return fmt.Errorf("%w: %v", ErrTargetDrift, err)
			}
		}
	}
	// Follow dependencies from every affected pinned/live target resource.
	// Considering both snapshots detects a dependency added or removed after
	// review while remaining cycle-safe.
	dependencyQueue := make([]string, 0, len(affected))
	for id := range affected {
		dependencyQueue = append(dependencyQueue, id)
	}
	dependencyVisited := map[string]bool{}
	for len(dependencyQueue) > 0 {
		id := dependencyQueue[0]
		dependencyQueue = dependencyQueue[1:]
		if dependencyVisited[id] {
			continue
		}
		dependencyVisited[id] = true
		for _, inventory := range []map[string]pingone.Resource{before, after} {
			if resource, ok := inventory[id]; ok {
				for _, dependencyID := range resource.Dependencies {
					if _, inPinned := before[dependencyID]; inPinned {
						affected[dependencyID] = true
						dependencyQueue = append(dependencyQueue, dependencyID)
					} else if _, inLive := after[dependencyID]; inLive {
						affected[dependencyID] = true
						dependencyQueue = append(dependencyQueue, dependencyID)
					}
				}
			}
		}
	}
	ids := make([]string, 0, len(affected))
	for id := range affected {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		b, inPinned := before[id]
		l, inLive := after[id]
		switch {
		case inPinned && !inLive:
			return fmt.Errorf("%w: affected resource %s was removed", ErrTargetDrift, id)
		case !inPinned && inLive:
			return fmt.Errorf("%w: affected resource %s appeared after review", ErrTargetDrift, id)
		case inPinned && inLive:
			bf, lf := driftConfigurationFingerprint(b), driftConfigurationFingerprint(l)
			if bf == "" || lf == "" || b.Type != l.Type || bf != lf || fingerprint(b.Dependencies) != fingerprint(l.Dependencies) {
				return fmt.Errorf("%w: affected resource %s changed", ErrTargetDrift, id)
			}
		}
	}
	for c := range collections {
		if !sameMembers(before, after, c) {
			return fmt.Errorf("%w: membership of %s changed", ErrTargetDrift, c)
		}
	}
	return nil
}

// PingOne documents form fieldTypes as read-only, and its provider describes it
// as a set: https://registry.terraform.io/providers/pingidentity/pingone/latest/docs/resources/form
// Snapshot responses can return the same set in different orders. Normalize only that
// field for drift comparison; all editable fields and other arrays stay exact.
func driftConfigurationFingerprint(r pingone.Resource) string {
	if r.Type != "forms" || r.Configuration == nil {
		return fingerprint(r.Configuration)
	}
	raw, exists := r.Configuration["fieldTypes"]
	if !exists {
		return fingerprint(r.Configuration)
	}
	values, ok := raw.([]any)
	if !ok {
		return ""
	}
	types := make([]string, len(values))
	for i, value := range values {
		types[i], ok = value.(string)
		if !ok {
			return ""
		}
	}
	sort.Strings(types)
	copyConfig := make(map[string]any, len(r.Configuration))
	for k, value := range r.Configuration {
		copyConfig[k] = value
	}
	copyConfig["fieldTypes"] = types
	return fingerprint(copyConfig)
}

func collectTargetIDs(value any, before, after map[string]pingone.Resource, affected map[string]bool) error {
	switch item := value.(type) {
	case string:
		if _, ok := before[item]; ok {
			affected[item] = true
		} else if _, ok := after[item]; ok {
			affected[item] = true
		}
	case []any:
		for _, child := range item {
			if err := collectTargetIDs(child, before, after, affected); err != nil {
				return err
			}
		}
	case map[string]any:
		for _, child := range item {
			if err := collectTargetIDs(child, before, after, affected); err != nil {
				return err
			}
		}
	}
	return nil
}

func byID(inv pingone.Inventory) map[string]pingone.Resource {
	m := make(map[string]pingone.Resource, len(inv.Resources))
	for _, r := range inv.Resources {
		m[r.ID] = r
	}
	return m
}

func resourcePath(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.RawQuery != "" || u.Fragment != "" || strings.Contains(u.Path, "//") {
		return ""
	}
	return strings.TrimRight(u.Path, "/")
}

// collectionType returns the top-level collection of a create URL under the
// target environment, e.g. "flows" for /v1/environments/<env>/flows.
func collectionType(p, env string) string {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	if len(parts) == 4 && parts[0] == "v1" && parts[1] == "environments" && parts[2] == env {
		return parts[3]
	}
	return ""
}

func sameMembers(before, after map[string]pingone.Resource, typ string) bool {
	set := func(m map[string]pingone.Resource) []string {
		out := []string{}
		for id, r := range m {
			if r.Type == typ || strings.HasPrefix(r.Type, typ+" / ") {
				out = append(out, id+"\x00"+fingerprint(r.Configuration))
			}
		}
		sort.Strings(out)
		return out
	}
	return fingerprint(set(before)) == fingerprint(set(after))
}
