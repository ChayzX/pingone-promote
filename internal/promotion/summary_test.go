package promotion

import (
	"strings"
	"testing"
)

func TestSummarizeNativeSteps(t *testing.T) {
	review := Review{Steps: []StepReview{
		{Operation: "create", Action: "POST", URL: "https://api.pingone.com/v1/environments/e/flows", Name: "A | flow"},
		{Operation: "change", Action: "PUT", URL: "https://api.pingone.com/v1/environments/e/flows/f", Name: "A | flow"},
		{Operation: "destroy", Action: "DELETE", URL: "https://api.pingone.com/v1/environments/e/flows/old", TargetID: "old", Name: "Old"},
		{Operation: "unsupported", Action: "POST", URL: "https://api.pingone.com/v1/environments/e/flows/f/status", Name: "Status"},
		{Operation: "unsupported", Action: "TRACE", URL: "https://api.pingone.com/v1/environments/e/flows/f", Name: "Unknown"},
	}}
	got := Summarize(review.Steps)
	if got.Create != 1 || got.Change != 1 || got.Destroy != 1 || got.Unsupported != 2 {
		t.Fatalf("summary = %#v", got)
	}
}

func TestRenderMarkdownEscapesActiveMarkupInResourceName(t *testing.T) {
	name := "![click](https://evil.test) [reference][x] <img src=x> *bold* _under_\n| forged row |"
	review := Review{Steps: []StepReview{{Operation: "change", Action: "PUT", URL: "https://api.pingone.com/v1/environments/e/flows/f", Name: name, Before: map[string]any{"clientSecret": "secret-value", "name": "old"}, After: map[string]any{"clientSecret": "secret-value", "name": "new"}}}}
	md := RenderMarkdown(review)
	if strings.Contains(md, "secret-value") || !strings.Contains(md, "REDACTED") {
		t.Fatalf("summary leaked a secret or omitted the redaction marker:\n%s", md)
	}
	if strings.Contains(md, "![click](https://evil.test)") || strings.Contains(md, "[reference][x]") || strings.Contains(md, "<img src=x>") || strings.Contains(md, "*bold*") || strings.Contains(md, "_under_\n") {
		t.Fatalf("resource name retained active Markdown/HTML syntax:\n%s", md)
	}
	if !strings.Contains(md, `\!\[click\]\(https\:\/\/evil\.test\)`) {
		t.Fatalf("resource name wasn't preserved as escaped plain text:\n%s", md)
	}
}
