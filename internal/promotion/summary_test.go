package promotion

import (
	"strings"
	"testing"
)

func TestSummarizeNativeSteps(t *testing.T) {
	review := Review{Steps: []StepReview{
		{Action: "POST", URL: "https://api.pingone.com/v1/environments/e/flows", Name: "A | flow"},
		{Action: "PUT", URL: "https://api.pingone.com/v1/environments/e/flows/f", Name: "A | flow"},
		{Action: "DELETE", URL: "https://api.pingone.com/v1/environments/e/flows/old", TargetID: "old", Name: "Old"},
		{Action: "POST", URL: "https://api.pingone.com/v1/environments/e/flows/f/publishedVersion", Name: "Publish"},
		{Action: "TRACE", URL: "https://api.pingone.com/v1/environments/e/flows/f", Name: "Unknown"},
	}}
	got := Summarize(review.Steps)
	if got.Create != 1 || got.Change != 1 || got.Destroy != 1 || got.Unsupported != 2 {
		t.Fatalf("summary = %#v", got)
	}
}

func TestRenderMarkdownUsesEscapedRedactedReviewValues(t *testing.T) {
	review := Review{Steps: []StepReview{{Action: "PUT", URL: "https://api.pingone.com/v1/environments/e/flows/f", Name: "Flow | [prod]", Before: map[string]any{"clientSecret": "secret-value", "name": "old"}, After: map[string]any{"clientSecret": "secret-value", "name": "new"}}}}
	md := RenderMarkdown(review)
	for _, want := range []string{"Native plan steps (classified locally)", "1 change", `Flow \| [prod]`, "[REDACTED]", "old", "new", "clientSecret"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "secret-value") {
		t.Fatalf("secret leaked: %s", md)
	}
}
