package checks

import "sort"

// RequiredVariables must be present (non-empty) in a protected deployment job.
var RequiredVariables = []string{
	"PROMOTION_GITLAB_URL", "PROMOTION_GITLAB_PROJECT_ID",
	"PINGONE_TARGET_ENVIRONMENT_ID", "PINGONE_TARGET_REGION",
	"PINGONE_CLIENT_ID", "PINGONE_CLIENT_SECRET", "GITLAB_READ_API_TOKEN",
}

// OptionalVariables are recognised so near-miss spellings of them are caught.
var OptionalVariables = []string{
	"PINGONE_AUTH_ENVIRONMENT_ID", "PROMOTION_RUNNER_TAG", "PROMOTION_ENVIRONMENT",
	"PROMOTION_TOOL_REF", "PROMOTION_GO_IMAGE", "EVIDENCE_HMAC_KEY",
}

// Variables checks variable presence and near-miss names. present maps each
// defined variable name to whether its value is non-empty; values are never
// passed in, so they cannot be reported.
func Variables(present map[string]bool) Report {
	r := Report{Title: "CI/CD variables"}
	known := map[string]bool{}
	for _, n := range append(append([]string{}, RequiredVariables...), OptionalVariables...) {
		known[n] = true
	}
	for _, n := range RequiredVariables {
		switch nonEmpty, ok := present[n]; {
		case ok && nonEmpty:
			r.Pass(n, "present")
		case ok:
			r.Fail(n, "defined but empty")
		default:
			r.Fail(n, "missing; it must be a protected variable scoped to the deployment environment")
		}
	}
	names := make([]string, 0, len(present))
	for n := range present {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if known[n] {
			continue
		}
		for k := range known {
			if d := distance(n, k); d > 0 && d <= 2 {
				if _, ok := present[k]; !ok {
					r.Fail(n, "looks like a misspelling of %s, which is not defined", k)
				} else {
					r.Warn(n, "looks like a misspelling of %s (both are defined)", k)
				}
				break
			}
		}
	}
	return r
}

// distance is the Levenshtein edit distance, capped early for long names.
func distance(a, b string) int {
	if a == b {
		return 0
	}
	if d := len(a) - len(b); d > 2 || d < -2 {
		return 3
	}
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}
