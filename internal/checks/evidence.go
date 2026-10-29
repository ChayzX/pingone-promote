package checks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"pingone-promote/internal/pingone"
)

// Evidence is a keyed fingerprint of every configuration resource in one
// environment at one moment. It never contains configuration values: each
// fingerprint is HMAC-SHA256 over the full configuration, keyed by a secret the
// operator holds, so a capture cannot be used to guess secret values offline.
type Evidence struct {
	Version        int                `json:"version"`
	CapturedAt     string             `json:"capturedAt"`
	EnvironmentID  string             `json:"environmentId"`
	OrganizationID string             `json:"organizationId"`
	SnapshotID     string             `json:"snapshotId"`
	KeyID          string             `json:"keyId"`
	Resources      []EvidenceResource `json:"resources"`
}

// EvidenceResource is one resource's identity and keyed fingerprint.
type EvidenceResource struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
}

// MinKeyLength is the shortest HMAC key accepted for evidence.
const MinKeyLength = 16

// NewEvidence fingerprints an inventory with the given key.
func NewEvidence(inv pingone.Inventory, key []byte, now time.Time) (Evidence, error) {
	if len(key) < MinKeyLength {
		return Evidence{}, fmt.Errorf("evidence key must be at least %d bytes", MinKeyLength)
	}
	e := Evidence{Version: 1, CapturedAt: now.UTC().Format(time.RFC3339), EnvironmentID: inv.EnvironmentID, OrganizationID: inv.OrganizationID, SnapshotID: inv.SnapshotID, KeyID: keyID(key), Resources: []EvidenceResource{}}
	for _, r := range inv.Resources {
		raw, err := json.Marshal(struct {
			Type          string         `json:"type"`
			Configuration map[string]any `json:"configuration"`
			Dependencies  []string       `json:"dependencies"`
		}{r.Type, r.Configuration, r.Dependencies})
		if err != nil {
			return Evidence{}, fmt.Errorf("resource %s configuration cannot be encoded", r.ID)
		}
		m := hmac.New(sha256.New, key)
		m.Write(raw)
		e.Resources = append(e.Resources, EvidenceResource{ID: r.ID, Type: r.Type, Name: r.Name, Fingerprint: hex.EncodeToString(m.Sum(nil))})
	}
	sort.Slice(e.Resources, func(i, j int) bool { return e.Resources[i].ID < e.Resources[j].ID })
	return e, nil
}

// keyID identifies the key without revealing it, so compare can refuse
// captures made with different keys.
func keyID(key []byte) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("pingone-promote evidence key id v1"))
	return hex.EncodeToString(m.Sum(nil))[:16]
}

// CompareOptions says which differences are intended.
type CompareOptions struct {
	// ExpectChanged lists resource IDs that must change; every other existing
	// resource must stay identical.
	ExpectChanged []string
	// AllowAddedTypes lists resource types that may gain new resources
	// (for example "flows" when a promotion creates a flow).
	AllowAddedTypes []string
}

// Compare reports every difference between two captures of one environment.
func Compare(before, after Evidence, o CompareOptions) Report {
	r := Report{Title: fmt.Sprintf("Evidence compare (%s)", after.EnvironmentID)}
	if before.Version != 1 || after.Version != 1 {
		r.Fail("format", "unsupported evidence version")
		return r
	}
	if before.EnvironmentID == "" || before.EnvironmentID != after.EnvironmentID || before.OrganizationID != after.OrganizationID {
		r.Fail("environment", "captures are of different environments (%s vs %s)", before.EnvironmentID, after.EnvironmentID)
		return r
	}
	if before.KeyID == "" || before.KeyID != after.KeyID {
		r.Fail("key", "captures were made with different evidence keys; fingerprints are not comparable")
		return r
	}
	r.Pass("captures", "before %s (snapshot %s), after %s (snapshot %s)", before.CapturedAt, before.SnapshotID, after.CapturedAt, after.SnapshotID)

	b, a := index(before), index(after)
	expect := set(o.ExpectChanged)
	addTypes := set(o.AllowAddedTypes)
	unexpected := 0
	for _, id := range sortedKeys(b) {
		br := b[id]
		ar, ok := a[id]
		switch {
		case !ok:
			unexpected++
			r.Fail("removed", "%s %q (%s) was removed", br.Type, br.Name, id)
		case ar.Fingerprint != br.Fingerprint && expect[id]:
			r.Pass("expected change", "%s %q (%s) changed as intended", ar.Type, ar.Name, id)
		case ar.Fingerprint != br.Fingerprint:
			unexpected++
			r.Fail("unexpected change", "%s %q (%s) changed but was not expected to", ar.Type, ar.Name, id)
		case expect[id]:
			r.Fail("expected change", "%s %q (%s) did not change", ar.Type, ar.Name, id)
		}
	}
	for _, id := range sortedKeys(a) {
		if _, ok := b[id]; ok {
			continue
		}
		ar := a[id]
		if addTypes[ar.Type] || expect[id] {
			r.Pass("added", "%s %q (%s) was added (allowed)", ar.Type, ar.Name, id)
		} else {
			unexpected++
			r.Fail("added", "%s %q (%s) appeared but no addition of type %s was allowed", ar.Type, ar.Name, id, ar.Type)
		}
	}
	for id := range expect {
		if _, inBefore := b[id]; !inBefore {
			if _, inAfter := a[id]; !inAfter {
				r.Fail("expected change", "expected resource %s is in neither capture", id)
			}
		}
	}
	if unexpected == 0 {
		r.Pass("unchanged", "%d other resource(s) are identical", countUnchanged(b, a, expect))
	}
	return r
}

// DecodeEvidence parses and sanity-checks a capture file.
func DecodeEvidence(raw []byte) (Evidence, error) {
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	var e Evidence
	if err := d.Decode(&e); err != nil {
		return e, errors.New("invalid evidence JSON")
	}
	if e.Version != 1 || e.EnvironmentID == "" || e.KeyID == "" {
		return e, errors.New("evidence file is missing version, environment or key identity")
	}
	seen := map[string]bool{}
	for _, r := range e.Resources {
		if r.ID == "" || seen[r.ID] || len(r.Fingerprint) != 64 {
			return e, errors.New("evidence file has a duplicate, empty or malformed resource entry")
		}
		seen[r.ID] = true
	}
	return e, nil
}

func index(e Evidence) map[string]EvidenceResource {
	m := make(map[string]EvidenceResource, len(e.Resources))
	for _, r := range e.Resources {
		m[r.ID] = r
	}
	return m
}
func set(ss []string) map[string]bool {
	m := map[string]bool{}
	for _, s := range ss {
		if s = strings.TrimSpace(s); s != "" {
			m[s] = true
		}
	}
	return m
}
func sortedKeys(m map[string]EvidenceResource) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
func countUnchanged(b, a map[string]EvidenceResource, expect map[string]bool) int {
	n := 0
	for id, br := range b {
		if ar, ok := a[id]; ok && ar.Fingerprint == br.Fingerprint && !expect[id] {
			n++
		}
	}
	return n
}
