package promotion

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"unicode"

	"pingone-promote/internal/pingone"
)

type ChangeSummary struct {
	Create      int `json:"create"`
	Change      int `json:"change"`
	Destroy     int `json:"destroy"`
	Unsupported int `json:"unsupported"`
}

// Summarize counts classifications produced while inspecting the native plan against inventory.
// Missing and unknown classifications are unsupported; summary output never guesses from names.
func Summarize(steps []StepReview) ChangeSummary {
	var out ChangeSummary
	for _, step := range steps {
		switch step.Operation {
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

func classifyNativeStep(st pingone.Step, index int, lifecycle *flowVersionLifecycle, target pingone.Inventory, src, dst map[string]pingone.Resource) string {
	if target.APIOrigin == "" || !sameOriginEnvURL(st.URL, target, dst) {
		return "unsupported"
	}
	if lifecycle != nil {
		switch index {
		case 0, 2:
			return "create"
		case 1, 3:
			return "change"
		}
	}
	action := strings.ToUpper(st.Action)
	switch action {
	case "POST":
		if targetResource, ok := dst[st.Target.ID]; ok && validMappedPathURL(st.URL, targetResource, action) {
			return "change"
		}
		if sourceResource, ok := src[st.Source.ID]; ok && validCreatePathURL(st.URL, sourceResource, target, action) {
			return "create"
		}
	case "PUT", "PATCH":
		if targetResource, ok := dst[st.Target.ID]; ok && validMappedPathURL(st.URL, targetResource, action) {
			return "change"
		}
	case "DELETE":
		if targetResource, ok := dst[st.Target.ID]; ok && exactURL(st.URL, targetResource.URL) {
			return "destroy"
		}
	}
	return "unsupported"
}

func sameOriginEnvURL(raw string, target pingone.Inventory, dst map[string]pingone.Resource) bool {
	u, err := parseStrictStepURL(raw)
	return err == nil && sameOriginEnv(u, target, dst)
}

func parseStrictStepURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return nil, fmt.Errorf("invalid native step URL")
	}
	return u, nil
}

func validMappedPathURL(raw string, resource pingone.Resource, action string) bool {
	u, err := parseStrictStepURL(raw)
	return err == nil && validMappedPath(u, resource, action)
}

func validCreatePathURL(raw string, resource pingone.Resource, target pingone.Inventory, action string) bool {
	u, err := parseStrictStepURL(raw)
	return err == nil && validCreatePath(u, resource, target, action)
}

func RenderMarkdown(review Review) string {
	summary := Summarize(review.Steps)
	var b strings.Builder
	fmt.Fprintf(&b, "## Plan change summary\n\n**Native plan steps (classified locally):** %d create, %d change, %d destroy, %d unsupported.\n\n", summary.Create, summary.Change, summary.Destroy, summary.Unsupported)
	b.WriteString("Counts classify PingOne's returned operation list against the selected inventory, destination API origin, HTTP method, and resource path. They are a local estimate; PingOne does not return Terraform-style resource classifications. Before/after values below are included only when present in the redacted review.\n\n")
	b.WriteString("| Operation | Resource | Before | After |\n| --- | --- | --- | --- |\n")
	for _, step := range review.Steps {
		label := step.Operation
		if label == "" {
			label = "unsupported"
		}
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
	var b strings.Builder
	for _, r := range value {
		if unicode.IsControl(r) {
			b.WriteByte(' ')
			continue
		}
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		default:
			if strings.ContainsRune(`!"#$%&'()*+,-./:;=?@[\\]^_`+"`"+`{|}~`, r) {
				b.WriteByte('\\')
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}
