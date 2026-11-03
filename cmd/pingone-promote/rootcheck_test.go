package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pingone-promote/internal/promote"
)

func copyRepo(t *testing.T) string {
	t.Helper()
	src := filepath.Join("..", "..", "internal", "promote", "testdata", "repo")
	dst := t.TempDir()
	err := filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if fi.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func rootCheck(t *testing.T, args ...string) (int, promote.CheckReport) {
	t.Helper()
	var out, errb bytes.Buffer
	code := runCheck(context.Background(), append(args, "--format", "json"), &out, &errb)
	var rep promote.CheckReport
	if code != checkUsage {
		if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
			t.Fatalf("%v\n%s\n%s", err, out.String(), errb.String())
		}
	}
	return code, rep
}

func codes(rep promote.CheckReport) string {
	var c []string
	for _, p := range rep.Problems {
		c = append(c, p.Code)
	}
	return strings.Join(c, ",")
}

func TestRootCheckFixtureIsValid(t *testing.T) {
	dir := copyRepo(t)
	code, rep := rootCheck(t, "--root", dir, "--customer-secrets", filepath.Join(dir, "customer_secrets.yml"), "--envs", "dev,qa,stage,prod")
	if code != checkOK || !rep.OK {
		t.Fatalf("code %d: %+v", code, rep.Problems)
	}
	if strings.Join(rep.RequiredSecrets, ",") != "PingOne_DaVinci_connection_PingOne_clientSecret,davinci_variable_apiKey_value" {
		t.Fatalf("required secrets: %v", rep.RequiredSecrets)
	}
}

func TestRootCheckFindsProblems(t *testing.T) {
	cases := map[string]struct {
		mutate func(dir string)
		args   []string
		want   string
	}{
		"missing value": {func(d string) {
			b, _ := os.ReadFile(filepath.Join(d, "prod.tfvars"))
			b = bytes.Replace(b, []byte("davinci_variable_companyName_value"), []byte("# removed"), 1)
			os.WriteFile(filepath.Join(d, "prod.tfvars"), b, 0o644)
		}, nil, promote.BlockMissingValue},
		"undeclared key": {func(d string) {
			f, _ := os.OpenFile(filepath.Join(d, "qa.tfvars"), os.O_APPEND|os.O_WRONLY, 0)
			f.WriteString("typo_value = \"x\"\n")
			f.Close()
		}, nil, promote.BlockUndeclaredKey},
		"secret in overlay": {func(d string) {
			f, _ := os.OpenFile(filepath.Join(d, "qa.tfvars"), os.O_APPEND|os.O_WRONLY, 0)
			f.WriteString("davinci_variable_apiKey_value = \"x\"\n")
			f.Close()
		}, nil, promote.BlockSecretInOverlay},
		"bad import key": {func(d string) {
			f, _ := os.OpenFile(filepath.Join(d, "qa.tfvars"), os.O_APPEND|os.O_WRONLY, 0)
			f.WriteString("import_ids = { \"module.davinci.pingone_davinci_flow.pingcli__Nope\" = \"x\" }\n")
			f.Close()
		}, nil, promote.BlockImportKey},
		"auto tfvars": {func(d string) {
			os.WriteFile(filepath.Join(d, "ping-export-terraform.auto.tfvars"), []byte("davinci_variable_apiKey_value = \"\"\n"), 0o644)
		}, nil, promote.BlockAutoTfvars},
		"literal import": {func(d string) {
			f, _ := os.OpenFile(filepath.Join(d, "imports.tf"), os.O_APPEND|os.O_WRONLY, 0)
			f.WriteString("\nimport {\n  to = module.davinci.pingone_davinci_flow.pingcli__Flow-0020-A\n  id = \"x/y\"\n}\n")
			f.Close()
		}, nil, "literal_import"},
		"missing env overlay": {func(string) {}, []string{"--envs", "dev,qa,stage,prod,perf"}, promote.BlockMissingOverlay},
		"secret undeclared": {func(d string) {
			os.WriteFile(filepath.Join(d, "customer_secrets.yml"), []byte(".customer-secrets:\n  secrets:\n    TF_VAR_client_secret:\n      vault: apps/pingone-davinci/${VAULT_PATH_ENV}/pingone/client_secret@secret\n"), 0o644)
		}, []string{"--customer-secrets", "CS"}, "secret_undeclared"},
		"masked value": {func(d string) {
			b, _ := os.ReadFile(filepath.Join(d, "definitions", "pingone_davinci_flow.tf"))
			b = bytes.Replace(b, []byte("Flow A start"), []byte("******"), 1)
			os.WriteFile(filepath.Join(d, "definitions", "pingone_davinci_flow.tf"), b, 0o644)
		}, nil, promote.BlockSecretFinding},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := copyRepo(t)
			tc.mutate(dir)
			args := []string{"--root", dir}
			for _, a := range tc.args {
				args = append(args, strings.ReplaceAll(a, "CS", filepath.Join(dir, "customer_secrets.yml")))
			}
			code, rep := rootCheck(t, args...)
			if code != checkProblems || !strings.Contains(codes(rep), tc.want) {
				t.Fatalf("code %d, problems %s", code, codes(rep))
			}
		})
	}
}

func TestRootCheckTerraform(t *testing.T) {
	bin := os.Getenv("PINGONE_PROMOTE_TERRAFORM")
	if bin == "" {
		t.Skip("set PINGONE_PROMOTE_TERRAFORM (and PINGONE_PROMOTE_PLUGIN_DIR for offline init)")
	}
	dir := copyRepo(t)
	args := []string{"--root", dir, "--terraform-fmt=" + bin, "--terraform-validate=" + bin}
	if pd := os.Getenv("PINGONE_PROMOTE_PLUGIN_DIR"); pd != "" {
		args = append(args, "--plugin-dir", pd)
	}
	code, rep := rootCheck(t, args...)
	if code != checkOK || rep.TerraformFmt != "ok" || rep.TerraformValidate != "ok" {
		t.Fatalf("code %d %+v", code, rep)
	}
}
