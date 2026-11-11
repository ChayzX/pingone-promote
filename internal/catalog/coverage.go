package catalog

import (
	"fmt"
	"strings"
)

// Tested names, per type, the fixture test that proves its single-item
// export, its upstream export and its exclusion from a whole-environment
// export. internal/multiexport TestTestedTypesAreExercised checks every
// entry. Types without an entry are covered by the generic mapper only.
var Tested = map[string]string{}

// CoverageMarkdown renders docs/coverage.md.
func CoverageMarkdown() string {
	var b strings.Builder
	b.WriteString("# Coverage matrix\n\n")
	b.WriteString("Generated from `internal/catalog` by `PINGONE_PROMOTE_WRITE_COVERAGE=1 go test ./internal/catalog`; do not edit by hand.\n\n")
	fmt.Fprintf(&b, "Every resource of the pinned `pingidentity/pingone` provider %s is listed. ", ProviderVersion)
	b.WriteString("**Exporter**: `pingcli-terraformer` (pinned v0.6.0, DaVinci) or `pingone-api` (the native read-only PingOne exporter, ADR 0019). ")
	b.WriteString("**Environment-specific values** become per-environment variables in `<env>.tfvars`; the environment ID, certificate and key IDs, and IDs of resources that are not exported are always per-environment. ")
	b.WriteString("**Built-in** objects exist in every environment and are adopted with `import_ids`, never created. ")
	b.WriteString("**Live** is `no` until the live acceptance case in `docs/acceptance/core-export.md` has passed on a real tenant: API paths and field names are assumptions until then.\n\n")
	counts := map[string]int{}
	for _, d := range All() {
		counts[d.Status]++
	}
	fmt.Fprintf(&b, "Totals: %d covered, %d covered with documented limits, %d not possible (%d resources).\n\n", counts[Covered], counts[Limited], counts[NotPossible], len(All()))
	fam := ""
	for _, d := range All() {
		if d.Family != fam {
			fam = d.Family
			fmt.Fprintf(&b, "\n## %s\n\n", strings.ReplaceAll(fam, "_", " "))
			b.WriteString("| Resource type | Status | Exporter | Environment-specific values / built-in | Tested | Live | Notes |\n| --- | --- | --- | --- | --- | --- | --- |\n")
		}
		exp := d.Exporter
		if exp == "" {
			exp = "-"
		}
		var rules []string
		if len(d.EnvLocal) > 0 {
			rules = append(rules, "per environment: `"+strings.Join(d.EnvLocal, "`, `")+"`")
		}
		if d.Builtin != "" {
			rules = append(rules, "built-in adopted when `"+d.Builtin+"`")
		}
		if d.Singleton {
			rules = append(rules, "one per environment, imported by environment ID")
		}
		if d.Owned {
			rules = append(rules, "exported with its "+DisplayName(d.Parent))
		}
		tested := "-"
		if t, ok := Tested[d.Type]; ok {
			tested = "`" + t + "`"
		}
		live := "no"
		if d.Verified {
			live = "yes (DaVinci, earlier live runs)"
		}
		r := strings.Join(rules, "; ")
		if r == "" {
			r = "-"
		}
		note := d.Reason
		if note == "" {
			note = "-"
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s | %s | %s |\n", d.Type, d.Status, exp, r, tested, live, note)
	}
	return b.String()
}
