package neutralize_test

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2/hclwrite"

	"pingone-promote/internal/exporter"
	"pingone-promote/internal/exporter/exportertest"
	"pingone-promote/internal/neutralize"
	"pingone-promote/internal/overlay"
	"pingone-promote/internal/tffmt"
)

var update = flag.Bool("update", false, "rewrite golden files")

const devEnv = "11111111-1111-4111-8111-111111111111"

func export(t *testing.T, seeds ...string) *neutralize.Input {
	t.Helper()
	f := exportertest.Build(t, t.TempDir(), exportertest.FixtureDir("dev"))
	r := &exporter.Runner{Config: exporter.Config{Version: f.Version, SHA256: f.SHA256, Path: f.Path}}
	creds := exporter.Credentials{ClientID: "c", ClientSecret: "s", WorkerEnvironmentID: devEnv, RegionCode: "NA"}
	var exp *exporter.Export
	var err error
	if len(seeds) == 0 {
		exp, err = r.Export(context.Background(), creds, exporter.ExportOptions{Include: []string{"*"}, Imports: true})
	} else {
		exp, err = r.ScopedExport(context.Background(), creds, seeds, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return &neutralize.Input{Files: exp.Files, ModuleDir: exp.ModuleDir, ModuleName: exp.ModuleName}
}

// render writes a neutralized result as one text document for goldens.
func render(t *testing.T, r *neutralize.Result) []byte {
	var b bytes.Buffer
	for _, blk := range r.Blocks {
		b.WriteString("# --- " + blk.File + " " + blk.Address + "\n")
		b.WriteString("# refs: " + strings.Join(blk.Refs, ", ") + "\n")
		b.Write(blk.Text)
		b.WriteString("\n\n")
	}
	var names []string
	for n := range r.Variables {
		names = append(names, n)
	}
	sort.Strings(names)
	b.WriteString("# --- variables\n")
	for _, n := range names {
		v := r.Variables[n]
		b.WriteString("# " + string(v.Kind) + " (origin " + v.Origin + ")\n")
		b.Write(neutralize.ChildVariableBlock(v))
		b.WriteString("\n\n")
	}
	src, err := overlay.RenderOverlay(r.SourceValues)
	if err != nil {
		t.Fatal(err)
	}
	b.WriteString("# --- source overlay\n")
	b.Write(src)
	b.WriteString("\n# --- source ids\n")
	var addrs []string
	for a := range r.SourceIDs {
		addrs = append(addrs, a)
	}
	sort.Strings(addrs)
	for _, a := range addrs {
		b.WriteString("# " + a + " = " + r.SourceIDs[a] + "\n")
	}
	b.WriteString("# --- global variable refs\n")
	for _, blk := range r.Blocks {
		if refs := r.GlobalVariableRefs[blk.Address]; len(refs) > 0 {
			b.WriteString("# " + blk.Address + ": " + strings.Join(refs, ", ") + "\n")
		}
	}
	b.WriteString("# --- missing: " + strings.Join(r.MissingDependencies, ", ") + "\n")
	return b.Bytes()
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if *update {
		os.MkdirAll("testdata", 0o755)
		if err := os.WriteFile(p, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s differs; run go test ./internal/neutralize -update\n%s", p, got)
	}
}

func TestNeutralizeFullExport(t *testing.T) {
	in := export(t)
	r, err := neutralize.Neutralize(in, neutralize.Options{SourceEnvironmentID: devEnv,
		SecretNames: map[string]string{"davinci_connection_PingOne_clientSecret": "PingOne_DaVinci_connection_PingOne_clientSecret"}})
	if err != nil {
		t.Fatal(err)
	}
	out := render(t, r)
	golden(t, "full.golden", out)
	for _, blk := range r.Blocks {
		if bytes.Contains(blk.Text, []byte(devEnv)) {
			t.Fatalf("%s still contains the source environment ID", blk.Address)
		}
		if !bytes.Equal(hclwrite.Format(blk.Text), blk.Text) {
			t.Fatalf("%s is not formatted", blk.Address)
		}
	}
	if _, ok := r.SourceValues["davinci_variable_apiKey_value"]; ok {
		t.Fatal("secret values must never be in the source overlay")
	}
	if len(r.Findings) != 0 {
		t.Fatalf("findings: %+v", r.Findings)
	}
}

func TestNeutralizeScopedExport(t *testing.T) {
	in := export(t, "pingone_davinci_flow.pingcli__Flow-0020-B")
	for p := range in.Files {
		if strings.HasSuffix(p, ".auto.tfvars") {
			goto ok
		}
	}
	t.Fatal("export must contain auto.tfvars for values")
ok:
	r, err := neutralize.Neutralize(in, neutralize.Options{SourceEnvironmentID: devEnv,
		ConnectorEnvLocal: map[string][]string{"pingcli__PingOne": {"clientId"}}})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "flow-b.golden", render(t, r))
	if got := r.Closure([]string{"pingone_davinci_flow.pingcli__Flow-0020-B"}); len(got) != 7 {
		t.Fatalf("closure: %v", got)
	}
}

func TestNeutralizedOutputPassesTerraformFmt(t *testing.T) {
	bin := os.Getenv("PINGONE_PROMOTE_TERRAFORM")
	if bin == "" {
		bin = "terraform"
	}
	if _, err := tffmt.ResolveFormatter(bin); err != nil {
		t.Skip("terraform not found; set PINGONE_PROMOTE_TERRAFORM")
	}
	r, err := neutralize.Neutralize(export(t), neutralize.Options{SourceEnvironmentID: devEnv})
	if err != nil {
		t.Fatal(err)
	}
	for _, blk := range r.Blocks {
		if _, err := tffmt.FormatWithTerraform(context.Background(), bin, append(append([]byte(nil), blk.Text...), '\n')); err != nil {
			t.Fatalf("%s: %v", blk.Address, err)
		}
	}
}

const collisionVars = `variable "pingone_environment_id" {
  type = string
}

variable "davinci_variable_counter_value" {
  type        = string
  description = "Value for DaVinci variable counter"
}
`

func collisionInput(sameFlow bool) *neutralize.Input {
	flow2 := "pingcli__Two"
	if sameFlow {
		flow2 = "pingcli__One"
	}
	vars := `
resource "pingone_davinci_variable" "pingcli__counter_flow" {
  environment_id = var.pingone_environment_id
  name           = "counter"
  context        = "flow"
  data_type      = "string"
  mutable        = true
  flow = {
    id = pingone_davinci_flow.pingcli__One.id
  }
  value = {
    string = var.davinci_variable_counter_value
  }
}

resource "pingone_davinci_variable" "pingcli__counter_flow_2" {
  environment_id = var.pingone_environment_id
  name           = "counter"
  context        = "flow"
  data_type      = "string"
  mutable        = true
  flow = {
    id = pingone_davinci_flow.` + flow2 + `.id
  }
  value = {
    string = var.davinci_variable_counter_value
  }
}
`
	flows := `
resource "pingone_davinci_flow" "pingcli__One" {
  environment_id = var.pingone_environment_id
  name           = "One"
  depends_on     = [pingone_davinci_variable.pingcli__counter_flow]
}

resource "pingone_davinci_flow" "pingcli__Two" {
  environment_id = var.pingone_environment_id
  name           = "Two"
  depends_on     = [pingone_davinci_variable.pingcli__counter_flow_2]
}
`
	return &neutralize.Input{Files: map[string][]byte{
		"ping-export-module/variables.tf":                []byte(collisionVars),
		"ping-export-module/pingone_davinci_variable.tf": []byte(vars),
		"ping-export-module/pingone_davinci_flow.tf":     []byte(flows),
		"ping-export-terraform.auto.tfvars":              []byte("davinci_variable_counter_value = \"1\"\n"),
	}}
}

func TestFlowScopedVariableCollisionAddsFlowToLabel(t *testing.T) {
	r, err := neutralize.Neutralize(collisionInput(false), neutralize.Options{SourceEnvironmentID: devEnv})
	if err != nil {
		t.Fatal(err)
	}
	var addrs []string
	for _, b := range r.Blocks {
		addrs = append(addrs, b.Address)
	}
	want := "pingone_davinci_flow.pingcli__One pingone_davinci_flow.pingcli__Two pingone_davinci_variable.pingcli__counter_flow_One pingone_davinci_variable.pingcli__counter_flow_Two"
	if strings.Join(addrs, " ") != want {
		t.Fatalf("addresses: %v", addrs)
	}
	two := r.Block("pingone_davinci_flow.pingcli__Two")
	if !bytes.Contains(two.Text, []byte("pingone_davinci_variable.pingcli__counter_flow_Two")) {
		t.Fatalf("reference not rewritten:\n%s", two.Text)
	}
	if len(r.Renames) != 2 {
		t.Fatalf("renames: %+v", r.Renames)
	}
	for _, n := range []string{"davinci_variable_counter_One_value", "davinci_variable_counter_Two_value"} {
		if r.Variables[n] == nil {
			t.Fatalf("missing variable %s", n)
		}
		if _, ok := r.SourceValues[n]; ok {
			t.Fatalf("%s: terraformer wrote one shared value; it must not be guessed", n)
		}
	}
}

func TestUnresolvableCollisionsFailClosed(t *testing.T) {
	_, err := neutralize.Neutralize(collisionInput(true), neutralize.Options{SourceEnvironmentID: devEnv})
	var ce *neutralize.CollisionError
	if !errors.As(err, &ce) {
		t.Fatalf("want collision error, got %v", err)
	}
	in := &neutralize.Input{Files: map[string][]byte{
		"ping-export-module/variables.tf": []byte(collisionVars),
		"ping-export-module/pingone_davinci_connector_instance.tf": []byte(`
resource "pingone_davinci_connector_instance" "pingcli__X" {
  name = "X"
}

resource "pingone_davinci_connector_instance" "pingcli__X" {
  name = "X"
}
`)}}
	if _, err := neutralize.Neutralize(in, neutralize.Options{SourceEnvironmentID: devEnv}); !errors.As(err, &ce) {
		t.Fatalf("want collision error, got %v", err)
	}
	// Two secrets mapped onto one name is fine; a secret and a value is not.
	in = export(t)
	_, err = neutralize.Neutralize(in, neutralize.Options{SourceEnvironmentID: devEnv,
		SecretNames: map[string]string{"davinci_variable_apiKey_value": "davinci_variable_companyName_value"}})
	if !errors.As(err, &ce) {
		t.Fatalf("want collision error, got %v", err)
	}
}

func TestLabelHelpers(t *testing.T) {
	if got := neutralize.Label("CIAM Plus - Main Flow"); got != "pingcli__CIAM-0020-Plus-0020---0020-Main-0020-Flow" {
		t.Fatal(got)
	}
	if got := neutralize.Label("ciam_x", "company"); got != "pingcli__ciam_x_company" {
		t.Fatal(got)
	}
	for _, name := range []string{"CIAM Plus - Registration, Authentication & Recovery (v2)", "Flow é"} {
		got, ok := neutralize.DecodeLabel(neutralize.Label(name))
		if !ok || got != name {
			t.Fatalf("round trip %q -> %q", name, got)
		}
	}
}

func TestSecretLookingLiteralIsNeverInlined(t *testing.T) {
	in := &neutralize.Input{Files: map[string][]byte{
		"ping-export-module/variables.tf": []byte(collisionVars + `
variable "davinci_connection_X_apiToken" {
  type = string
}
`),
		"ping-export-module/pingone_davinci_connector_instance.tf": []byte(`
resource "pingone_davinci_connector_instance" "pingcli__X" {
  environment_id = var.pingone_environment_id
  name           = "X"
  properties = jsonencode({
    "apiToken" : { "value" : var.davinci_connection_X_apiToken }
  })
}
`),
		"ping-export-terraform.auto.tfvars": []byte("davinci_connection_X_apiToken = \"abc123def\"\n"),
	}}
	r, err := neutralize.Neutralize(in, neutralize.Options{SourceEnvironmentID: devEnv})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(r.Blocks[0].Text, []byte("abc123def")) {
		t.Fatal("a secret-looking literal was inlined into shared definitions")
	}
	if len(r.Findings) != 1 || r.Findings[0].Name != "davinci_connection_X_apiToken" {
		t.Fatalf("findings: %+v", r.Findings)
	}
}
