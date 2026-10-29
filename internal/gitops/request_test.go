package gitops

import (
	"bytes"
	"encoding/json"
	"pingone-promote/internal/pingone"
	"strings"
	"testing"
)

func testRequest(t *testing.T) Request {
	t.Helper()
	src, dst := testInventories("https://api.pingone.com")
	r, e := NewRequest(src, dst, "us", "us", []string{"flow"}, map[string]string{"flow": "dest"}, "Review flow")
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func testInventories(origin string) (pingone.Inventory, pingone.Inventory) {
	src := pingone.Inventory{APIOrigin: origin, EnvironmentID: "source", OrganizationID: "org", SnapshotID: "sourcepin", Resources: []pingone.Resource{{ID: "flow", URL: origin + "/v1/environments/source/flows/flow", Type: "flows", Configuration: map[string]any{"name": "Flow", "clientSecret": "secret-value"}}, {ID: "template", URL: origin + "/v1/environments/source/templates/template", Type: "templates", Configuration: map[string]any{"name": "Hidden"}}}}
	dst := pingone.Inventory{APIOrigin: origin, EnvironmentID: "target", OrganizationID: "org", SnapshotID: "targetpin", Resources: []pingone.Resource{{ID: "dest", URL: origin + "/v1/environments/target/flows/dest", Type: "flows", Configuration: map[string]any{"name": "Old"}}}}
	return src, dst
}
func TestRequestRedactionStrictDecodeAndFingerprint(t *testing.T) {
	r := testRequest(t)
	raw, e := Encode(r)
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(raw, []byte("secret-value")) {
		t.Fatal("secret in manifest")
	}
	if _, e = Decode(bytes.NewReader(raw)); e != nil {
		t.Fatal(e)
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	m["unexpected"] = true
	bad, _ := json.Marshal(m)
	if _, e = Decode(bytes.NewReader(bad)); e == nil {
		t.Fatal("accepted unknown field")
	}
	r.Configurations["flow"] = map[string]any{"name": "tampered"}
	if Validate(r) == nil {
		t.Fatal("accepted tampered configuration")
	}
}
func TestRequestRejectsMissingUnsafeAndExtraFields(t *testing.T) {
	for _, mutate := range []func(*Request){func(r *Request) { r.Target.ID = "" }, func(r *Request) { r.Source.ID = "../escape" }, func(r *Request) { r.Target.Region = "moon" }, func(r *Request) { r.Fingerprints["extra"] = strings.Repeat("a", 64) }, func(r *Request) { r.Source.SnapshotID = "" }, func(r *Request) { r.Target.OrganizationID = "other" }} {
		r := testRequest(t)
		mutate(&r)
		if Validate(r) == nil {
			t.Fatalf("accepted invalid request %+v", r)
		}
	}
	src, dst := testInventories("https://api.pingone.com")
	src.Resources[0].Configuration = nil
	if _, e := NewRequest(src, dst, "us", "us", []string{"flow"}, nil, ""); e == nil {
		t.Fatal("accepted unavailable config")
	}
	if _, e := Decode(strings.NewReader(strings.Repeat(" ", maxRequestBytes+1))); e == nil {
		t.Fatal("accepted oversized request")
	}
}
