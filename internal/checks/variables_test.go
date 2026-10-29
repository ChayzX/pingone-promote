package checks

import (
	"strings"
	"testing"
)

func allRequired() map[string]bool {
	m := map[string]bool{}
	for _, n := range RequiredVariables {
		m[n] = true
	}
	return m
}

func TestVariablesAllPresent(t *testing.T) {
	m := allRequired()
	m["CI_JOB_ID"] = true
	if r := Variables(m); r.Failed() {
		t.Fatalf("complete set failed:\n%s", r.Markdown())
	}
}

// Regression for the trial run: two secrets were saved one letter short.
func TestVariablesCatchesTruncatedNames(t *testing.T) {
	m := allRequired()
	delete(m, "PINGONE_CLIENT_SECRET")
	delete(m, "GITLAB_READ_API_TOKEN")
	m["PINGONE_CLIENT_SECRE"] = true
	m["GITLAB_READ_API_TOKE"] = true
	r := Variables(m)
	md := r.Markdown()
	for _, want := range []string{"PINGONE_CLIENT_SECRE | looks like a misspelling of PINGONE_CLIENT_SECRET", "GITLAB_READ_API_TOKE | looks like a misspelling of GITLAB_READ_API_TOKEN", "PINGONE_CLIENT_SECRET | missing"} {
		if !strings.Contains(md, want) {
			t.Fatalf("missing %q:\n%s", want, md)
		}
	}
}

func TestVariablesEmptyAndDuplicateNearMiss(t *testing.T) {
	m := allRequired()
	m["PINGONE_CLIENT_ID"] = false
	m["PINGONE_CLIENT_IDD"] = true
	r := Variables(m)
	if status(r, "PINGONE_CLIENT_ID") != Fail || status(r, "PINGONE_CLIENT_IDD") != Warn {
		t.Fatalf("empty/near-miss handling:\n%s", r.Markdown())
	}
}

func TestDistance(t *testing.T) {
	for _, c := range []struct {
		a, b string
		d    int
	}{{"ABC", "ABC", 0}, {"ABC", "AB", 1}, {"ABC", "AXC", 1}, {"ABCDEF", "AB", 3}, {"KITTEN", "SITTING", 3}} {
		if got := distance(c.a, c.b); got != c.d && !(c.d >= 3 && got >= 3) {
			t.Fatalf("distance(%s,%s)=%d want %d", c.a, c.b, got, c.d)
		}
	}
}
