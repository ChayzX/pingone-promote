package checks

import (
	"strings"
	"testing"

	"pingone-promote/internal/gitops"
	"pingone-promote/internal/promotion"
)

const api = "https://api.pingone.com"

func planArtifact() gitops.Artifact {
	return gitops.Artifact{
		Version: 1, Kind: "promotion", NativeID: "native-1",
		Request: gitops.Request{
			Source:   gitops.Environment{ID: "src", Region: "us", SnapshotID: "srcpin"},
			Target:   gitops.Environment{ID: "tgt", Region: "us"},
			Selected: []string{"flow"}, Mappings: map[string]string{"flow": "dest"},
		},
		Origin:           gitops.Origin{PipelineID: "1", CommitSHA: strings.Repeat("a", 40)},
		TargetSnapshotID: "tgtpin",
		Review: promotion.Review{
			Digest: strings.Repeat("d", 64), Issues: []string{},
			Steps: []promotion.StepReview{{Action: "PUT", URL: api + "/v1/environments/tgt/flows/dest", SourceID: "flow", TargetID: "dest", Name: "Login", Changes: []string{"name"}}},
		},
	}
}

func status(r Report, name string) Status {
	var s Status
	for _, c := range r.Checks {
		if c.Name == name {
			if c.Status == Fail || s == "" || (s == Pass && c.Status == Warn) {
				s = c.Status
			}
		}
	}
	return s
}

func TestCheckPlanPassesMatchingPlan(t *testing.T) {
	a := planArtifact()
	r := CheckPlan(a, []byte(`{"redacted":"[REDACTED]"}`), PlanOptions{
		DigestFileContent: strings.Repeat("d", 64) + "\n", ExpectSelected: []string{"flow"},
		ForbidTypes: []string{"templates"}, Secrets: []string{"s3cret-value"},
	})
	if r.Failed() {
		t.Fatalf("unexpected failure:\n%s", r.Markdown())
	}
	for _, n := range []string{"digest", "review issues", "selection", "target scope", "selection scope", "forbidden type", "secrets"} {
		if status(r, n) != Pass {
			t.Fatalf("%s = %q\n%s", n, status(r, n), r.Markdown())
		}
	}
}

func TestCheckPlanFailures(t *testing.T) {
	cases := map[string]struct {
		mutate func(*gitops.Artifact, *PlanOptions, *[]byte)
		check  string
	}{
		"digest mismatch": {func(a *gitops.Artifact, o *PlanOptions, _ *[]byte) { o.DigestFileContent = strings.Repeat("e", 64) }, "digest"},
		"review issues":   {func(a *gitops.Artifact, _ *PlanOptions, _ *[]byte) { a.Review.Issues = []string{"step 1: unknown"} }, "review issues"},
		"no steps":        {func(a *gitops.Artifact, _ *PlanOptions, _ *[]byte) { a.Review.Steps = nil }, "steps"},
		"selection differs": {func(a *gitops.Artifact, o *PlanOptions, _ *[]byte) {
			o.ExpectSelected = []string{"flow", "template"}
		}, "selection"},
		"other environment": {func(a *gitops.Artifact, _ *PlanOptions, _ *[]byte) {
			a.Review.Steps[0].URL = api + "/v1/environments/prod/flows/dest"
		}, "target scope"},
		"traversal path": {func(a *gitops.Artifact, _ *PlanOptions, _ *[]byte) {
			a.Review.Steps[0].URL = api + "/v1/environments/tgt/../prod/flows"
		}, "target scope"},
		"unselected source": {func(a *gitops.Artifact, _ *PlanOptions, _ *[]byte) {
			a.Review.Steps = append(a.Review.Steps, promotion.StepReview{Action: "POST", URL: api + "/v1/environments/tgt/templates", SourceID: "template"})
		}, "selection scope"},
		"forbidden type": {func(a *gitops.Artifact, o *PlanOptions, _ *[]byte) {
			o.ForbidTypes = []string{"Templates"}
			a.Review.Steps[0].URL = api + "/v1/environments/tgt/templates/email/contents/x"
		}, "forbidden type"},
		"secret leaked": {func(a *gitops.Artifact, o *PlanOptions, raw *[]byte) {
			o.Secrets = []string{"s3cret-value"}
			*raw = []byte(`{"clientSecret":"s3cret-value"}`)
		}, "secrets"},
		"rollback unlinked": {func(a *gitops.Artifact, _ *PlanOptions, _ *[]byte) {
			a.Kind = "rollback"
			a.OriginalNativeID = a.NativeID
		}, "rollback link"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			a, o, raw := planArtifact(), PlanOptions{}, []byte("{}")
			c.mutate(&a, &o, &raw)
			r := CheckPlan(a, raw, o)
			if status(r, c.check) != Fail || !r.Failed() {
				t.Fatalf("%s not failed:\n%s", c.check, r.Markdown())
			}
		})
	}
}

func TestCheckPlanWarnsWithoutSecretsAndIgnoresShortValues(t *testing.T) {
	r := CheckPlan(planArtifact(), []byte(`{"v":"abc"}`), PlanOptions{Secrets: []string{"abc"}})
	if status(r, "secrets") != Warn || r.Failed() {
		t.Fatalf("short secret handling:\n%s", r.Markdown())
	}
}

func TestPlanSummaryListsStepsAndEscapesCells(t *testing.T) {
	a := planArtifact()
	a.Review.Steps[0].Name = "Login | Main"
	s := PlanSummary(a)
	for _, want := range []string{"Promotion review: native-1", "`flow` → `dest`", "flows/dest", `Login \| Main`} {
		if !strings.Contains(s, want) {
			t.Fatalf("summary missing %q:\n%s", want, s)
		}
	}
}
