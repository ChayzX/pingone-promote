package promotion

import (
	"encoding/json"
	"pingone-promote/internal/pingone"
	"strings"
	"testing"
)

func inventories() (pingone.Inventory, pingone.Inventory) {
	return pingone.Inventory{EnvironmentID: "src", OrganizationID: "org", SnapshotID: "snapshot", Resources: []pingone.Resource{{ID: "f", Name: "Sign in", Type: "flows", URL: "https://api.pingone.com/v1/environments/src/flows/f"}, {ID: "n", Name: "Email", Type: "templates", URL: "https://api.pingone.com/v1/environments/src/templates/n"}, {ID: "c", Name: "Connector", Type: "connections", URL: "https://api.pingone.com/v1/environments/src/connections/c"}}}, pingone.Inventory{EnvironmentID: "dst", OrganizationID: "org", Resources: []pingone.Resource{{ID: "g", Name: "Production sign in", Type: "flows", URL: "https://api.pingone.com/v1/environments/dst/flows/g"}, {ID: "m", Name: "Email", Type: "templates", URL: "https://api.pingone.com/v1/environments/dst/templates/m"}, {ID: "d", Name: "Connector", Type: "connections", URL: "https://api.pingone.com/v1/environments/dst/connections/d"}}}
}
func TestBuildRequestNeverDefaultsToEnvironmentWidePromotion(t *testing.T) {
	s, d := inventories()
	if _, err := BuildRequest(s, d, nil, nil, ""); err == nil {
		t.Fatal("empty selection accepted")
	}
	req, err := BuildRequest(s, d, []string{"f"}, map[string]string{"f": "g", "c": "d"}, "Release")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(req)
	text := string(raw)
	if !strings.Contains(text, `"selectedResources":["f"]`) || !strings.Contains(text, `"excludedResources":["c","n"]`) || !strings.Contains(text, `"c":"d"`) {
		t.Fatalf("selection/exclusion/reuse contract: %s", text)
	}
}
func TestBuildRequestRejectsInvalidMappings(t *testing.T) {
	s, d := inventories()
	for _, m := range []map[string]string{{"f": "m"}, {"f": "unknown"}, {"bad": "g"}} {
		if _, err := BuildRequest(s, d, []string{"f"}, m, ""); err == nil {
			t.Fatalf("accepted mapping %v", m)
		}
	}
	d.OrganizationID = "other"
	if _, err := BuildRequest(s, d, []string{"f"}, nil, ""); err == nil {
		t.Fatal("cross tenant accepted")
	}
}
func TestReviewBlocksUnselectedTemplateAndWrongTarget(t *testing.T) {
	s, d := inventories()
	base := pingone.Step{Action: "PUT", URL: d.Resources[0].URL, Source: pingone.Ref{ID: "f"}, Target: pingone.Ref{ID: "g"}, Payload: json.RawMessage(`{"name":"new"}`)}
	for _, change := range []string{"none", "unselected template", "wrong target", "forged type", "delete", "unknown source", "cross host", "sibling flow"} {
		t.Run(change, func(t *testing.T) {
			steps := []pingone.Step{base}
			switch change {
			case "unselected template":
				steps = append(steps, pingone.Step{Action: "PUT", URL: d.Resources[1].URL, Source: pingone.Ref{ID: "n"}})
			case "wrong target":
				steps[0].URL = strings.Replace(base.URL, "/dst/", "/other/", 1)
			case "forged type":
				steps[0].URL = d.Resources[1].URL
			case "delete":
				steps[0].Action = "DELETE"
			case "unknown source":
				steps[0].Source.ID = "version-not-in-inventory"
			case "cross host":
				steps[0].URL = strings.Replace(base.URL, "api.pingone.com", "attacker.example", 1)
			case "sibling flow":
				steps[0].URL = strings.Replace(base.URL, "/g", "/gg", 1)
			}
			r := Inspect(s, d, []string{"f"}, map[string]string{"f": "g"}, steps, false)
			if change == "none" {
				if len(r.Issues) > 0 || r.Digest == "" {
					t.Fatalf("valid review rejected %+v", r)
				}
			} else if len(r.Issues) == 0 {
				t.Fatal("unapproved change allowed")
			}
		})
	}
}
func TestTemplatesArePromotableOnlyWhenSelected(t *testing.T) {
	s, d := inventories()
	steps := []pingone.Step{{Action: "PUT", URL: d.Resources[1].URL, Source: pingone.Ref{ID: "n"}, Payload: json.RawMessage(`{"body":"Hello"}`)}}
	r := Inspect(s, d, []string{"n"}, map[string]string{"n": "m"}, steps, false)
	if len(r.Issues) > 0 {
		t.Fatal(r.Issues)
	}
}
func TestFlowVersionDescendantsStayInsideMappedFlow(t *testing.T) {
	s, d := inventories()
	steps := []pingone.Step{{Action: "POST", URL: d.Resources[0].URL + "/versions", Source: pingone.Ref{ID: "f"}, Payload: json.RawMessage(`{"graphData":{}}`)}}
	r := Inspect(s, d, []string{"f"}, map[string]string{"f": "g"}, steps, false)
	if len(r.Issues) > 0 {
		t.Fatal(r.Issues)
	}
}
func TestReviewFingerprintChangesWithPayloadAndRedactsSecrets(t *testing.T) {
	s, d := inventories()
	st := pingone.Step{Action: "PUT", URL: d.Resources[0].URL, Source: pingone.Ref{ID: "f"}, Payload: json.RawMessage(`{"password":"hidden","nested":{"clientSecret":"also hidden"},"name":"A"}`)}
	r := Inspect(s, d, []string{"f"}, map[string]string{"f": "g"}, []pingone.Step{st}, false)
	raw, _ := json.Marshal(r)
	if strings.Contains(string(raw), "hidden") {
		t.Fatalf("secret in preview %s", raw)
	}
	st.Payload = json.RawMessage(`{"name":"B"}`)
	r2 := Inspect(s, d, []string{"f"}, map[string]string{"f": "g"}, []pingone.Step{st}, false)
	if r.Digest == r2.Digest {
		t.Fatal("changed payload kept digest")
	}
}
func TestRollbackAllowsExplicitlyReviewedDeletesButNotOtherEnvironments(t *testing.T) {
	s, d := inventories()
	steps := []pingone.Step{{Action: "DELETE", URL: d.Resources[0].URL, Source: pingone.Ref{ID: "created"}}}
	r := Inspect(s, d, nil, nil, steps, true)
	if len(r.Issues) > 0 {
		t.Fatal(r.Issues)
	}
	steps[0].URL = s.Resources[0].URL
	r = Inspect(s, d, nil, nil, steps, true)
	if len(r.Issues) == 0 {
		t.Fatal("rollback escaped target")
	}
}

func TestUnmappedFlowCreationAndBoundVersionChild(t *testing.T) {
	s, d := inventories()
	d.Resources = nil
	d.APIOrigin = "https://api.pingone.com"
	root := pingone.Step{Action: "POST", URL: "https://api.pingone.com/v1/environments/dst/flows", Source: pingone.Ref{ID: "f"}, Target: pingone.Ref{ID: "new-flow"}, Payload: json.RawMessage(`{"id":"new-flow","name":"Created"}`)}
	child := pingone.Step{Action: "POST", URL: "https://api.pingone.com/v1/environments/dst/flows/new-flow/versions", Source: pingone.Ref{ID: "f"}, Payload: json.RawMessage(`{"graphData":{}}`)}
	r := Inspect(s, d, []string{"f"}, nil, []pingone.Step{root, child}, false)
	if len(r.Issues) > 0 {
		t.Fatal(r.Issues)
	}
	child.URL = "https://api.pingone.com/v1/environments/dst/flows/sibling/versions"
	r = Inspect(s, d, []string{"f"}, nil, []pingone.Step{root, child}, false)
	if len(r.Issues) == 0 {
		t.Fatal("unbound flow child allowed")
	}
}

func TestUnmappedFlowCreationRejectsTargetIDPayloadIDMismatch(t *testing.T) {
	s, d := inventories()
	d.Resources = nil
	d.APIOrigin = "https://api.pingone.com"
	root := pingone.Step{Action: "POST", URL: "https://api.pingone.com/v1/environments/dst/flows", Source: pingone.Ref{ID: "f"}, Target: pingone.Ref{ID: "target-flow"}, Payload: json.RawMessage(`{"id":"payload-flow","name":"Created"}`)}
	child := pingone.Step{Action: "POST", URL: "https://api.pingone.com/v1/environments/dst/flows/target-flow/versions", Source: pingone.Ref{ID: "f"}, Payload: json.RawMessage(`{"graphData":{}}`)}
	r := Inspect(s, d, []string{"f"}, nil, []pingone.Step{root, child}, false)
	if len(r.Issues) == 0 {
		t.Fatal("accepted flow creation whose target identity differs from its create payload")
	}
}

func TestRollbackDeleteRejectsTargetIDURLMismatch(t *testing.T) {
	s, d := inventories()
	del := pingone.Step{Action: "DELETE", URL: d.Resources[0].URL, Target: pingone.Ref{ID: "another-flow"}}
	r := Inspect(s, d, nil, nil, []pingone.Step{del}, true)
	if len(r.Issues) == 0 {
		t.Fatal("accepted rollback delete whose declared target differs from its URL")
	}
}

func TestBuildRequestPinsSnapshotAndTarget(t *testing.T) {
	s, d := inventories()
	s.SnapshotID = ""
	if _, err := BuildRequest(s, d, []string{"f"}, nil, ""); err == nil {
		t.Fatal("empty snapshot allowed")
	}
	s.SnapshotID = "snapshot"
	req, err := BuildRequest(s, d, []string{"f"}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(req)
	if !strings.Contains(string(b), `"targetEnvironment":{"id":"dst"}`) {
		t.Fatalf("target environment missing: %s", b)
	}
}

func TestInspectRejectsEncodedTraversalAndUsesConfiguredOrigin(t *testing.T) {
	s, d := inventories()
	d.APIOrigin = "https://api.pingone.com"
	base := pingone.Step{Action: "PUT", URL: "https://api.pingone.com/v1/environments/dst/flows/g", Source: pingone.Ref{ID: "f"}, Target: pingone.Ref{ID: "g"}}
	for _, path := range []string{"https://api.pingone.com/v1/environments/dst/flows/%2e%2e/g", "https://api.pingone.com/v1//environments/dst/flows/g", "https://api.pingone.com/v1/environments/dst/flows/g?x=1"} {
		st := base
		st.URL = path
		r := Inspect(s, d, []string{"f"}, map[string]string{"f": "g"}, []pingone.Step{st}, false)
		if len(r.Issues) == 0 {
			t.Fatalf("accepted unsafe URL %s", path)
		}
	}
	d.Resources = nil
	base.Action = "POST"
	base.Target.ID = ""
	base.URL = "https://api.pingone.com/v1/environments/dst/flows"
	base.Payload = json.RawMessage(`{"name":"Created"}`)
	r := Inspect(s, d, []string{"f"}, nil, []pingone.Step{base}, false)
	if len(r.Issues) > 0 {
		t.Fatalf("configured origin rejected with empty target inventory: %v", r.Issues)
	}
}

func TestReviewDiffsAgainstTargetAndRedactsPropertySecrets(t *testing.T) {
	s, d := inventories()
	d.Resources[0].Configuration = map[string]any{"name": "Old", "nested": map[string]any{"keep": true, "gone": 1}}
	st := pingone.Step{Action: "POST", URL: d.Resources[0].URL, Source: pingone.Ref{ID: "f"}, Target: pingone.Ref{ID: "g"}, Payload: json.RawMessage(`{"name":"New","nested":{"keep":true,"added":2},"apiKey":{"value":"hidden"},"auth":{"value":"also hidden"}}`)}
	r := Inspect(s, d, []string{"f"}, map[string]string{"f": "g"}, []pingone.Step{st}, false)
	if len(r.Issues) > 0 {
		t.Fatal(r.Issues)
	}
	got := r.Steps[0]
	if got.Before == nil || len(got.Changes) != 5 {
		t.Fatalf("missing before/diff: %+v", got)
	}
	b, _ := json.Marshal(got)
	if strings.Contains(string(b), "hidden") {
		t.Fatalf("secret exposed: %s", b)
	}
}

func TestInspectRejectsUninspectableMutationPayloads(t *testing.T) {
	s, d := inventories()
	for _, tc := range []struct {
		name    string
		payload json.RawMessage
	}{{"absent", nil}, {"null", json.RawMessage(`null`)}, {"scalar", json.RawMessage(`"secret"`)}, {"array", json.RawMessage(`[{"name":"hidden"}]`)}} {
		t.Run(tc.name, func(t *testing.T) {
			st := pingone.Step{Action: "PUT", URL: d.Resources[0].URL, Source: pingone.Ref{ID: "f"}, Target: pingone.Ref{ID: "g"}, Payload: tc.payload}
			r := Inspect(s, d, []string{"f"}, map[string]string{"f": "g"}, []pingone.Step{st}, false)
			if len(r.Issues) == 0 {
				t.Fatal("uninspectable payload accepted")
			}
		})
	}
}

func TestInspectAllowsRecognizedBodylessFlowActionAndRollbackDelete(t *testing.T) {
	s, d := inventories()
	st := pingone.Step{Action: "POST", URL: d.Resources[0].URL, Source: pingone.Ref{ID: "f"}, Target: pingone.Ref{ID: "g"}}
	r := Inspect(s, d, []string{"f"}, map[string]string{"f": "g"}, []pingone.Step{st}, false)
	if len(r.Issues) > 0 {
		t.Fatalf("mapped flow action rejected: %v", r.Issues)
	}
	del := pingone.Step{Action: "DELETE", URL: d.Resources[0].URL}
	r = Inspect(s, d, nil, nil, []pingone.Step{del}, true)
	if len(r.Issues) > 0 {
		t.Fatalf("rollback delete rejected: %v", r.Issues)
	}
}
