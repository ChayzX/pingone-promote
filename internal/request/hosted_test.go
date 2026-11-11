package request

import (
	"context"
	"encoding/json"
	"pingone-promote/internal/exporter"
	"pingone-promote/internal/secretscan"
	"strings"
	"testing"

	"pingone-promote/internal/gitlab"
)

func TestGenericSelectionContract(t *testing.T) {
	for _, s := range []string{
		`"resources":[{"type":"pingone_notification_template_content","id":"template-1"}]`,
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
	raw := requestJSON(func(m map[string]any) { m["includeUpstream"] = false; m["dryRun"] = true })
	h := newHarness(t, raw)
	res := h.run(raw)
	if res.Error != nil {
		if res.Status != StatusBlocked {
			t.Fatalf("no-upstream export failed: %v", res.Error)
		}
		values := map[string]map[string]string{}
		for env, blockers := range res.Blockers {
			for _, b := range blockers {
				if b.Code != "missing_value" || b.Var == "" {
					t.Fatalf("unexpected blocker: %+v", b)
				}
				if values[env] == nil {
					values[env] = map[string]string{}
				}
				values[env][b.Var] = "fixture-existing-dependency-id"
			}
		}
		var fields map[string]any
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		fields["envValues"] = values
		raw, _ = json.Marshal(fields)
		res = h.run(raw)
		if res.Error != nil || res.Status != StatusPreviewed {
			t.Fatalf("filled export still blocked: %v %+v", res.Error, res.Blockers)
		}
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
}

type secretFindingExporter struct{ Exporter }

func (e secretFindingExporter) ScopedExport(ctx context.Context, creds exporter.Credentials, seeds, exclude []string) (*exporter.Export, error) {
	exp, err := e.Exporter.ScopedExport(ctx, creds, seeds, exclude)
	if err != nil {
		return nil, err
	}
	exp.Files[exp.ModuleDir+"/pingone_notification_template_content.tf"] = []byte(`resource "pingone_notification_template_content" "pingcli__Fixture" {
 environment_id = var.pingone_environment_id
 name = "Fixture"
 content = "-----BEGIN PRIVATE KEY-----\nfixture-private-sentinel\n-----END PRIVATE KEY-----"
}`)
	exp.Findings = append(exp.Findings, secretscan.ScanFiles(exp.Files)...)
	return exp, nil
}
func TestBlockedSecretPreviewOmitsDiff(t *testing.T) {
	raw := requestJSON(func(m map[string]any) { m["dryRun"] = true })
	h := newHarness(t, raw)
	h.env.Exporter = secretFindingExporter{h.env.Exporter}
	before := len(h.gl.CommitOrder)
	res := h.run(raw)
	if res.Status != StatusBlocked {
		t.Fatalf("secret export was not blocked: %s %v", res.Status, res.Error)
	}
	if res.Diff != "" {
		t.Fatal("blocked secret preview exposes raw diff")
	}
	if len(h.gl.CommitOrder) != before {
		t.Fatal("blocked secret export wrote a commit")
	}
}
