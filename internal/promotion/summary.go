package promotion

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

type ChangeSummary struct {
	Create      int `json:"create"`
	Change      int `json:"change"`
	Destroy     int `json:"destroy"`
	Unsupported int `json:"unsupported"`
}

// Summarize classifies the native operation list from its HTTP action and resource path.
// Unknown or non-resource operations are reported as unsupported, never guessed.
func Summarize(steps []StepReview) ChangeSummary {
	var out ChangeSummary
	for _, step := range steps {
		switch classifyStep(step) {
		case "create":
			out.Create++
		case "change":
			out.Change++
		case "destroy":
			out.Destroy++
		default:
			out.Unsupported++
		}
	}
	return out
}

func classifyStep(step StepReview) string {
	u, err := url.Parse(step.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || strings.Contains(u.Path, "//") {
		return "unsupported"
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 4 || parts[0] != "v1" || parts[1] != "environments" || parts[2] == "" {
		return "unsupported"
	}
	switch strings.ToUpper(step.Action) {
	case "POST":
		last := parts[len(parts)-1]
		if strings.HasSuffix(last, "s") {
			return "create"
		}
	case "PUT", "PATCH":
		if len(parts) >= 5 {
			return "change"
		}
	case "DELETE":
		if len(parts) >= 5 {
			return "destroy"
		}
	}
	return "unsupported"
}

func RenderMarkdown(review Review) string {
	summary := Summarize(review.Steps)
	var b strings.Builder
	fmt.Fprintf(&b, "## Plan change summary\n\n**Native plan steps (classified locally):** %d create, %d change, %d destroy, %d unsupported.\n\n", summary.Create, summary.Change, summary.Destroy, summary.Unsupported)
	b.WriteString("Counts classify PingOne's returned operation list by HTTP method and resource path. They are a local estimate; PingOne does not return Terraform-style resource classifications. Before/after values below are included only when present in the redacted review.\n\n")
	b.WriteString("| Operation | Resource | Before | After |\n| --- | --- | --- | --- |\n")
	for _, step := range review.Steps {
		label := classifyStep(step)
		name := step.Name
		if name == "" {
			name = step.TargetID
			if name == "" {
				name = step.SourceID
			}
			if name == "" {
				name = "Unnamed resource"
			}
		}
		before := markdownValue(Redact(step.Before))
		after := markdownValue(Redact(step.After))
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", markdownEscape(label), markdownEscape(name), markdownEscape(before), markdownEscape(after))
	}
	return b.String()
}

func markdownValue(value any) string {
	if value == nil {
		return "Not provided"
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "Not provided"
	}
	return string(data)
}

func markdownEscape(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.ReplaceAll(value, "`", "\\`")
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	value = strings.ReplaceAll(value, ">", "&gt;")
	return value
}
