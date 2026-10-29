package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"pingone-promote/internal/checks"
	"pingone-promote/internal/cigate"
	"pingone-promote/internal/pingone"
)

// targetClient builds a PingOne client for the protected runner's target
// region and worker credentials, without needing a request file.
func targetClient(getenv func(string) string) (*pingone.Client, error) {
	target := getenv("PINGONE_TARGET_ENVIRONMENT_ID")
	if target == "" || getenv("PINGONE_CLIENT_ID") == "" {
		return nil, errors.New("PINGONE_TARGET_ENVIRONMENT_ID and PINGONE_CLIENT_ID are required")
	}
	profile := pingone.Profile{Region: getenv("PINGONE_TARGET_REGION"), EnvironmentID: target, AuthEnvironmentID: getenv("PINGONE_AUTH_ENVIRONMENT_ID"), ClientID: getenv("PINGONE_CLIENT_ID"), SecretEnv: "PINGONE_CLIENT_SECRET"}
	return pingone.NewClient(profile, getenv("PINGONE_CLIENT_SECRET"))
}

func defaultGitLabPreflight(getenv func(string) string) func(context.Context) checks.Report {
	return func(ctx context.Context) checks.Report {
		cfg, err := cigate.FromEnv(getenv)
		if err != nil {
			r := checks.Report{Title: "GitLab preflight"}
			r.Fail("job context", "%s", err.Error())
			return r
		}
		return (&cigate.Verifier{Config: cfg}).Preflight(ctx)
	}
}

// runChecks handles the acceptance-check commands; handled is false for any
// other command name.
func runChecks(ctx context.Context, args []string, stdout, stderr io.Writer, deps runnerDeps) (bool, error) {
	switch args[0] {
	case "preflight":
		return true, runPreflight(ctx, args[1:], stdout, stderr, deps)
	case "plan-check":
		return true, runPlanCheck(args[1:], stdout, stderr, deps)
	case "evidence":
		return true, runEvidence(ctx, args[1:], stdout, stderr, deps)
	}
	return false, nil
}

func runPreflight(ctx context.Context, args []string, stdout, stderr io.Writer, deps runnerDeps) error {
	fs := flagSet("preflight", stderr)
	source := fs.String("source", "", "optional source PingOne environment ID to probe as well")
	reportPath := fs.String("report", "", "optional Markdown report output path")
	skipPingOne := fs.Bool("skip-pingone", false, "audit GitLab and variables only")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *reportPath != "" {
		if err := requireAbsent(*reportPath); err != nil {
			return err
		}
	}
	present := map[string]bool{}
	for _, kv := range deps.environ() {
		name, value, _ := strings.Cut(kv, "=")
		present[name] = value != ""
	}
	all := checks.Report{Title: "Preflight"}
	all.Merge(checks.Variables(present))
	gl := deps.gitlabPreflight(ctx)
	all.Merge(gl)
	if !*skipPingOne {
		c, err := deps.targetClient(deps.getenv)
		if err != nil {
			all.Fail("PingOne client", "%s", err.Error())
		} else {
			all.Merge(checks.PingOnePreflight(ctx, c, checks.EnvironmentLabels(*source, deps.getenv("PINGONE_TARGET_ENVIRONMENT_ID"))))
		}
	}
	return finish(all, stdout, *reportPath, "preflight")
}

func runPlanCheck(args []string, stdout, stderr io.Writer, deps runnerDeps) error {
	fs := flagSet("plan-check", stderr)
	artifactPath := fs.String("artifact", "", "plan or rollback artifact JSON")
	digestPath := fs.String("digest-file", "", "optional approved digest file")
	expect := fs.String("expect-selected", "", "comma-separated source resource IDs the plan must select exactly")
	forbid := fs.String("forbid-type", "", "comma-separated resource types (URL path segments) no step may touch")
	secretEnv := fs.String("secret-env", "", "comma-separated extra variable names whose values must not appear")
	summaryPath := fs.String("summary-out", "", "optional Markdown summary output path")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *artifactPath == "" {
		return errors.New("plan-check requires --artifact")
	}
	if *summaryPath != "" {
		if err := requireAbsent(*summaryPath); err != nil {
			return err
		}
	}
	a, err := readArtifact(*artifactPath)
	if err != nil {
		return err
	}
	raw, err := readSmallFile(*artifactPath, maxArtifactBytes)
	if err != nil {
		return err
	}
	opts := checks.PlanOptions{ExpectSelected: list(*expect), ForbidTypes: list(*forbid)}
	if *digestPath != "" {
		d, err := readSmallFile(*digestPath, 1024)
		if err != nil {
			return fmt.Errorf("could not read digest file: %w", err)
		}
		opts.DigestFileContent = string(d)
	}
	for _, name := range append([]string{"PINGONE_CLIENT_SECRET", "GITLAB_READ_API_TOKEN", "CI_JOB_TOKEN", "EVIDENCE_HMAC_KEY"}, list(*secretEnv)...) {
		if v := deps.getenv(name); v != "" {
			opts.Secrets = append(opts.Secrets, v)
		}
	}
	r := checks.CheckPlan(a, raw, opts)
	if *summaryPath != "" {
		if err := writePrivateBytes(*summaryPath, []byte(checks.PlanSummary(a)+"\n"+r.Markdown())); err != nil {
			return err
		}
	}
	return finish(r, stdout, "", "plan check")
}

func runEvidence(ctx context.Context, args []string, stdout, stderr io.Writer, deps runnerDeps) error {
	if len(args) == 0 {
		return errors.New("usage: pingone-runner evidence <capture|compare> [flags]")
	}
	switch args[0] {
	case "capture":
		fs := flagSet("evidence capture", stderr)
		env := fs.String("env", "", "PingOne environment ID to capture")
		out := fs.String("out", "", "evidence JSON output path")
		if err := parseFlags(fs, args[1:]); err != nil {
			return err
		}
		if *env == "" || *out == "" {
			return errors.New("evidence capture requires --env and --out")
		}
		if err := requireAbsent(*out); err != nil {
			return err
		}
		key := deps.getenv("EVIDENCE_HMAC_KEY")
		if key == "" {
			key = deps.getenv("PINGONE_CLIENT_SECRET")
		}
		if len(key) < checks.MinKeyLength {
			return fmt.Errorf("set EVIDENCE_HMAC_KEY (at least %d characters) or PINGONE_CLIENT_SECRET to key evidence fingerprints", checks.MinKeyLength)
		}
		c, err := deps.targetClient(deps.getenv)
		if err != nil {
			return err
		}
		inv, err := c.Inventory(ctx, *env)
		if err != nil {
			return err
		}
		e, err := checks.NewEvidence(inv, []byte(key), time.Now())
		if err != nil {
			return err
		}
		if err = writePrivateJSON(*out, e); err != nil {
			return err
		}
		_, err = fmt.Fprintf(stdout, "captured %d resource fingerprint(s) for %s (snapshot %s, key %s)\n", len(e.Resources), e.EnvironmentID, e.SnapshotID, e.KeyID)
		return err
	case "compare":
		fs := flagSet("evidence compare", stderr)
		before := fs.String("before", "", "earlier evidence JSON")
		after := fs.String("after", "", "later evidence JSON")
		expect := fs.String("expect-changed", "", "comma-separated resource IDs that must change")
		allowAdded := fs.String("allow-added-type", "", "comma-separated resource types that may gain resources")
		reportPath := fs.String("report", "", "optional Markdown report output path")
		if err := parseFlags(fs, args[1:]); err != nil {
			return err
		}
		if *before == "" || *after == "" {
			return errors.New("evidence compare requires --before and --after")
		}
		if *reportPath != "" {
			if err := requireAbsent(*reportPath); err != nil {
				return err
			}
		}
		b, err := readEvidence(*before)
		if err != nil {
			return err
		}
		a, err := readEvidence(*after)
		if err != nil {
			return err
		}
		r := checks.Compare(b, a, checks.CompareOptions{ExpectChanged: list(*expect), AllowAddedTypes: list(*allowAdded)})
		return finish(r, stdout, *reportPath, "evidence compare")
	}
	return fmt.Errorf("unknown evidence command %q", args[0])
}

func readEvidence(path string) (checks.Evidence, error) {
	raw, err := readSmallFile(path, maxArtifactBytes)
	if err != nil {
		return checks.Evidence{}, fmt.Errorf("could not read evidence %s: %w", path, err)
	}
	return checks.DecodeEvidence(raw)
}

func finish(r checks.Report, stdout io.Writer, reportPath, what string) error {
	if err := r.WriteText(stdout); err != nil {
		return err
	}
	if reportPath != "" {
		if err := writePrivateBytes(reportPath, []byte(r.Markdown())); err != nil {
			return err
		}
	}
	if r.Failed() {
		_, _, f := r.Counts()
		return fmt.Errorf("%s failed: %d check(s) failed", what, f)
	}
	return nil
}

func list(s string) []string {
	out := []string{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
