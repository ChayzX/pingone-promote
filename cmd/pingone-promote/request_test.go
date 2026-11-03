package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"pingone-promote/internal/exporter/exportertest"
	"pingone-promote/internal/request"
)

func cliEnvFor(t *testing.T) func(string) string {
	t.Helper()
	tf := os.Getenv("PINGONE_PROMOTE_TERRAFORM")
	if tf == "" {
		p, err := exec.LookPath("terraform")
		if err != nil {
			t.Skip("terraform not found")
		}
		tf = p
	}
	fake := exportertest.Build(t, t.TempDir(), exportertest.FixtureDir("dev"))
	vals := map[string]string{
		"PINGONE_PROMOTE_WORKER_CLIENT_ID": "c", "PINGONE_PROMOTE_WORKER_CLIENT_SECRET": "s",
		"PINGONE_PROMOTE_WORKER_ENVIRONMENT_ID": "11111111-1111-4111-8111-111111111111", "PINGONE_PROMOTE_REGION_CODE": "NA",
		"PINGONE_PROMOTE_TERRAFORMER_PATH": fake.Path, "PINGONE_PROMOTE_TERRAFORMER_SHA256": fake.SHA256, "PINGONE_PROMOTE_TERRAFORMER_VERSION": "v" + fake.Version,
		"PINGONE_PROMOTE_ROLLOUT": "qa,stage,prod", "PINGONE_PROMOTE_SOURCES": "dev", "PINGONE_PROMOTE_TERRAFORM_BIN": tf,
	}
	return func(k string) string { return vals[k] }
}

func TestRequestCommandWritesTypedErrorForMalformedRequest(t *testing.T) {
	ws := t.TempDir()
	os.MkdirAll(filepath.Join(ws, ".pingone-promote"), 0o755)
	os.WriteFile(filepath.Join(ws, ".pingone-promote", "request.json"), []byte(`{"requestVersion": 2}`), 0o644)
	result := filepath.Join(t.TempDir(), "result.json")
	var out, errb bytes.Buffer
	code := runRequest(context.Background(), []string{"--workspace", ws, "--result", result}, func(string) string { return "" }, &out, &errb)
	if code != 1 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	var res request.Result
	b, _ := os.ReadFile(result)
	if err := json.Unmarshal(b, &res); err != nil || res.Status != request.StatusFailed || res.Error.Code != request.ErrPreflight {
		t.Fatalf("%v %s", err, b)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if !strings.HasPrefix(lines[len(lines)-1], request.LogPrefix) {
		t.Fatalf("last line: %q", lines[len(lines)-1])
	}
}

func TestExportCommandAmbiguousThenLocalMerge(t *testing.T) {
	getenv := cliEnvFor(t)
	repo := copyRepo(t)
	var out, errb bytes.Buffer
	code := runExport(context.Background(), []string{"--source", "dev", "--flow", "flow", "--repo", repo}, getenv, &out, &errb)
	if code != 1 || !strings.Contains(out.String(), "Flow B Subflow") || !strings.Contains(errb.String(), "4 flows match") {
		t.Fatalf("code %d\n%s\n%s", code, out.String(), errb.String())
	}
	out.Reset()
	code = runExport(context.Background(), []string{"--source", "dev", "--flow", "Flow B", "--repo", repo, "--dry-run"}, getenv, &out, &errb)
	if code != 0 || !strings.Contains(out.String(), "status: previewed") || !strings.Contains(out.String(), "+resource \"pingone_davinci_flow\"") {
		t.Fatalf("dry run: %d\n%s\n%s", code, out.String(), errb.String())
	}
	before, _ := os.ReadFile(filepath.Join(repo, "definitions", "pingone_davinci_flow.tf"))
	out.Reset()
	if code := runExport(context.Background(), []string{"--source", "dev", "--flow", "f0000000000000000000000000000002", "--repo", repo}, getenv, &out, &errb); code != 0 {
		t.Fatalf("export: %d %s %s", code, out.String(), errb.String())
	}
	after, _ := os.ReadFile(filepath.Join(repo, "definitions", "pingone_davinci_flow.tf"))
	if bytes.Equal(before, after) || !bytes.Contains(after, []byte("pingcli__Flow-0020-B-0020-Subflow")) {
		t.Fatal("flow file not updated")
	}
	out.Reset()
	runExport(context.Background(), []string{"--source", "dev", "--flow", "Flow B", "--repo", repo}, getenv, &out, &errb)
	if !strings.Contains(out.String(), "status: no_changes") {
		t.Fatalf("re-run: %s", out.String())
	}
	if code, rep := rootCheck(t, "--root", repo, "--envs", "dev,qa,stage,prod"); code != checkOK {
		t.Fatalf("check after export: %+v", rep.Problems)
	}
}
