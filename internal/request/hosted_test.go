package request

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"pingone-promote/internal/exporter"
	"pingone-promote/internal/exporter/exportertest"
	"pingone-promote/internal/gitlab"
)

func copyFixture(t *testing.T, source string) string {
	t.Helper()
	dest := filepath.Join(t.TempDir(), "fixture")
	err := filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dest
}

func TestGenericSelectionContract(t *testing.T) {
	for _, s := range []string{
		`"resources":[{"type":"pingone_notification_template_content","name":"recovery_code_template"}]`,
		`"resources":[{"address":"pingone_population.pingcli__Default"}],"includeUpstream":false`,
	} {
		raw := []byte(`{"requestVersion":1,"requestId":"request-fixture-001","source":"dev",` + s + `}`)
		if _, err := Parse(raw); err != nil {
			t.Fatalf("generic selector rejected: %v", err)
		}
	}
}

func TestGenericSelectionRejectsUnsafeTypes(t *testing.T) {
	for _, typ := range []string{"pingone_user", "pingone_user_role_assignment", "pingone_audit_event", "aws_instance"} {
		raw := []byte(`{"requestVersion":1,"requestId":"request-fixture-001","source":"dev","resources":[{"type":"` + typ + `","name":"fixture"}]}`)
		if _, err := Parse(raw); err == nil {
			t.Fatalf("unsafe type %s accepted", typ)
		}
	}
}

func TestHostedPreviewHasNoRequestFile(t *testing.T) {
	req := requestJSON(func(m map[string]any) { m["dryRun"] = true })
	h := newHarness(t, req)
	h.env.NoRequestFile = true
	res := h.run(req)
	if res.Error != nil {
		t.Fatalf("preview failed: %v", res.Error)
	}
	for _, f := range res.Files {
		if f.Path == gitlab.RequestFile {
			t.Fatal("hosted preview reports a CI request file mutation")
		}
	}
}

func TestHostedPreviewAndCommitDoNotLeakOpaqueJSONCredential(t *testing.T) {
	const credential = "SYNTHETIC-opaque-auth-value-123456"
	fixture := copyFixture(t, exportertest.FixtureDir("dev"))
	appendFixture := func(rel, extra string) {
		t.Helper()
		path := filepath.Join(fixture, rel)
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(extra); err != nil {
			f.Close()
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	appendFixture("ping-export-module/pingone_davinci_variable.tf", `
resource "pingone_davinci_variable" "pingcli__config_company" {
  environment_id = var.pingone_environment_id
  name           = "config"
  context        = "company"
  data_type      = "string"
  mutable        = true
  value          = { string = var.davinci_variable_config_value }
}
`)
	appendFixture("ping-export-module/variables.tf", `
variable "davinci_variable_config_value" {
  type = string
}
`)
	appendFixture("values.tfvars", "\ndavinci_variable_config_value = "+strconv.Quote(`{"auth":"`+credential+`","region":"us"}`)+"\n")

	previewReq := requestJSON(func(m map[string]any) { m["scope"] = "all"; m["confirmAll"] = true; m["dryRun"] = true })
	h := newHarness(t, previewReq)
	fake := exportertest.Build(t, t.TempDir(), fixture)
	h.fake = fake
	h.env.Exporter = &exporter.Runner{Config: exporter.Config{Version: fake.Version, SHA256: fake.SHA256, Path: fake.Path}}
	preview := h.run(previewReq)
	serialized, err := json.Marshal(preview)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serialized), credential) {
		t.Fatal("hosted preview exposed the synthetic credential")
	}
	if preview.Error != nil || preview.Status != StatusPreviewed {
		t.Fatalf("hosted preview failed: %s %+v %+v", preview.Status, preview.Error, preview.Blockers)
	}

	submitReq := requestJSON(func(m map[string]any) { m["scope"] = "all"; m["confirmAll"] = true })
	submitted := h.run(submitReq)
	if submitted.Error != nil || submitted.Status != StatusSubmitted {
		t.Fatalf("hosted fake GitLab submit failed: %s %+v %+v", submitted.Status, submitted.Error, submitted.Blockers)
	}
	for path, content := range h.gl.Files(h.branch) {
		if strings.Contains(content, credential) {
			t.Errorf("hosted fake GitLab file %s contains the synthetic credential", path)
		}
	}
}

func TestGenericAddressRunsThroughEngine(t *testing.T) {
	req := requestJSON(func(m map[string]any) {
		delete(m, "flows")
		m["resources"] = []map[string]string{{"address": "pingone_davinci_flow.pingcli__Flow-0020-B"}}
		m["dryRun"] = true
	})
	h := newHarness(t, req)
	res := h.run(req)
	if res.Error != nil || res.Status != StatusPreviewed {
		t.Fatalf("generic engine selection failed: %s %v", res.Status, res.Error)
	}
	if len(res.Selected) != 1 || !strings.Contains(res.Selected[0].Address, "Flow-0020-B") {
		t.Fatal("generic item did not reach selected merge")
	}
}

func TestGenericAddressIncludeExcludeConflict(t *testing.T) {
	raw := []byte(`{"requestVersion":1,"requestId":"request-fixture-001","source":"dev","resources":[{"address":"pingone_population.pingcli__Default"}],"exclude":[{"address":"pingone_population.pingcli__Default"}]}`)
	if _, err := Parse(raw); err == nil {
		t.Fatal("same address included and excluded")
	}
}

func TestSelectedWithoutUpstreamDoesNotAddDependencies(t *testing.T) {
	// Without upstream, the subflow the selected flow calls is not
	// exported: the shared definitions refer to it by an ID variable, so
	// every rollout environment must supply that ID (without it the engine
	// correctly blocks with missing_value).
	const idVar = "pingone_davinci_flow_pingcli__Flow_0020_B_0020_Subflow_id"
	vals := map[string]any{}
	for env, id := range map[string]string{"qa": "f2000000000000000000000000000003", "stage": "f3000000000000000000000000000003", "prod": "f4000000000000000000000000000003"} {
		vals[env] = map[string]any{idVar: id}
	}
	raw := requestJSON(func(m map[string]any) { m["includeUpstream"] = false; m["dryRun"] = true; m["envValues"] = vals })
	h := newHarness(t, raw)
	res := h.run(raw)
	if res.Error != nil || res.Status != StatusPreviewed {
		t.Fatalf("no-upstream export failed: %s %+v %+v", res.Status, res.Error, res.Blockers)
	}
	for _, change := range res.Changes {
		if change.Role == "dependency" && (change.Action == "added" || change.Action == "updated") {
			t.Fatalf("dependency was exported: %s", change.Address)
		}
	}
	found := false
	for _, change := range res.Changes {
		if change.Address == "pingone_davinci_flow.pingcli__Flow-0020-B" && change.Role == "selected" {
			found = true
		}
	}
	if !found {
		t.Fatal("selected flow missing")
	}
	if strings.Contains(res.Diff, "+resource \"pingone_davinci_flow\" \"pingcli__Flow-0020-B-0020-Subflow\"") {
		t.Fatal("the upstream subflow was added although includeUpstream is false")
	}
}
