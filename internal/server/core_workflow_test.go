package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pingone-promote/internal/exporter"
	"pingone-promote/internal/exporter/exportertest"
	"pingone-promote/internal/gitlab"
)

type hostedCoreInventory []exporter.SourceResource

func (i hostedCoreInventory) List(context.Context, exporter.Credentials) ([]exporter.SourceResource, error) {
	return i, nil
}

type hostedCoreReader struct{ schema []byte }

func (r hostedCoreReader) Read(_ context.Context, _ exporter.Credentials, resources []exporter.SourceResource) (*exporter.Export, error) {
	resource := resources[0]
	state, err := json.Marshal(map[string]any{"values": map[string]any{"root_module": map[string]any{"resources": []any{map[string]any{
		"address": resource.String(), "mode": "managed", "type": resource.Type,
		"values": map[string]any{
			"environment_id": "11111111-1111-4111-8111-111111111111", "locale": "en-US", "template_name": "general", "id": resource.ID,
			"email": map[string]any{"body": "Your account is ready.", "subject": "Account ready", "from": map[string]any{"address": "notify@dev.example"}},
		},
	}}}}})
	if err != nil {
		return nil, err
	}
	out, err := exporter.RenderProviderState(r.schema, state, map[string]string{resource.String(): resource.ImportID})
	if err != nil {
		return nil, err
	}
	out.EnvironmentID = "11111111-1111-4111-8111-111111111111"
	return out, nil
}

func TestHostedCoreNotificationContentWorkflow(t *testing.T) {
	for _, mode := range []string{"mr", "branch"} {
		t.Run(mode, func(t *testing.T) { runHostedCoreNotificationContentWorkflow(t, mode) })
	}
}

func runHostedCoreNotificationContentWorkflow(t *testing.T, mode string) {
	fake := exportertest.Build(t, t.TempDir(), exportertest.FixtureDir("dev"))
	h := newHTTPHarness(t, fake)
	address := "pingone_notification_template_content.general_en_us"
	schema, err := os.ReadFile(filepath.Join("..", "exporter", "exportertest", "fixtures", "core", "notification-schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	resource := exporter.SourceResource{
		Resource: exporter.Resource{Address: exporter.Address{Type: "pingone_notification_template_content", Label: "general_en_us"}, Name: "General English notification content", ID: "22222222-2222-4222-8222-222222222222"},
		ImportID: "11111111-1111-4111-8111-111111111111/general/22222222-2222-4222-8222-222222222222",
		Values:   map[string]any{"email": map[string]any{"from": map[string]any{"address": "notify@dev.example"}}},
	}
	h.s.deps.Exporter = &exporter.Composite{Core: &exporter.CoreExporter{Inventory: hostedCoreInventory{resource}, Reader: hostedCoreReader{schema: schema}}}

	id := h.create(mode)
	h.action(id, "search", url.Values{"query": {"General English notification content"}})
	d := h.wait(id)
	if d.State != "editing" || len(d.Catalog) != 1 || d.Catalog[0].String() != address {
		t.Fatalf("core search did not return notification content: state=%s catalog=%v", d.State, d.Catalog)
	}
	h.action(id, "selection", url.Values{"resource": {address}})
	h.action(id, "preview", nil)
	d = h.wait(id)
	if d.State != "needs_values" {
		t.Fatalf("core preview did not expose missing sender values: state=%s", d.State)
	}
	if d.Report == nil {
		t.Fatal("core preview omitted its readiness report")
	}
	form := url.Values{"resource": {address}}
	for _, env := range []string{"qa", "stage", "prod"} {
		filled := false
		for _, blocker := range d.Report.Blockers[env] {
			if blocker.Code == "missing_value" && blocker.Var != "" {
				form.Set("value."+env+"."+blocker.Var, `"notify+`+env+`@example.test"`)
				filled = true
			}
		}
		if !filled {
			t.Fatalf("notification sender address blocker missing for %s: %+v", env, d.Report.Blockers[env])
		}
	}
	h.action(id, "selection", form)
	d = h.draft(id)
	for _, env := range []string{"qa", "stage", "prod"} {
		if len(d.Request.EnvValues[env]) == 0 {
			t.Fatalf("sender address was not retained for %s: %+v", env, d.Request.EnvValues)
		}
	}
	h.action(id, "preview", nil)
	d = h.wait(id)
	if d.State != "ready" {
		t.Fatalf("core preview state=%s blockers=%+v error=%v", d.State, d.Report.Blockers, d.Report.Error)
	}
	h.action(id, "submit", nil)
	d = h.wait(id)
	wantMRs := 0
	if mode == "mr" {
		wantMRs = 1
	}
	if d.State != "submitted" || d.Report.CommitID == "" || len(h.gl.MRs) != wantMRs {
		t.Fatalf("core publication state=%s commit=%q MRs=%d", d.State, d.Report.CommitID, len(h.gl.MRs))
	}
	commit := h.gl.Commits[d.Report.CommitID]
	if commit == nil {
		t.Fatal("GitLab did not retain the hosted core commit")
	}
	var sourceOverlay string
	for _, action := range commit.Actions {
		path := action["file_path"]
		content := action["content"]
		if path == "dev.tfvars" {
			sourceOverlay = content
		}
		if path == gitlab.RequestFile || path == "auto.tfvars" || strings.HasSuffix(path, ".auto.tfvars") {
			t.Errorf("hosted core commit included an ephemeral transport file: %s", path)
		}
		if path == "imports.tf" && strings.Contains(content, `id       = "11111111-1111-4111-8111-111111111111/general/22222222-2222-4222-8222-222222222222"`) {
			t.Errorf("hosted core commit emitted a literal import ID in %s", path)
		}
		if strings.Contains(content, "fixture-worker-secret") || strings.Contains(content, "fixture-worker-id") {
			t.Errorf("worker credential was committed in %s", path)
		}
	}
	if !strings.Contains(sourceOverlay, `module.davinci.pingone_notification_template_content.general_en_us"`) || !strings.Contains(sourceOverlay, `"general/22222222-2222-4222-8222-222222222222"`) {
		t.Fatalf("source import ID was not preserved under the correct module address: %s", sourceOverlay)
	}
	if got := h.call(http.MethodGet, "/requests/"+id, nil); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), "unknown") {
		t.Fatalf("rollout did not retain unknown readiness: status=%d body=%s", got.Code, got.Body.String())
	}
}
