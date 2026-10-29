package gitops

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"pingone-promote/internal/pingone"
)

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
