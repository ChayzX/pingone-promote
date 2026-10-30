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

func TestRequestRoundTripsNativeSnapshotResourceIdentities(t *testing.T) {
	for _, tc := range []struct{ source, target, kind, sourcePath, targetPath string }{
		{"flowId:flow-one-version:3", "flowId:flow-two-version:7", "flows / versions / details", "flows/flow-one/versions/3/details", "flows/flow-two/versions/7/details"},
		{"type:brandingsettings", "type:brandingsettings", "brandingSettings", "brandingSettings", "brandingSettings"},
		{"type:notificationssettings:emaildeliverysettings", "type:notificationssettings:emaildeliverysettings", "notificationsSettings / emailDeliverySettings", "notificationsSettings/emailDeliverySettings", "notificationsSettings/emailDeliverySettings"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			src, dst := testInventories("https://api.pingone.com")
			src.Resources[0].ID, src.Resources[0].Type = tc.source, tc.kind
			dst.Resources[0].ID, dst.Resources[0].Type = tc.target, tc.kind
			src.Resources[0].URL = src.APIOrigin + "/v1/environments/source/" + tc.sourcePath
			dst.Resources[0].URL = dst.APIOrigin + "/v1/environments/target/" + tc.targetPath
			r, err := NewRequest(src, dst, "us", "us", []string{tc.source}, map[string]string{tc.source: tc.target}, "Native snapshot identity")
			if err != nil {
				t.Fatal(err)
			}
			raw, err := Encode(r)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := Decode(bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			if len(decoded.Selected) != 1 || decoded.Selected[0] != tc.source || decoded.Mappings[tc.source] != tc.target {
				t.Fatal("native resource identity changed during serialization")
			}
		})
	}
}

func TestRequestRejectsUnsupportedCompositeResourceIdentities(t *testing.T) {
	for _, id := range []string{"type:arbitrarysettings", "flowId:../escape-version:1", "flowId:flow-version:01", "flowId:flow-version:latest", "flowId:flow-version:1/extra", "type:brandingsettings/extra"} {
		t.Run(id, func(t *testing.T) {
			r := testRequest(t)
			r.Mappings = map[string]string{"flow": id}
			if Validate(r) == nil {
				t.Fatal("accepted unsupported mapping identity")
			}
			r.Mappings = nil
			r.Selected = []string{id}
			r.Configurations = map[string]any{id: r.Configurations["flow"]}
			r.Fingerprints = map[string]string{id: r.Fingerprints["flow"]}
			if Validate(r) == nil {
				t.Fatal("accepted unsupported selection identity")
			}
		})
	}
}
