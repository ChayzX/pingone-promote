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
func TestMappedFlowVersionDescendantsAreUnsupported(t *testing.T) {
	s, d := inventories()
	for _, suffix := range []string{"/versions", "/versions/1/details", "/versions/2/details/extra"} {
		t.Run(suffix, func(t *testing.T) {
			steps := []pingone.Step{{Action: "POST", URL: d.Resources[0].URL + suffix, Source: pingone.Ref{ID: "f"}, Payload: json.RawMessage(`{"graphData":{}}`)}}
			r := Inspect(s, d, []string{"f"}, map[string]string{"f": "g"}, steps, false)
			if len(r.Issues) == 0 {
				t.Fatal("mapped flow version descendant allowed without an explicitly selected version lifecycle")
			}
		})
	}
	s.Resources = append(s.Resources, pingone.Resource{ID: "flowId:f-version:1", Name: "Sign in version 1", Type: "flows / versions / details", URL: "https://api.pingone.com/v1/environments/src/flows/f/versions/1/details"})
	d.Resources = append(d.Resources, pingone.Resource{ID: "flowId:g-version:1", Name: "Production sign in version 1", Type: "flows / versions / details", URL: "https://api.pingone.com/v1/environments/dst/flows/g/versions/1/details"})
	unselectedVersion := []pingone.Step{{Action: "PUT", URL: d.Resources[len(d.Resources)-1].URL, Source: pingone.Ref{ID: "flowId:f-version:1"}, Target: pingone.Ref{ID: "flowId:g-version:1"}, Payload: json.RawMessage(`{"version":1}`)}}
	r := Inspect(s, d, []string{"f"}, map[string]string{"f": "g", "flowId:f-version:1": "flowId:g-version:1"}, unselectedVersion, false)
	if len(r.Issues) == 0 {
		t.Fatal("mapped version update was allowed without selecting its source version")
	}
}

func TestMappedFlowVersionUpdateMustMatchExplicitResource(t *testing.T) {
	s, d := inventories()
	s.Resources = append(s.Resources, pingone.Resource{ID: "flowId:f-version:1", Name: "Sign in version 1", Type: "flows / versions / details", URL: "https://api.pingone.com/v1/environments/src/flows/f/versions/1/details"})
	d.Resources = append(d.Resources, pingone.Resource{ID: "flowId:g-version:2", Name: "Production sign in version 2", Type: "flows / versions / details", URL: "https://api.pingone.com/v1/environments/dst/flows/g/versions/2/details"})
	steps := []pingone.Step{{Action: "PUT", URL: d.Resources[len(d.Resources)-1].URL, Source: pingone.Ref{ID: "flowId:f-version:1"}, Target: pingone.Ref{ID: "flowId:g-version:2"}, Payload: json.RawMessage(`{"version":1}`)}}
	r := Inspect(s, d, []string{"flowId:f-version:1"}, map[string]string{"flowId:f-version:1": "flowId:g-version:2"}, steps, false)
	if len(r.Issues) != 0 {
		t.Fatalf("exact explicitly mapped version update rejected: %v", r.Issues)
	}
	steps[0].URL = strings.Replace(steps[0].URL, "/versions/2/", "/versions/1/", 1)
	r = Inspect(s, d, []string{"flowId:f-version:1"}, map[string]string{"flowId:f-version:1": "flowId:g-version:2"}, steps, false)
	if len(r.Issues) == 0 {
		t.Fatal("mapped version update accepted URL for a different version")
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
	r := Inspect(s, d, []string{"f"}, nil, []pingone.Step{root}, false)
	if len(r.Issues) > 0 {
		t.Fatalf("root collection create unexpectedly blocked: %v", r.Issues)
	}
	r = Inspect(s, d, []string{"f"}, nil, []pingone.Step{root, child}, false)
	if len(r.Issues) == 0 {
		t.Fatal("root-sourced arbitrary flow-version child allowed without explicit version selection")
	}
}

func flowVersionLifecycleFixture() (pingone.Inventory, pingone.Inventory, []string, []pingone.Step) {
	const origin = "https://api.pingone.com"
	const rootID = "flow-a"
	const versionID = "flowId:flow-a-version:1"
	rootURL := origin + "/v1/environments/src/flows/" + rootID
	versionURL := origin + "/v1/environments/src/flows/" + rootID + "/versions/1/details"
	source := pingone.Inventory{APIOrigin: origin, EnvironmentID: "src", SnapshotID: "source-snapshot", OrganizationID: "org", Resources: []pingone.Resource{
		{ID: rootID, Name: "Flow A", Type: "flows", URL: rootURL, Dependencies: []string{"source-connector"}, Configuration: map[string]any{"id": rootID, "name": "Flow A", "enabled": true, "currentVersion": 1, "publishedVersion": 1, "environment": map[string]any{"id": "src"}, "graphData": map[string]any{"elements": map[string]any{"nodes": []any{map[string]any{"data": map[string]any{"connectionId": "source-connector"}}}}}}},
		{ID: versionID, Name: "Flow A version 1", Type: "flows / versions / details", URL: versionURL, Configuration: map[string]any{"flow": map[string]any{"id": rootID, "name": "Flow A"}, "version": 1, "graphData": map[string]any{"elements": map[string]any{"nodes": []any{map[string]any{"data": map[string]any{"connectionId": "source-connector"}}}}}}},
		{ID: "source-connector", Name: "Source connection", Type: "connections", URL: origin + "/v1/environments/src/connections/source-connector", Configuration: map[string]any{"name": "Source connection"}},
	}}
	target := pingone.Inventory{APIOrigin: origin, EnvironmentID: "dst", SnapshotID: "target-snapshot", OrganizationID: "org", Resources: []pingone.Resource{
		{ID: "target-connector", Name: "Target connection", Type: "connections", URL: origin + "/v1/environments/dst/connections/target-connector", Dependencies: []string{"target-dependency"}, Configuration: map[string]any{"name": "Target connection"}},
		{ID: "target-dependency", Name: "Target credential", Type: "credentials", URL: origin + "/v1/environments/dst/credentials/target-dependency", Configuration: map[string]any{"name": "Target credential"}},
	}}
	steps := []pingone.Step{
		{Action: "POST", URL: origin + "/v1/environments/dst/flows", Source: pingone.Ref{ID: rootID}, Payload: json.RawMessage(`{"color":"blue","connectors":[],"currentVersion":1,"deployedAt":null,"description":"Flow A","enabled":true,"environment":{"id":"dst"},"graphData":{"elements":{"nodes":[{"data":{"connectionId":"target-connector"}}]}},"id":"flow-a","name":"Flow A","publishedVersion":1,"settings":{}}`)},
		{Action: "PUT", URL: origin + "/v1/environments/dst/flows/flow-a/enabled", Payload: json.RawMessage(`{"enabled":true}`)},
		{Action: "POST", URL: origin + "/v1/environments/dst/flows/flow-a/versions", Source: pingone.Ref{ID: versionID}, Payload: json.RawMessage(`{"color":"blue","connectors":[],"deployedAt":null,"description":"Flow A","enabled":true,"flow":{"id":"flow-a","name":"Flow A"},"graphData":{"elements":{"nodes":[{"data":{"connectionId":"target-connector"}}]}},"id":"flowId:flow-a-version:1","settings":{},"updates":[],"version":1}`)},
		{Action: "POST", URL: origin + "/v1/environments/dst/flows/flow-a/publishedVersion", Payload: json.RawMessage(`{"version":"1"}`)},
	}
	return source, target, []string{rootID, versionID}, steps
}

func TestInspectSelectedFlowVersionCreateLifecycle(t *testing.T) {
	s, d, selected, steps := flowVersionLifecycleFixture()
	r := Inspect(s, d, selected, nil, steps, false)
	if len(r.Issues) != 0 {
		t.Fatalf("selected flow/version lifecycle rejected: %v", r.Issues)
	}
	if len(r.Steps) != 4 || r.Digest == "" {
		t.Fatalf("expected all four operations and a full-step digest: %#v", r)
	}
	for i, want := range []string{"flow-a", "flow-a", "flowId:flow-a-version:1", "flow-a"} {
		if r.Steps[i].SourceID != want {
			t.Fatalf("step %d owner=%q want %q", i, r.Steps[i].SourceID, want)
		}
	}
}

func TestInspectFlowLifecyclePreservesNewerCurrentAndSelectedPublishedVersion(t *testing.T) {
	source, target, selected, steps := flowVersionLifecycleFixture()
	source.Resources[0].Configuration["currentVersion"] = 2
	steps[0].Payload = json.RawMessage(strings.Replace(string(steps[0].Payload), `"currentVersion":1`, `"currentVersion":2`, 1))
	review := Inspect(source, target, selected, nil, steps, false)
	if len(review.Issues) != 0 {
		t.Fatalf("reviewed newer current version with selected published version rejected: %v", review.Issues)
	}
	if len(review.Steps) != 4 || review.Digest == "" {
		t.Fatal("complete lifecycle and digest required")
	}
	// A newer saved version is metadata, never permission to publish it.
	steps[3].Payload = json.RawMessage(`{"version":"2"}`)
	if changed := Inspect(source, target, selected, nil, steps, false); len(changed.Issues) == 0 {
		t.Fatal("publishing an unselected saved version must remain blocked")
	}
}

func TestFlowLifecycleExposesAllowedContentAndBindsItToRawDigest(t *testing.T) {
	s, d, selected, steps := flowVersionLifecycleFixture()
	first := Inspect(s, d, selected, nil, steps, false)
	steps[0].Payload = json.RawMessage(strings.Replace(string(steps[0].Payload), `"description":"Flow A"`, `"description":"Reviewed changed content"`, 1))
	second := Inspect(s, d, selected, nil, steps, false)
	if len(second.Issues) != 0 {
		t.Fatalf("an allowlisted content change should remain explicitly reviewable: %v", second.Issues)
	}
	if first.Digest == second.Digest {
		t.Fatal("canonical raw step digest ignored a changed allowlisted payload field")
	}
	after, ok := second.Steps[0].After.(map[string]any)
	if !ok || after["description"] != "Reviewed changed content" {
		t.Fatalf("changed allowlisted content is absent from redacted review: %#v", second.Steps[0].After)
	}
}

func TestInspectTreatsFlowVersionSourceNamesAsDigestBoundDisplayMetadata(t *testing.T) {
	source, target, selected, baseSteps := flowVersionLifecycleFixture()
	base := Inspect(source, target, selected, nil, baseSteps, false)
	if len(base.Issues) != 0 {
		t.Fatalf("empty optional source names rejected: %v", base.Issues)
	}
	for _, tc := range []struct {
		name      string
		root, ver bool
	}{
		{name: "root", root: true},
		{name: "version", ver: true},
		{name: "both", root: true, ver: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			steps := append([]pingone.Step(nil), baseSteps...)
			if tc.root {
				steps[0].Source.Name = "Native root display label"
			}
			if tc.ver {
				steps[2].Source.Name = "Native version display label"
			}
			review := Inspect(source, target, selected, nil, steps, false)
			if len(review.Issues) != 0 {
				t.Fatalf("nonempty source display metadata rejected: %v", review.Issues)
			}
			if review.Digest == base.Digest {
				t.Fatal("sourceRef.Name was omitted from the canonical raw-step digest")
			}
		})
	}
}

func TestInspectProvidesDistinctSafeFlowLifecycleReasons(t *testing.T) {
	tests := []struct {
		name   string
		want   string
		mutate func(*pingone.Inventory, *pingone.Inventory, *[]pingone.Step)
	}{
		{name: "root destination URL", want: "root-step-target-url", mutate: func(_ *pingone.Inventory, _ *pingone.Inventory, steps *[]pingone.Step) {
			(*steps)[0].URL = strings.Replace((*steps)[0].URL, "/flows", "/templates", 1)
		}},
		{name: "source root URL", want: "source-root-url", mutate: func(source *pingone.Inventory, _ *pingone.Inventory, _ *[]pingone.Step) {
			source.Resources[0].URL += "/details"
		}},
		{name: "source version URL", want: "source-version-url", mutate: func(source *pingone.Inventory, _ *pingone.Inventory, _ *[]pingone.Step) {
			source.Resources[1].URL += "/extra"
		}},
		{name: "target root name collision", want: "target-root-name-collision", mutate: func(_ *pingone.Inventory, target *pingone.Inventory, _ *[]pingone.Step) {
			target.Resources = append(target.Resources, pingone.Resource{ID: "other-flow", Name: "Flow A", Type: "flows", URL: "https://api.pingone.com/v1/environments/dst/flows/other-flow"})
		}},
		{name: "target root id collision", want: "target-root-id-collision", mutate: func(_ *pingone.Inventory, target *pingone.Inventory, _ *[]pingone.Step) {
			target.Resources = append(target.Resources, pingone.Resource{ID: "flow-a", Name: "Other", Type: "flows", URL: "https://api.pingone.com/v1/environments/dst/flows/flow-a"})
		}},
		{name: "target version id collision", want: "target-version-id-collision", mutate: func(_ *pingone.Inventory, target *pingone.Inventory, _ *[]pingone.Step) {
			target.Resources = append(target.Resources, pingone.Resource{ID: "flowId:flow-a-version:1", Name: "Existing version", Type: "flows / versions / details", URL: "https://api.pingone.com/v1/environments/dst/flows/flow-a/versions/1/details"})
		}},
		{name: "root schema", want: "root-payload-schema", mutate: func(_ *pingone.Inventory, _ *pingone.Inventory, steps *[]pingone.Step) {
			(*steps)[0].Payload = json.RawMessage(`{"id":"flow-a"}`)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			source, target, selected, original := flowVersionLifecycleFixture()
			steps := append([]pingone.Step(nil), original...)
			tc.mutate(&source, &target, &steps)
			review := Inspect(source, target, selected, nil, steps, false)
			want := "selected flow/version create lifecycle is unsupported or mismatched: " + tc.want
			found := false
			for _, issue := range review.Issues {
				if issue == want {
					found = true
					if strings.Contains(issue, "flow-a") || strings.Contains(issue, "Flow A") {
						t.Fatalf("diagnostic disclosed an identifier or name: %q", issue)
					}
				}
			}
			if !found {
				t.Fatalf("missing safe reason %q in issues: %v", want, review.Issues)
			}
		})
	}
}

func TestInspectRejectsInvalidSelectedFlowVersionCreateLifecycle(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*pingone.Inventory, *pingone.Inventory, *[]string, *map[string]string, *[]pingone.Step)
	}{
		{"unselected version", func(_ *pingone.Inventory, _ *pingone.Inventory, selected *[]string, _ *map[string]string, _ *[]pingone.Step) {
			*selected = []string{"flow-a"}
		}},
		{"malformed selected version cannot fall back", func(s *pingone.Inventory, _ *pingone.Inventory, selected *[]string, _ *map[string]string, steps *[]pingone.Step) {
			s.Resources[1].ID = "unlinked-version"
			*selected = []string{"flow-a", "unlinked-version"}
			*steps = (*steps)[:1]
		}},
		{"version selected under a different root cannot fall back", func(s *pingone.Inventory, _ *pingone.Inventory, selected *[]string, _ *map[string]string, steps *[]pingone.Step) {
			s.Resources[1].ID = "flowId:flow-b-version:1"
			*selected = []string{"flow-a", "flowId:flow-b-version:1"}
			*steps = (*steps)[:1]
		}},
		{"extra selected resource", func(s *pingone.Inventory, _ *pingone.Inventory, selected *[]string, _ *map[string]string, _ *[]pingone.Step) {
			s.Resources = append(s.Resources, pingone.Resource{ID: "extra", Name: "Extra", Type: "templates", URL: "https://api.pingone.com/v1/environments/src/templates/extra"})
			*selected = append(*selected, "extra")
		}},
		{"root mapping unsupported", func(_ *pingone.Inventory, _ *pingone.Inventory, _ *[]string, mappings *map[string]string, _ *[]pingone.Step) {
			*mappings = map[string]string{"flow-a": "target-flow"}
		}},
		{"version mapping unsupported", func(_ *pingone.Inventory, _ *pingone.Inventory, _ *[]string, mappings *map[string]string, _ *[]pingone.Step) {
			*mappings = map[string]string{"flowId:flow-a-version:1": "target-version"}
		}},
		{"wrong root owner", func(_ *pingone.Inventory, _ *pingone.Inventory, _ *[]string, _ *map[string]string, steps *[]pingone.Step) {
			(*steps)[0].Source.ID = "flow-b"
		}},
		{"root source ID mismatch despite matching display name", func(_ *pingone.Inventory, _ *pingone.Inventory, _ *[]string, _ *map[string]string, steps *[]pingone.Step) {
			(*steps)[0].Source.ID = "flow-b"
			(*steps)[0].Source.Name = "Flow A"
		}},
		{"version source ID mismatch despite matching display name", func(_ *pingone.Inventory, _ *pingone.Inventory, _ *[]string, _ *map[string]string, steps *[]pingone.Step) {
			(*steps)[2].Source.ID = "flowId:flow-a-version:2"
			(*steps)[2].Source.Name = "Flow A version 1"
		}},
		{"source root version mismatch", func(s *pingone.Inventory, _ *pingone.Inventory, _ *[]string, _ *map[string]string, _ *[]pingone.Step) {
			s.Resources[0].Configuration["currentVersion"] = 2
		}},
		{"source root environment mismatch", func(s *pingone.Inventory, _ *pingone.Inventory, _ *[]string, _ *map[string]string, _ *[]pingone.Step) {
			s.Resources[0].Configuration["environment"] = map[string]any{"id": "other"}
		}},
		{"root target resource supplied", func(_ *pingone.Inventory, _ *pingone.Inventory, _ *[]string, _ *map[string]string, steps *[]pingone.Step) {
			(*steps)[0].Target.ID = "flow-a"
		}},
		{"enabled source supplied", func(_ *pingone.Inventory, _ *pingone.Inventory, _ *[]string, _ *map[string]string, steps *[]pingone.Step) {
			(*steps)[1].Source.ID = "flow-a"
		}},
		{"enabled target supplied", func(_ *pingone.Inventory, _ *pingone.Inventory, _ *[]string, _ *map[string]string, steps *[]pingone.Step) {
			(*steps)[1].Target.ID = "flow-a"
		}},
		{"publish source supplied", func(_ *pingone.Inventory, _ *pingone.Inventory, _ *[]string, _ *map[string]string, steps *[]pingone.Step) {
			(*steps)[3].Source.ID = "flow-a"
		}},
		{"publish target supplied", func(_ *pingone.Inventory, _ *pingone.Inventory, _ *[]string, _ *map[string]string, steps *[]pingone.Step) {
			(*steps)[3].Target.ID = "flow-a"
		}},
		{"wrong destination environment", func(_ *pingone.Inventory, _ *pingone.Inventory, _ *[]string, _ *map[string]string, steps *[]pingone.Step) {
			(*steps)[1].URL = strings.Replace((*steps)[1].URL, "/dst/", "/other/", 1)
		}},
		{"wrong API origin", func(_ *pingone.Inventory, _ *pingone.Inventory, _ *[]string, _ *map[string]string, steps *[]pingone.Step) {
			(*steps)[3].URL = strings.Replace((*steps)[3].URL, "api.pingone.com", "other.example", 1)
		}},
		{"extra step", func(_ *pingone.Inventory, _ *pingone.Inventory, _ *[]string, _ *map[string]string, steps *[]pingone.Step) {
			*steps = append(*steps, (*steps)[3])
		}},
		{"missing lifecycle step does not fall back", func(_ *pingone.Inventory, _ *pingone.Inventory, _ *[]string, _ *map[string]string, steps *[]pingone.Step) {
			*steps = (*steps)[:3]
		}},
		{"wrong order", func(_ *pingone.Inventory, _ *pingone.Inventory, _ *[]string, _ *map[string]string, steps *[]pingone.Step) {
			(*steps)[1], (*steps)[2] = (*steps)[2], (*steps)[1]
		}},
		{"sibling version path", func(_ *pingone.Inventory, _ *pingone.Inventory, _ *[]string, _ *map[string]string, steps *[]pingone.Step) {
			(*steps)[2].URL = strings.Replace((*steps)[2].URL, "/flow-a/", "/sibling/", 1)
		}},
		{"wrong version source", func(_ *pingone.Inventory, _ *pingone.Inventory, _ *[]string, _ *map[string]string, steps *[]pingone.Step) {
			(*steps)[2].Source.ID = "flowId:flow-a-version:2"
		}},
		{"version target resource supplied", func(_ *pingone.Inventory, _ *pingone.Inventory, _ *[]string, _ *map[string]string, steps *[]pingone.Step) {
			(*steps)[2].Target.ID = "flowId:flow-a-version:1"
		}},
		{"enabled false", func(_ *pingone.Inventory, _ *pingone.Inventory, _ *[]string, _ *map[string]string, steps *[]pingone.Step) {
			(*steps)[1].Payload = json.RawMessage(`{"enabled":false}`)
		}},
		{"extra enabled payload field", func(_ *pingone.Inventory, _ *pingone.Inventory, _ *[]string, _ *map[string]string, steps *[]pingone.Step) {
			(*steps)[1].Payload = json.RawMessage(`{"enabled":true,"extra":true}`)
		}},
		{"root identity mismatch", func(_ *pingone.Inventory, _ *pingone.Inventory, _ *[]string, _ *map[string]string, steps *[]pingone.Step) {
			(*steps)[0].Payload = json.RawMessage(strings.Replace(string((*steps)[0].Payload), `"id":"flow-a"`, `"id":"flow-b"`, 1))
		}},
		{"root environment mismatch", func(_ *pingone.Inventory, _ *pingone.Inventory, _ *[]string, _ *map[string]string, steps *[]pingone.Step) {
			(*steps)[0].Payload = json.RawMessage(strings.Replace(string((*steps)[0].Payload), `"id":"dst"`, `"id":"other"`, 1))
		}},
		{"root disabled", func(_ *pingone.Inventory, _ *pingone.Inventory, _ *[]string, _ *map[string]string, steps *[]pingone.Step) {
			(*steps)[0].Payload = json.RawMessage(strings.Replace(string((*steps)[0].Payload), `"enabled":true`, `"enabled":false`, 1))
		}},
		{"root current version mismatch", func(_ *pingone.Inventory, _ *pingone.Inventory, _ *[]string, _ *map[string]string, steps *[]pingone.Step) {
			(*steps)[0].Payload = json.RawMessage(strings.Replace(string((*steps)[0].Payload), `"currentVersion":1`, `"currentVersion":2`, 1))
		}},
		{"root published version mismatch", func(_ *pingone.Inventory, _ *pingone.Inventory, _ *[]string, _ *map[string]string, steps *[]pingone.Step) {
			(*steps)[0].Payload = json.RawMessage(strings.Replace(string((*steps)[0].Payload), `"publishedVersion":1`, `"publishedVersion":2`, 1))
		}},
		{"root extra field", func(_ *pingone.Inventory, _ *pingone.Inventory, _ *[]string, _ *map[string]string, steps *[]pingone.Step) {
			(*steps)[0].Payload = json.RawMessage(strings.TrimSuffix(string((*steps)[0].Payload), "}") + `,"execute":true}`)
		}},
		{"version parent mismatch", func(_ *pingone.Inventory, _ *pingone.Inventory, _ *[]string, _ *map[string]string, steps *[]pingone.Step) {
			(*steps)[2].Payload = json.RawMessage(strings.Replace(string((*steps)[2].Payload), `"id":"flow-a"`, `"id":"sibling"`, 1))
		}},
		{"version value mismatch", func(_ *pingone.Inventory, _ *pingone.Inventory, _ *[]string, _ *map[string]string, steps *[]pingone.Step) {
			(*steps)[2].Payload = json.RawMessage(strings.Replace(string((*steps)[2].Payload), `"version":1`, `"version":2`, 1))
		}},
		{"version value string instead of number", func(_ *pingone.Inventory, _ *pingone.Inventory, _ *[]string, _ *map[string]string, steps *[]pingone.Step) {
			(*steps)[2].Payload = json.RawMessage(strings.Replace(string((*steps)[2].Payload), `"version":1`, `"version":"1"`, 1))
		}},
		{"version ID mismatch", func(_ *pingone.Inventory, _ *pingone.Inventory, _ *[]string, _ *map[string]string, steps *[]pingone.Step) {
			(*steps)[2].Payload = json.RawMessage(strings.Replace(string((*steps)[2].Payload), "flowId:flow-a-version:1", "flowId:flow-a-version:2", 1))
		}},
		{"version extra field", func(_ *pingone.Inventory, _ *pingone.Inventory, _ *[]string, _ *map[string]string, steps *[]pingone.Step) {
			(*steps)[2].Payload = json.RawMessage(strings.TrimSuffix(string((*steps)[2].Payload), "}") + `,"execute":true}`)
		}},
		{"publish version mismatch", func(_ *pingone.Inventory, _ *pingone.Inventory, _ *[]string, _ *map[string]string, steps *[]pingone.Step) {
			(*steps)[3].Payload = json.RawMessage(`{"version":"2"}`)
		}},
		{"extra publish payload field", func(_ *pingone.Inventory, _ *pingone.Inventory, _ *[]string, _ *map[string]string, steps *[]pingone.Step) {
			(*steps)[3].Payload = json.RawMessage(`{"version":"1","extra":true}`)
		}},
		{"target root ID already exists", func(_ *pingone.Inventory, d *pingone.Inventory, _ *[]string, _ *map[string]string, _ *[]pingone.Step) {
			d.Resources = append(d.Resources, pingone.Resource{ID: "flow-a", Name: "Other", Type: "flows", URL: "https://api.pingone.com/v1/environments/dst/flows/flow-a"})
		}},
		{"target root name already exists", func(_ *pingone.Inventory, d *pingone.Inventory, _ *[]string, _ *map[string]string, _ *[]pingone.Step) {
			d.Resources = append(d.Resources, pingone.Resource{ID: "other-flow", Name: "Flow A", Type: "flows", URL: "https://api.pingone.com/v1/environments/dst/flows/other-flow"})
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, d, selected, original := flowVersionLifecycleFixture()
			steps := append([]pingone.Step(nil), original...)
			var mappings map[string]string
			tc.mutate(&s, &d, &selected, &mappings, &steps)
			r := Inspect(s, d, selected, mappings, steps, false)
			if len(r.Issues) == 0 {
				t.Fatal("invalid flow/version lifecycle was accepted")
			}
		})
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

func TestReviewRedactsSchemaMarkedAndCommonCredentials(t *testing.T) {
	s, d := inventories()
	const canary = "SYNTHETIC-REDACTION-CANARY"
	st := pingone.Step{Action: "PUT", URL: d.Resources[0].URL, Source: pingone.Ref{ID: "f"}, Target: pingone.Ref{ID: "g"}, Payload: json.RawMessage(`{"DaVinci":{"dataType":"secret","value":"SYNTHETIC-REDACTION-CANARY"},"nested":{"IsSensitive":true,"value":"SYNTHETIC-REDACTION-CANARY"},"passphrase":"SYNTHETIC-REDACTION-CANARY","connectionString":"SYNTHETIC-REDACTION-CANARY","authorization":"SYNTHETIC-REDACTION-CANARY","dsn":"SYNTHETIC-REDACTION-CANARY","connectionUri":"SYNTHETIC-REDACTION-CANARY","safe":{"value":"visible"},"array":[{"dataType":"secret","value":"SYNTHETIC-REDACTION-CANARY"}]}`)}
	r := Inspect(s, d, []string{"f"}, map[string]string{"f": "g"}, []pingone.Step{st}, false)
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), canary) {
		t.Fatalf("synthetic secret reached serialized review: %s", raw)
	}
	if !strings.Contains(string(raw), `"value":"visible"`) {
		t.Fatalf("ordinary value was redacted: %s", raw)
	}
	first, err := json.Marshal(Redact(r.Steps[0].After))
	if err != nil {
		t.Fatal(err)
	}
	second, err := json.Marshal(Redact(Redact(r.Steps[0].After)))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("redaction was not idempotent: %s != %s", first, second)
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

func TestInspectEmptyPlanDoesNotReportUnsupportedFlowLifecycle(t *testing.T) {
	s, d, selected, _ := flowVersionLifecycleFixture()
	r := Inspect(s, d, selected, nil, nil, false)
	if len(r.Issues) != 1 || r.Issues[0] != "promotion plan has no steps" {
		t.Fatalf("empty plan misclassified: %v", r.Issues)
	}
	if len(r.Steps) != 0 {
		t.Fatal("empty plan gained operations")
	}
}
