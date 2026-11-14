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
)

// A DaVinci variable whose JSON holds a credential in a shape other than
// {"key": "secret"} (a header pair, a wrapped value, a URL with a
// password) is kept out of the preview and out of Git: it is a secret.
func TestSourceJSONCredentialShapesStayOutOfGit(t *testing.T) {
	const credential = "SYNTHETIC-opaque-value-123456"
	for _, value := range []string{
		`{"headers":[{"name":"Authorization","value":"` + credential + `"}]}`,
		`{"password":{"current":"` + credential + `"}}`,
		`{"url":"https://svc:` + credential + `@host.example.com/x"}`,
		`{"auth":{"user":"svc","pass":"` + credential + `"}}`,
	} {
		t.Run(value, func(t *testing.T) {
			fixture := copyFixture(t, exportertest.FixtureDir("dev"))
			appendTo := func(rel, extra string) {
				t.Helper()
				f, err := os.OpenFile(filepath.Join(fixture, rel), os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				if _, err := f.WriteString(extra); err != nil {
					t.Fatal(err)
				}
			}
			appendTo("ping-export-module/pingone_davinci_variable.tf", `
resource "pingone_davinci_variable" "pingcli__config_company" {
  environment_id = var.pingone_environment_id
  name           = "config"
  context        = "company"
  data_type      = "string"
  mutable        = true
  value          = { string = var.davinci_variable_config_value }
}
`)
			appendTo("ping-export-module/variables.tf", "\nvariable \"davinci_variable_config_value\" {\n  type = string\n}\n")
			appendTo("values.tfvars", "\ndavinci_variable_config_value = "+strconv.Quote(value)+"\n")

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
				t.Fatal("the preview holds the credential")
			}
			if preview.Error != nil || preview.Status != StatusPreviewed {
				t.Fatalf("preview: %s %+v %+v", preview.Status, preview.Error, preview.Blockers)
			}
			submitted := h.run(requestJSON(func(m map[string]any) { m["scope"] = "all"; m["confirmAll"] = true }))
			if submitted.Error != nil || submitted.Status != StatusSubmitted {
				t.Fatalf("submit: %s %+v %+v", submitted.Status, submitted.Error, submitted.Blockers)
			}
			files := h.gl.Files(h.branch)
			for path, content := range files {
				if strings.Contains(content, credential) {
					t.Errorf("%s holds the credential", path)
				}
			}
			if !strings.Contains(files["customer_secrets.yml"], "davinci_variable_config_value") {
				t.Error("the variable is not a secret in customer_secrets.yml")
			}
		})
	}
}
