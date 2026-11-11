// Package gitlabci tests the example CI configuration.
package gitlabci

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"pingone-promote/internal/secretscan"
)

type rule struct {
	If     string   `yaml:"if"`
	Exists []string `yaml:"exists"`
	When   string   `yaml:"when"`
}

type job struct {
	Extends       string            `yaml:"extends"`
	Stage         string            `yaml:"stage"`
	Image         string            `yaml:"image"`
	Tags          []string          `yaml:"tags"`
	ResourceGroup string            `yaml:"resource_group"`
	Rules         []rule            `yaml:"rules"`
	Needs         any               `yaml:"needs"`
	Trigger       any               `yaml:"trigger"`
	BeforeScript  []string          `yaml:"before_script"`
	Script        []string          `yaml:"script"`
	Variables     map[string]string `yaml:"variables"`
	Artifacts     struct {
		When  string   `yaml:"when"`
		Paths []string `yaml:"paths"`
	} `yaml:"artifacts"`
}

func load(t *testing.T) (map[string]job, map[string]string, string) {
	t.Helper()
	raw, err := os.ReadFile("pingone-promote.yml")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	jobs := map[string]job{}
	var vars map[string]string
	for k, n := range doc {
		if k == "variables" {
			if err := n.Decode(&vars); err != nil {
				t.Fatal(err)
			}
			continue
		}
		var j job
		if err := n.Decode(&j); err != nil {
			t.Fatalf("%s: %v", k, err)
		}
		jobs[k] = j
	}
	// Resolve extends (one level, as the example uses).
	for k, j := range jobs {
		if j.Extends == "" {
			continue
		}
		base := jobs[j.Extends]
		if j.Image == "" {
			j.Image = base.Image
		}
		if j.Tags == nil {
			j.Tags = base.Tags
		}
		if j.BeforeScript == nil {
			j.BeforeScript = base.BeforeScript
		}
		jobs[k] = j
	}
	return jobs, vars, string(raw)
}

func TestRequestJobIsGuarded(t *testing.T) {
	jobs, vars, _ := load(t)
	j, ok := jobs["pingone-promote:request"]
	if !ok {
		t.Fatal("pingone-promote:request missing")
	}
	if j.Stage != ".pre" || j.ResourceGroup != "pingone-promote-requests" || j.Needs != nil || j.Trigger != nil {
		t.Fatalf("stage/resource_group/needs/trigger: %+v", j)
	}
	if len(j.Rules) != 1 {
		t.Fatalf("want exactly one rule, got %+v", j.Rules)
	}
	r := j.Rules[0]
	// The rule's regex must match exactly the configured request branch prefix.
	prefix := vars["PINGONE_PROMOTE_BRANCH_PREFIX"]
	if prefix == "" || !strings.HasSuffix(prefix, "/") && !strings.HasSuffix(prefix, "-") {
		t.Fatalf("PINGONE_PROMOTE_BRANCH_PREFIX must be set and end with / or -: %q", prefix)
	}
	want := "$CI_COMMIT_BRANCH =~ /^" + strings.ReplaceAll(regexp.QuoteMeta(prefix), "/", `\/`) + "/"
	if !strings.Contains(r.If, `$CI_PIPELINE_SOURCE == "push"`) || !strings.Contains(r.If, want) || !strings.Contains(r.If, "&&") {
		t.Fatalf("rule if: %q (want %q)", r.If, want)
	}
	if len(r.Exists) != 1 || r.Exists[0] != ".pingone-promote/request.json" || r.When == "always" {
		t.Fatalf("rule exists: %+v", r)
	}
	if j.Artifacts.When != "always" || len(j.Artifacts.Paths) != 1 || j.Artifacts.Paths[0] != "result.json" {
		t.Fatalf("artifacts: %+v", j.Artifacts)
	}
	if len(j.Script) != 1 || !strings.HasPrefix(j.Script[0], "pingone-promote request ") {
		t.Fatalf("script: %v", j.Script)
	}
}

func TestCheckJobIsMROnly(t *testing.T) {
	jobs, _, _ := load(t)
	j := jobs["pingone-promote:check"]
	if j.Stage != ".pre" || len(j.Rules) != 1 || j.Rules[0].If != `$CI_PIPELINE_SOURCE == "merge_request_event"` || len(j.Rules[0].Exists) != 0 {
		t.Fatalf("check job: %+v", j)
	}
	if !strings.Contains(strings.Join(j.Script, " "), "pingone-promote check --root .") {
		t.Fatal(j.Script)
	}
}

func TestJobsBuildFromPinnedSources(t *testing.T) {
	jobs, vars, _ := load(t)
	for _, name := range []string{"pingone-promote:request", "pingone-promote:check"} {
		j := jobs[name]
		if j.Image != "$PINGONE_PROMOTE_GO_IMAGE" || len(j.Tags) != 1 || j.Tags[0] != "$PINGONE_PROMOTE_RUNNER_TAG" {
			t.Fatalf("%s: image %q tags %v", name, j.Image, j.Tags)
		}
		bs := strings.Join(j.BeforeScript, "\n")
		for _, want := range []string{
			`test "$(git -C /tmp/pp/tool rev-parse HEAD)" = "$PINGONE_PROMOTE_REF"`,
			"go build -trimpath",
			`test "$(git -C /tmp/pp/terraformer rev-parse HEAD)" = "$TERRAFORMER_COMMIT"`,
			"sha256sum -c -",
		} {
			if !strings.Contains(bs, want) {
				t.Fatalf("%s: before_script lacks %q", name, want)
			}
		}
	}
	if vars["PINGONE_PROMOTE_GO_IMAGE"] != "golang:1.27" || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(vars["PINGONE_PROMOTE_REF"]) ||
		vars["TERRAFORMER_VERSION"] != "v0.6.0" || vars["PINGONE_PROMOTE_OVERLAY_PATTERN"] != "{env}.tfvars" {
		t.Fatalf("variables: %v", vars)
	}
	if jobs[".pingone_promote_build"].Variables["GOFLAGS"] != "-mod=readonly" {
		t.Fatal("module verification must stay on")
	}
}

func TestNoSecretsIDsOrApplies(t *testing.T) {
	_, vars, raw := load(t)
	if f := secretscan.ScanText("pingone-promote.yml", []byte(raw)); len(f) > 0 {
		t.Fatalf("secret-looking content: %+v", f)
	}
	for _, bad := range []string{"glpat-", "terraform apply", "terraform plan", "trigger:", "when: manual"} {
		if strings.Contains(raw, bad) {
			t.Fatalf("example contains %q", bad)
		}
	}
	// The only literal IDs are public pins: the terraformer tag commit, the
	// terraform release checksum and the all-zero placeholder.
	allowed := map[string]bool{vars["TERRAFORMER_COMMIT"]: true, vars["TERRAFORM_SHA256"]: true, strings.Repeat("0", 40): true}
	for _, m := range regexp.MustCompile(`[0-9a-fA-F]{32,}`).FindAllString(raw, -1) {
		if !allowed[m] {
			t.Fatalf("unexpected identifier %s", m)
		}
	}
	if regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-`).MatchString(raw) {
		t.Fatal("environment IDs do not belong in the example")
	}
	for _, v := range []string{"PINGONE_PROMOTE_WORKER_CLIENT_SECRET", "PINGONE_PROMOTE_GITLAB_TOKEN"} {
		if _, ok := vars[v]; ok {
			t.Fatalf("%s must be a masked project variable, not in the file", v)
		}
	}
}
