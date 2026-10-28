package checks

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"pingone-promote/internal/pingone"
)

var testKey = []byte("0123456789abcdef-evidence")

func inventory(cfg map[string]map[string]any) pingone.Inventory {
	inv := pingone.Inventory{EnvironmentID: "tgt", OrganizationID: "org", SnapshotID: "pin"}
	for id, c := range cfg {
		typ := "flows"
		if strings.HasPrefix(id, "tpl") {
			typ = "templates"
		}
		inv.Resources = append(inv.Resources, pingone.Resource{ID: id, Type: typ, Name: id, Configuration: c})
	}
	return inv
}

func capture(t *testing.T, cfg map[string]map[string]any) Evidence {
	t.Helper()
	e, err := NewEvidence(inventory(cfg), testKey, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestEvidenceNeverContainsConfigurationValues(t *testing.T) {
	e := capture(t, map[string]map[string]any{"flow": {"name": "Login", "clientSecret": "very-secret-value"}})
	raw, _ := json.Marshal(e)
	if strings.Contains(string(raw), "very-secret-value") || strings.Contains(string(raw), "clientSecret") {
		t.Fatalf("configuration leaked into evidence: %s", raw)
	}
	if _, err := NewEvidence(inventory(nil), []byte("short"), time.Now()); err == nil {
		t.Fatal("short key accepted")
	}
}

func TestEvidenceFingerprintIsKeyed(t *testing.T) {
	cfg := map[string]map[string]any{"flow": {"name": "Login"}}
	a := capture(t, cfg)
	b, _ := NewEvidence(inventory(cfg), []byte("another-evidence-key-123"), time.Unix(0, 0))
	if a.Resources[0].Fingerprint == b.Resources[0].Fingerprint || a.KeyID == b.KeyID {
		t.Fatal("fingerprint or key id does not depend on the key")
	}
	if r := Compare(a, b, CompareOptions{}); !r.Failed() || status(r, "key") != Fail {
		t.Fatalf("different keys compared:\n%s", r.Markdown())
	}
}

func TestCompareDetectsEveryUnintendedDifference(t *testing.T) {
	before := capture(t, map[string]map[string]any{"flow": {"v": 1}, "tpl1": {"v": 1}, "var": {"v": 1}})
	cases := map[string]struct {
		after map[string]map[string]any
		opts  CompareOptions
		fail  string
	}{
		"unexpected change":   {map[string]map[string]any{"flow": {"v": 2}, "tpl1": {"v": 2}, "var": {"v": 1}}, CompareOptions{ExpectChanged: []string{"flow"}}, "unexpected change"},
		"expected not seen":   {map[string]map[string]any{"flow": {"v": 1}, "tpl1": {"v": 1}, "var": {"v": 1}}, CompareOptions{ExpectChanged: []string{"flow"}}, "expected change"},
		"removed":             {map[string]map[string]any{"flow": {"v": 2}, "tpl1": {"v": 1}}, CompareOptions{ExpectChanged: []string{"flow"}}, "removed"},
		"added not allowed":   {map[string]map[string]any{"flow": {"v": 1}, "tpl1": {"v": 1}, "var": {"v": 1}, "newflow": {"v": 1}}, CompareOptions{}, "added"},
		"dependency changed":  {map[string]map[string]any{"flow": {"v": 2}, "tpl1": {"v": 1}, "var": {"v": 9}}, CompareOptions{ExpectChanged: []string{"flow"}}, "unexpected change"},
		"unknown expected id": {map[string]map[string]any{"flow": {"v": 1}, "tpl1": {"v": 1}, "var": {"v": 1}}, CompareOptions{ExpectChanged: []string{"ghost"}}, "expected change"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := Compare(before, capture(t, c.after), c.opts)
			if !r.Failed() || status(r, c.fail) != Fail {
				t.Fatalf("not failed on %s:\n%s", c.fail, r.Markdown())
			}
		})
	}
}

func TestCompareAcceptsExactlyIntendedChange(t *testing.T) {
	before := capture(t, map[string]map[string]any{"flow": {"v": 1}, "tpl1": {"v": 1}})
	after := capture(t, map[string]map[string]any{"flow": {"v": 2}, "tpl1": {"v": 1}, "newflow": {"v": 1}})
	r := Compare(before, after, CompareOptions{ExpectChanged: []string{"flow"}, AllowAddedTypes: []string{"flows"}})
	if r.Failed() {
		t.Fatalf("intended change rejected:\n%s", r.Markdown())
	}
	other := before
	other.EnvironmentID = "prod"
	if r = Compare(before, other, CompareOptions{}); !r.Failed() {
		t.Fatal("different environments compared")
	}
}

func TestDecodeEvidenceRejectsMalformedFiles(t *testing.T) {
	good, _ := json.Marshal(capture(t, map[string]map[string]any{"flow": {"v": 1}}))
	if _, err := DecodeEvidence(good); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{}`, `{"version":1,"environmentId":"e","keyId":"k","resources":[{"id":"a","fingerprint":"x"}]}`, `{"version":1,"environmentId":"e","keyId":"k","extra":1}`} {
		if _, err := DecodeEvidence([]byte(bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}
