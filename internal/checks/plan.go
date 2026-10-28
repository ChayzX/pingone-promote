package checks

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"pingone-promote/internal/gitops"
)

// PlanOptions describes what a reviewer intended the plan to contain.
type PlanOptions struct {
	// DigestFileContent is the approved digest file's content; empty skips the check.
	DigestFileContent string
	// ExpectSelected, when non-empty, must equal the request's selected IDs exactly.
	ExpectSelected []string
	// ForbidTypes are resource collection names (URL path segments such as
	// "templates" or "notificationsTemplates") that no step may touch.
	ForbidTypes []string
	// Secrets are values that must not appear anywhere in the artifact bytes.
	Secrets []string
}

// CheckPlan asserts a plan or rollback artifact matches reviewer intent. The
// artifact must already have passed gitops validation; raw is its file bytes.
func CheckPlan(a gitops.Artifact, raw []byte, o PlanOptions) Report {
	r := Report{Title: fmt.Sprintf("Plan check (%s %s)", a.Kind, a.NativeID)}
	r.Pass("artifact", "valid %s artifact for target %s, %d step(s)", a.Kind, a.Request.Target.ID, len(a.Review.Steps))

	if d := strings.TrimSpace(o.DigestFileContent); d != "" {
		if d == a.Review.Digest {
			r.Pass("digest", "approved digest file matches review digest %s", short(d))
		} else {
			r.Fail("digest", "digest file %s does not match review digest %s", short(d), short(a.Review.Digest))
		}
	}

	if len(a.Review.Issues) == 0 {
		r.Pass("review issues", "native plan review reported no issues")
	} else {
		r.Fail("review issues", "%d issue(s): %s", len(a.Review.Issues), strings.Join(a.Review.Issues, "; "))
	}
	if len(a.Review.Steps) == 0 {
		r.Fail("steps", "plan has no steps")
	}

	if len(o.ExpectSelected) > 0 {
		want, got := sorted(o.ExpectSelected), sorted(a.Request.Selected)
		if strings.Join(want, ",") == strings.Join(got, ",") {
			r.Pass("selection", "request selects exactly the %d expected resource(s)", len(want))
		} else {
			r.Fail("selection", "request selects [%s], expected [%s]", strings.Join(got, ", "), strings.Join(want, ", "))
		}
	}

	selected := map[string]bool{}
	for _, id := range a.Request.Selected {
		selected[id] = true
	}
	forbidden := map[string]bool{}
	for _, t := range o.ForbidTypes {
		if t = strings.TrimSpace(t); t != "" {
			forbidden[strings.ToLower(t)] = true
		}
	}
	outside, unselected, forbiddenHits := 0, 0, 0
	for i, st := range a.Review.Steps {
		env, segments, ok := stepPath(st.URL)
		if !ok || env != a.Request.Target.ID {
			outside++
			r.Fail("target scope", "step %d (%s) is outside target environment %s", i, st.URL, a.Request.Target.ID)
		}
		if a.Kind == "promotion" && st.SourceID != "" && !selected[st.SourceID] {
			unselected++
			r.Fail("selection scope", "step %d comes from unselected source resource %s", i, st.SourceID)
		}
		for _, seg := range segments {
			if forbidden[strings.ToLower(seg)] {
				forbiddenHits++
				r.Fail("forbidden type", "step %d touches forbidden resource type %q (%s)", i, seg, st.URL)
				break
			}
		}
	}
	if outside == 0 && len(a.Review.Steps) > 0 {
		r.Pass("target scope", "all %d step(s) stay inside target environment %s", len(a.Review.Steps), a.Request.Target.ID)
	}
	if unselected == 0 && a.Kind == "promotion" {
		r.Pass("selection scope", "every step comes from a selected source resource")
	}
	if len(forbidden) > 0 && forbiddenHits == 0 {
		r.Pass("forbidden type", "no step touches %s", strings.Join(sorted(keys(forbidden)), ", "))
	}

	if a.Kind == "rollback" {
		if a.OriginalNativeID != "" && a.OriginalNativeID != a.NativeID {
			r.Pass("rollback link", "rollback %s is linked to original promotion %s", a.NativeID, a.OriginalNativeID)
		} else {
			r.Fail("rollback link", "rollback is not linked to a distinct original promotion")
		}
	}

	leaked := 0
	text := string(raw)
	for _, s := range o.Secrets {
		if len(s) >= 6 && strings.Contains(text, s) {
			leaked++
		}
	}
	switch {
	case leaked > 0:
		r.Fail("secrets", "%d known secret value(s) appear in the artifact", leaked)
	case countUsable(o.Secrets) > 0:
		r.Pass("secrets", "none of %d known secret value(s) appear in the artifact", countUsable(o.Secrets))
	default:
		r.Warn("secrets", "no secret values supplied to scan for")
	}
	return r
}

// PlanSummary renders a reviewer-facing summary of the plan's steps.
func PlanSummary(a gitops.Artifact) string {
	var b strings.Builder
	kind := "Promotion"
	if a.Kind == "rollback" {
		kind = "Rollback"
	}
	fmt.Fprintf(&b, "# %s review: %s\n\n", kind, a.NativeID)
	fmt.Fprintf(&b, "- Source: `%s` snapshot `%s`\n- Target: `%s` (%s) snapshot `%s`\n- Digest: `%s`\n- GitLab pipeline: %s, commit `%s`\n",
		a.Request.Source.ID, a.Request.Source.SnapshotID, a.Request.Target.ID, a.Request.Target.Region, a.TargetSnapshotID, a.Review.Digest, a.Origin.PipelineID, a.Origin.CommitSHA)
	if a.OriginalNativeID != "" {
		fmt.Fprintf(&b, "- Rolls back promotion: `%s`\n", a.OriginalNativeID)
	}
	fmt.Fprintf(&b, "- Selected: %s\n", codeList(a.Request.Selected))
	if len(a.Request.Mappings) > 0 {
		pairs := []string{}
		for k, v := range a.Request.Mappings {
			pairs = append(pairs, fmt.Sprintf("`%s` → `%s`", k, v))
		}
		sort.Strings(pairs)
		fmt.Fprintf(&b, "- Mappings: %s\n", strings.Join(pairs, ", "))
	}
	b.WriteString("\n| # | Action | Resource path | Source | Target | Name | Changed fields |\n| --- | --- | --- | --- | --- | --- | --- |\n")
	for i, st := range a.Review.Steps {
		_, segs, _ := stepPath(st.URL)
		changes := strings.Join(st.Changes, ", ")
		if len(changes) > 120 {
			changes = changes[:120] + "…"
		}
		fmt.Fprintf(&b, "| %d | %s | %s | %s | %s | %s | %s |\n", i, cell(st.Action), cell(strings.Join(segs, "/")), cell(st.SourceID), cell(st.TargetID), cell(st.Name), cell(changes))
	}
	if len(a.Review.Issues) > 0 {
		b.WriteString("\n**Issues**\n\n")
		for _, is := range a.Review.Issues {
			fmt.Fprintf(&b, "- %s\n", is)
		}
	}
	return b.String()
}

// stepPath returns the environment ID and the path segments after it.
func stepPath(raw string) (string, []string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", nil, false
	}
	p := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(p) < 3 || p[0] != "v1" || p[1] != "environments" {
		return "", nil, false
	}
	for _, s := range p {
		if s == "" || s == "." || s == ".." {
			return "", nil, false
		}
	}
	return p[2], p[3:], true
}

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
func countUsable(ss []string) int {
	n := 0
	for _, s := range ss {
		if len(s) >= 6 {
			n++
		}
	}
	return n
}
func short(s string) string {
	if len(s) > 12 {
		return s[:12] + "…"
	}
	return s
}
func codeList(ids []string) string {
	if len(ids) == 0 {
		return "(none)"
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = "`" + id + "`"
	}
	return strings.Join(parts, ", ")
}
