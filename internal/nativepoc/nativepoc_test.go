package nativepoc_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"pingone-promote/internal/nativepoc"
	"pingone-promote/internal/nativepoc/mockapi"
)

func setup(t *testing.T) (*mockapi.Mock, *nativepoc.Client, nativepoc.Config, nativepoc.Request) {
	t.Helper()
	m := mockapi.Demo()
	t.Cleanup(m.Close)
	c := &nativepoc.Client{APIBase: m.URL, AuthBase: m.URL, WorkerEnv: "worker", ClientID: "id", Secret: "secret", Poll: time.Millisecond}
	cfg := nativepoc.Config{Environments: map[string]string{"dev": mockapi.DevID, "qa": mockapi.QAID}}
	var req nativepoc.Request
	if err := nativepoc.Load(filepath.Join("..", "..", "examples", "native-pipeline", "promotions", "CIAM-77.yml"), &req); err != nil {
		t.Fatal(err)
	}
	return m, c, cfg, req
}

func snapshot(m *mockapi.Mock, env string) string {
	b, _ := json.Marshal(m.Envs[env])
	return string(b)
}

// A plan changes nothing, names every operation with its fields, marks
// what PingOne added as a dependency, flags a value that points at the
// source, and never holds a secret.
func TestPlanShowsWhatChangesAndWritesNothing(t *testing.T) {
	m, c, cfg, req := setup(t)
	before := snapshot(m, mockapi.QAID)
	r, err := nativepoc.Plan(context.Background(), c, cfg, req)
	if err != nil {
		t.Fatal(err)
	}
	if m.Writes != 0 || snapshot(m, mockapi.QAID) != before {
		t.Fatal("planning changed the target")
	}
	if cr, up, del := r.Counts(); cr != 1 || up != 3 || del != 0 {
		t.Fatalf("counts = %d created, %d updated, %d deleted", cr, up, del)
	}
	byName := map[string]nativepoc.Item{}
	for _, it := range r.Items {
		byName[it.Name] = it
	}
	flow := byName["CIAM Sign On"]
	if !flow.Selected || flow.Action != "update" || flow.Type != "flow" {
		t.Errorf("flow item = %+v", flow)
	}
	paths := map[string]nativepoc.Change{}
	for _, ch := range flow.Changes {
		paths[ch.Path] = ch
	}
	if ch := paths["settings.logLevel"]; ch.Before != "1" || ch.After != "2" {
		t.Errorf("logLevel change = %+v", ch)
	}
	// A node added in the middle of the flow is one line, and the nodes
	// after it are not reported as changed.
	if ch := paths["graphData.nodes[mfa]"]; ch.Kind != "add" || !strings.Contains(ch.After, "Step-up MFA") {
		t.Errorf("added node = %+v", ch)
	}
	if _, moved := paths["graphData.nodes[done].name"]; moved {
		t.Error("an unchanged node after an insertion is reported as changed")
	}
	if byName["Splunk HEC"].Selected || byName["MFA Subflow"].Action != "create" {
		t.Errorf("dependencies = %+v, %+v", byName["Splunk HEC"], byName["MFA Subflow"])
	}
	if !strings.Contains(strings.Join(r.Warnings, "\n"), "properties.url: the new value looks specific to dev") {
		t.Errorf("no warning for the connector URL pointing at dev: %q", r.Warnings)
	}
	dir := t.TempDir()
	if err := nativepoc.Write(dir, r); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"plan.json", "mr-note.md", "report.html"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "SYNTHETIC") {
			t.Errorf("%s holds a secret value", name)
		}
	}
}

// Apply runs exactly the reviewed plan: it refuses when the target changed
// after the review, and a rollback puts the target back as it was.
func TestApplyRefusesAStalePlanAndRollbackRestores(t *testing.T) {
	m, c, cfg, req := setup(t)
	ctx := context.Background()
	original := snapshot(m, mockapi.QAID)
	plan, err := nativepoc.Plan(ctx, c, cfg, req)
	if err != nil {
		t.Fatal(err)
	}
	m.Edit(mockapi.QAID, "q-mfa", "value", "maybe")
	if _, err := nativepoc.Apply(ctx, c, plan); err == nil || !strings.Contains(err.Error(), "changed since the review") {
		t.Fatalf("stale apply: %v", err)
	}
	if m.Writes != 0 {
		t.Fatal("a refused apply wrote to the target")
	}
	m.Edit(mockapi.QAID, "q-mfa", "value", "false")

	tampered := plan
	tampered.Digest = strings.Repeat("0", 64)
	if _, err := nativepoc.Apply(ctx, c, tampered); err == nil {
		t.Fatal("a plan with another digest was applied")
	}

	p, err := nativepoc.Apply(ctx, c, plan)
	if err != nil || p.Status != "COMPLETED" {
		t.Fatalf("apply: %v %+v", err, p)
	}
	if got := m.Envs[mockapi.QAID]["q-mfa"].Config["value"]; got != "true" {
		t.Errorf("variable after apply = %v", got)
	}
	if _, ok := m.Envs[mockapi.QAID]["new-f-mfa"]; !ok {
		t.Error("the subflow was not created")
	}

	rb, err := nativepoc.PlanRollback(ctx, c, plan)
	if err != nil {
		t.Fatal(err)
	}
	if cr, up, del := rb.Counts(); cr != 0 || up != 3 || del != 1 || !rb.Rollback {
		t.Fatalf("rollback plan = %d/%d/%d rollback=%v", cr, up, del, rb.Rollback)
	}
	if _, err := nativepoc.Apply(ctx, c, rb); err != nil {
		t.Fatal(err)
	}
	var want, got any
	_ = json.Unmarshal([]byte(original), &want)
	_ = json.Unmarshal([]byte(snapshot(m, mockapi.QAID)), &got)
	if !reflect.DeepEqual(want, got) {
		t.Error("qa is not back as it was before the promotion")
	}
	if _, err := nativepoc.PlanRollback(ctx, c, plan); err == nil {
		t.Error("a promotion was rolled back twice")
	}
}

func TestRequestProblemsAreNamed(t *testing.T) {
	_, c, cfg, req := setup(t)
	req.Resources[0].Name = "No Such Flow"
	if _, err := nativepoc.Plan(context.Background(), c, cfg, req); err == nil || !strings.Contains(err.Error(), `no flow named "No Such Flow"`) {
		t.Errorf("unknown name: %v", err)
	}
	req.Target = "prod"
	if _, err := nativepoc.Plan(context.Background(), c, cfg, req); err == nil || !strings.Contains(err.Error(), `"prod" is not in promote.yml`) {
		t.Errorf("unknown environment: %v", err)
	}
}

func TestTypeOf(t *testing.T) {
	for in, want := range map[string]string{
		"https://api.pingone.com/v1/environments/e/flows/f":                     "flow",
		"https://api.pingone.com/v1/environments/e/applications/a/attributes/b": "applications/attributes",
		"https://api.pingone.com/v1/environments/e/keys/k":                      "keys",
	} {
		if got := nativepoc.TypeOf(in); got != want {
			t.Errorf("TypeOf(%s) = %q, want %q", in, got, want)
		}
	}
}
