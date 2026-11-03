package promote

import (
	"bytes"
	"context"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"pingone-promote/internal/pipelinesecrets"
	"pingone-promote/internal/tfvars"
	"pingone-promote/internal/translate"
)

var update = flag.Bool("update", false, "rewrite golden files and the module e2e fixture")

var (
	moduleDir     = filepath.Join("..", "..", "terraform", "modules", "pingone-davinci")
	moduleE2EFile = filepath.Join(moduleDir, "tests", "e2e", "promoted.auto.tfvars")
	secretsYAML   = filepath.Join("..", "pipelinesecrets", "testdata", "customer_secrets.yml")
)

func goldenFile(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		os.MkdirAll("testdata", 0o755)
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/promote -update)", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s differs; review and run with -update", path)
	}
}

// TestEndToEnd: fixture inventory -> translate -> merge into the target ->
// write tfvars, secrets.tf and customer_secrets.yml -> terraform fmt gate ->
// the module's e2e terraform test consumes the promoted tfvars.
func TestEndToEnd(t *testing.T) {
	// The target starts from a checked-in tfvars file.
	targetSrc, err := tfvars.Write(targetDocument(t))
	if err != nil {
		t.Fatal(err)
	}
	goldenFile(t, "e2e_target.tfvars", targetSrc)
	target, err := tfvars.Parse(targetSrc, "e2e_target.tfvars")
	if err != nil {
		t.Fatal(err)
	}

	// The target's connection already uses the existing pipeline secret
	// PingOne_DaVinci_connection_client; the operator acknowledges the
	// heuristic match on the HMAC key, which becomes a new pipeline secret.
	src, err := TranslateFor(sourceInventory(t), target, translate.Options{
		Acknowledged: map[string]bool{"node:Login/n3sso/hmacKey": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := Plan(src, target, Selection{Flows: []string{loginKey}, Applications: []string{appKey}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Blocked() || len(res.Problems) != 0 || len(res.MissingParameters) != 0 {
		t.Fatalf("blocks=%+v problems=%+v missing=%v", res.Blocks, res.Problems, res.MissingParameters)
	}

	promoted, err := tfvars.Write(res.Doc)
	if err != nil {
		t.Fatal(err)
	}
	goldenFile(t, "e2e_promoted.tfvars", promoted)
	if *update {
		os.MkdirAll(filepath.Dir(moduleE2EFile), 0o755)
		if err := os.WriteFile(moduleE2EFile, promoted, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if fixture, err := os.ReadFile(moduleE2EFile); err != nil || !bytes.Equal(fixture, promoted) {
		t.Fatalf("%s is stale; run go test ./internal/promote -update", moduleE2EFile)
	}

	secretsTF, err := tfvars.WriteSecretsTF(res.RequiredSecrets)
	if err != nil {
		t.Fatal(err)
	}
	goldenFile(t, "e2e_secrets.tf", secretsTF)

	yml, err := os.ReadFile(secretsYAML)
	if err != nil {
		t.Fatal(err)
	}
	ps, err := pipelinesecrets.Parse(yml)
	if err != nil {
		t.Fatal(err)
	}
	report := ps.Check(pipelinesecrets.DefaultConfig, res.RequiredSecrets)
	var add []pipelinesecrets.NewEntry
	for _, k := range report.Undeclared {
		add = append(add, pipelinesecrets.NewEntry{VarName: k})
	}
	if strings.Join(report.Declared, ",") != "PingOne_DaVinci_connection_client" || len(add) != 1 {
		t.Fatalf("customer secrets report %+v", report)
	}
	updated, err := ps.Add(pipelinesecrets.DefaultConfig, add)
	if err != nil {
		t.Fatal(err)
	}
	goldenFile(t, "e2e_customer_secrets.yml", updated)

	// No source secret reaches any output.
	re := regexp.MustCompile(`LEAKCANARY-[a-z-]+-[0-9]{4}`)
	for name, out := range map[string][]byte{"tfvars": promoted, "secrets.tf": secretsTF, "customer_secrets.yml": updated} {
		if m := re.Find(out); m != nil || bytes.Contains(out, []byte("******")) {
			t.Fatalf("%s leaked a source secret", name)
		}
	}

	// terraform fmt gate, when a formatter is available.
	if _, err := tfvars.ResolveFormatter(""); err == nil {
		for name, out := range map[string][]byte{"tfvars": promoted, "secrets.tf": secretsTF, "target": targetSrc} {
			if _, err := tfvars.FormatWithTerraform(context.Background(), "", out); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		}
	}

	// The module test needs the provider in its .terraform directory, which
	// may require a download, so it runs only on request.
	if os.Getenv("PINGONE_PROMOTE_TF_E2E") != "1" {
		t.Log("set PINGONE_PROMOTE_TF_E2E=1 to also run terraform test -test-directory=tests/e2e")
		return
	}
	bin, err := tfvars.ResolveFormatter("")
	if err != nil {
		t.Skip("terraform not on PATH")
	}
	cmd := exec.Command(bin, "test", "-no-color", "-test-directory=tests/e2e")
	cmd.Dir = moduleDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("terraform test: %v\n%s", err, out)
	}
}
