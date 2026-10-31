package gitops

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"pingone-promote/internal/pingone"
)

func TestFormDriftTreatsFieldTypesAsAnUnorderedSetOnly(t *testing.T) {
	base := pingone.Resource{Type: "forms", Configuration: map[string]any{
		"fieldTypes": []any{"TEXT", "SUBMIT_BUTTON", "PASSWORD"},
		"components": []any{"first", "second"}, "name": "Form",
	}}
	reordered := pingone.Resource{Type: "forms", Configuration: map[string]any{
		"fieldTypes": []any{"PASSWORD", "TEXT", "SUBMIT_BUTTON"},
		"components": []any{"first", "second"}, "name": "Form",
	}}
	if driftConfigurationFingerprint(base) == "" || driftConfigurationFingerprint(base) != driftConfigurationFingerprint(reordered) {
		t.Fatal("reordered read-only form field type set caused drift")
	}
	for name, changed := range map[string]pingone.Resource{
		"different member": {Type: "forms", Configuration: map[string]any{"fieldTypes": []any{"PASSWORD", "TEXT", "OTHER"}, "components": []any{"first", "second"}, "name": "Form"}},
		"component order":  {Type: "forms", Configuration: map[string]any{"fieldTypes": []any{"PASSWORD", "TEXT", "SUBMIT_BUTTON"}, "components": []any{"second", "first"}, "name": "Form"}},
		"other type":       {Type: "flows", Configuration: reordered.Configuration},
	} {
		if name == "other type" {
			original := base
			original.Type = "flows"
			if driftConfigurationFingerprint(original) == driftConfigurationFingerprint(changed) {
				t.Error("normalized a non-form array")
			}
		} else if driftConfigurationFingerprint(base) == driftConfigurationFingerprint(changed) {
			t.Errorf("missed %s drift", name)
		}
	}
	malformed := pingone.Resource{Type: "forms", Configuration: map[string]any{"fieldTypes": []any{"TEXT", 123}}}
	if driftConfigurationFingerprint(malformed) != "" {
		t.Error("accepted malformed fieldTypes")
	}
}

func TestApplyChecksFormDependencyDespiteUnorderedFieldTypes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		fields     []any
		components []any
		allowed    bool
	}{
		{"reordered field types", []any{"PASSWORD", "TEXT"}, []any{"first", "second"}, true},
		{"changed field type", []any{"PASSWORD", "OTHER"}, []any{"first", "second"}, false},
		{"reordered components", []any{"PASSWORD", "TEXT"}, []any{"second", "first"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := fixture(t)
			_, dst := testInventories(f.origin)
			dst.Resources[0].Dependencies = []string{"form1"}
			dst.Resources = append(dst.Resources, pingone.Resource{ID: "form1", Type: "forms", URL: f.origin + "/v1/environments/target/forms/form1", Configuration: map[string]any{"fieldTypes": []any{"TEXT", "PASSWORD"}, "components": []any{"first", "second"}}})
			f.targetInventory = &dst
			e := &Engine{Client: f.client}
			a, err := e.Plan(context.Background(), f.req, testOrigin())
			if err != nil || len(a.Review.Issues) != 0 {
				t.Fatalf("plan: %v issues=%v", err, a.Review.Issues)
			}
			f.liveTarget = func(in []pingone.Resource) []pingone.Resource {
				out := append([]pingone.Resource(nil), in...)
				for i := range out {
					if out[i].ID == "form1" {
						out[i].Configuration = map[string]any{"fieldTypes": tc.fields, "components": tc.components}
					}
				}
				return out
			}
			authorized := false
			_, err = e.Apply(context.Background(), a, a.Review.Digest, func(context.Context, Artifact) error { authorized = true; return nil })
			if tc.allowed {
				if err != nil || !authorized || f.execute != 1 {
					t.Fatalf("expected execution: err=%v authorized=%v execute=%d", err, authorized, f.execute)
				}
			} else if !errors.Is(err, ErrTargetDrift) || authorized || f.execute != 0 {
				t.Fatalf("drift not blocked before execution: err=%v authorized=%v execute=%d", err, authorized, f.execute)
			}
		})
	}
}

func TestApplyBlocksConcurrentCollectionCreateWithFutureTargetID(t *testing.T) {
	f := fixture(t)
	source, target := testInventories(f.origin)
	request, err := NewRequest(source, target, "us", "us", []string{"flow"}, nil, "create")
	if err != nil {
		t.Fatal(err)
	}
	f.native.Mapping = nil
	f.steps = []pingone.Step{{Action: "POST", URL: f.origin + "/v1/environments/target/flows", Source: pingone.Ref{ID: "flow"}, Target: pingone.Ref{ID: "future-id"}, Payload: json.RawMessage(`{"name":"Flow"}`)}}
	e := &Engine{Client: f.client}
	a, err := e.Plan(context.Background(), request, testOrigin())
	if err != nil || len(a.Review.Issues) != 0 {
		t.Fatalf("plan err=%v issues=%v", err, a.Review.Issues)
	}
	f.liveTarget = func(in []pingone.Resource) []pingone.Resource {
		return append(in, pingone.Resource{ID: "concurrent-id", URL: f.origin + "/v1/environments/target/flows/concurrent-id", Type: "flows", Configuration: map[string]any{"name": "Flow"}})
	}
	_, err = e.Apply(context.Background(), a, a.Review.Digest, func(context.Context, Artifact) error { return nil })
	if !errors.Is(err, ErrTargetDrift) || f.execute != 0 {
		t.Fatalf("concurrent create into planned collection was not blocked: err=%v execute=%d", err, f.execute)
	}
}

func TestApplyRejectsTargetDriftBeforeAuthorization(t *testing.T) {
	f := fixture(t)
	e := &Engine{Client: f.client}
	a, err := e.Plan(context.Background(), f.req, testOrigin())
	if err != nil {
		t.Fatal(err)
	}
	f.targetChanged = true
	authorized := false
	_, err = e.Apply(context.Background(), a, a.Review.Digest, func(context.Context, Artifact) error {
		authorized = true
		return nil
	})
	if !errors.Is(err, ErrTargetDrift) || authorized || f.execute != 0 {
		t.Fatalf("target drift reached authorization or execution: err=%v authorized=%v execute=%d", err, authorized, f.execute)
	}
}
