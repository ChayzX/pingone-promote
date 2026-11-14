package promote

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"pingone-promote/internal/exporter/exportertest"
	"pingone-promote/internal/neutralize"
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
	endToEnd(t, repo, res.RequiredSecrets)
}

// endToEnd checks a merged root and, when terraform is available, runs
// validate and a mock-provider plan with -var-file for every environment.
func endToEnd(t *testing.T, repo MapRepo, secrets []string) {
	t.Helper()
	endToEndPlans(t, repo, secrets, []string{"dev", "qa", "stage", "prod"})
}

// endToEndPlans is endToEnd with the environments whose mock plan runs.
func endToEndPlans(t *testing.T, repo MapRepo, secrets []string, planEnvs []string) {
	t.Helper()
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
	for _, s := range secrets {
		tests.WriteString("  " + s + " = \"test-only\"\n")
	}
	tests.WriteString("}\n\nrun \"plan\" {\n  command = plan\n}\n")
	// Every resource gets a fixed, valid ID: imports cannot run against a
	// mock provider, and PingOne resources validate references as UUIDs.
	n := 0
	for p, src := range repo {
		if !strings.HasPrefix(p, "definitions/") || !strings.HasSuffix(p, ".tf") {
			continue
		}
		for _, m := range regexp.MustCompile(`(?m)^resource "([a-z0-9_]+)" "([A-Za-z0-9_-]+)"`).FindAllStringSubmatch(string(src), -1) {
			n++
			id := fmt.Sprintf("00000000-0000-4000-8000-%012d", n)
			if strings.HasPrefix(m[1], "pingone_davinci_") {
				id = fmt.Sprintf("%032d", n) // DaVinci IDs are 32 hex digits
			}
			values := "id = \"" + id + "\""
			if m[1] == "pingone_branding_theme_default" {
				// A mock import leaves this computed attribute unknown.
				values += ", branding_theme_id = \"" + id + "\""
			}
			tests.WriteString("\noverride_resource {\n  target = module.davinci." + m[1] + "." + m[2] + "\n  values = { " + values + " }\n}\n")
		}
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
	for _, env := range planEnvs {
		if out := run("test", "-no-color", "-var-file="+env+".tfvars"); !strings.Contains(out, "1 passed, 0 failed") {
			t.Fatalf("%s: %s", env, out)
		}
	}
}

// TestEndToEndExcludedDependency: Flow B is requested with the PingOne
// connection excluded and not in Git. The shared definitions refer to it by
// the root variable <type>_<label>_id; every environment's -var-file sets the
// ID that exists there, and each plans.
func TestEndToEndExcludedDependency(t *testing.T) {
	exp := neutralizedExcluding(t, exportertest.FixtureDir("dev"), []neutralize.Excluded{{Address: pingOneAddr}}, flowB)
	head := MapRepo{"customer_secrets.yml": []byte(fixtureCustomerSecrets)}
	res, err := Merge(MergeRequest{Export: exp, Head: head, Selected: selectedFor(flowB), SourceEnv: "dev", Rollout: rollout,
		EnvValues: idVarValues(""), Choices: map[string]Choice{pingOneAddr: ChoiceExclude}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Blocked() {
		t.Fatalf("blockers: %+v", res.Blockers)
	}
	repo := apply(head, res)
	for env, id := range map[string]string{"dev": "c0000000000000000000000000000002", "qa": "c2000000000000000000000000000002"} {
		if !strings.Contains(string(repo[env+".tfvars"]), pingOneIDVar+" = \""+id+"\"") {
			t.Fatalf("%s.tfvars lacks the ID:\n%s", env, repo[env+".tfvars"])
		}
	}
	if strings.Contains(string(repo["definitions/pingone_davinci_connector_instance.tf"]), "PingOne") {
		t.Fatal("the excluded connection must not be written")
	}
	endToEnd(t, repo, res.RequiredSecrets)
}
