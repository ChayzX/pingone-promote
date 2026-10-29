package gitops

import (
	"fmt"
	"sort"
	"strings"

	"pingone-promote/internal/pingone"
)

// DriftFromInventories is checkTargetDrift without the API reads (cross-check only).
func DriftFromInventories(pinned, live pingone.Inventory, steps []pingone.Step, a Artifact) error {
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
		if !matched && st.Target.ID == "" && strings.EqualFold(st.Action, "POST") {
			if c := collectionType(p, a.Request.Target.ID); c != "" {
				collections[c] = true
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
			bf, lf := fingerprint(b.Configuration), fingerprint(l.Configuration)
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
