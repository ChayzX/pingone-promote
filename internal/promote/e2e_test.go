package promote

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"pingone-promote/internal/exporter/exportertest"
)

// TestEndToEnd: fake terraformer export of Flow B -> neutralize -> merge
// into a new config repository with overlays for qa, stage and prod ->
// check passes -> terraform validate and terraform test (mock provider)
// for every environment with -var-file. The terraform steps need
// PINGONE_PROMOTE_TERRAFORM and, offline, PINGONE_PROMOTE_PLUGIN_DIR.
func TestEndToEnd(t *testing.T) {
	exp := neutralized(t, exportertest.FixtureDir("dev"), flowB)
	head := MapRepo{"customer_secrets.yml": []byte(fixtureCustomerSecrets)}
	vals := envValues()
	for env := range vals {
		delete(vals[env], "davinci_application_Web_oauth_redirect_uris") // no application in this request
	}
	res, err := Merge(MergeRequest{Export: exp, Head: head, Selected: selectedFor(flowB), SourceEnv: "dev",
		Rollout: []string{"qa", "stage", "prod"}, EnvValues: vals})
	if err != nil {
		t.Fatal(err)
	}
	if res.Blocked() {
		t.Fatalf("blockers: %+v", res.Blockers)
	}
	repo := apply(head, res)
	dir := t.TempDir()
	writeTree(t, dir, repo)

	bin := os.Getenv("PINGONE_PROMOTE_TERRAFORM")
	rep, err := CheckRoot(context.Background(), CheckOptions{Root: dir, Envs: []string{"dev", "qa", "stage", "prod"},
		CustomerSecrets: repo["customer_secrets.yml"], TerraformFmt: bin != "", TerraformValidate: bin != "", TerraformBin: bin,
		PluginDir: os.Getenv("PINGONE_PROMOTE_PLUGIN_DIR")})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK {
		t.Fatalf("check: %+v", rep.Problems)
	}
	if bin == "" {
		t.Skip("set PINGONE_PROMOTE_TERRAFORM to run terraform validate and test")
	}
	// terraform test with mock_provider, per environment.
	var tests strings.Builder
	tests.WriteString("mock_provider \"pingone\" {}\n\nvariables {\n")
	for _, s := range res.RequiredSecrets {
		tests.WriteString("  " + s + " = \"test-only\"\n")
	}
	tests.WriteString("}\n\nrun \"plan\" {\n  command = plan\n}\n")
	for _, m := range regexp.MustCompile(`(?m)^  to       = (\S+)$`).FindAllStringSubmatch(string(repo["imports.tf"]), -1) {
		tests.WriteString("\noverride_resource {\n  target = " + m[1] + "\n  values = { id = \"mock-imported-id\" }\n}\n")
	}
	writeTree(t, dir, map[string][]byte{"tests/e2e.tftest.hcl": []byte(tests.String())})
	run := func(args ...string) string {
		cmd := exec.Command(bin, args...)
		cmd.Dir = dir
		cmd.Env = append(validateEnv(t.TempDir()), "TF_DATA_DIR="+filepath.Join(dir, ".terraform"))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("terraform %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	initArgs := []string{"init", "-backend=false", "-input=false", "-no-color"}
	if pd := os.Getenv("PINGONE_PROMOTE_PLUGIN_DIR"); pd != "" {
		initArgs = append(initArgs, "-plugin-dir="+pd)
	}
	run(initArgs...)
	run("validate", "-no-color")
	for _, env := range []string{"dev", "qa", "stage", "prod"} {
		if out := run("test", "-no-color", "-var-file="+env+".tfvars"); !strings.Contains(out, "1 passed, 0 failed") {
			t.Fatalf("%s: %s", env, out)
		}
	}
}
