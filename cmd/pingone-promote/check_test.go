package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"pingone-promote/internal/tfvars"
)

var goldenFull = filepath.Join("..", "..", "internal", "tfvars", "testdata", "full.tfvars")

func check(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := runCheck(context.Background(), args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func writeVariant(t *testing.T, edit func(string) string) string {
	t.Helper()
	src, err := os.ReadFile(goldenFull)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "env.tfvars")
	if err := os.WriteFile(p, []byte(edit(string(src))), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCheckValidGolden(t *testing.T) {
	code, out, _ := check(t, goldenFull)
	if code != checkOK || !strings.Contains(out, ": OK (tfvars schema v1, canonical)") {
		t.Fatalf("code=%d out=%q", code, out)
	}
	code, out, _ = check(t, "--required-secrets", goldenFull)
	if code != checkOK || !strings.Contains(out, "required pingone_secrets keys (3):\n  dv_api_token\n  login_hmac_key\n  pingone_sso_client_secret\n") {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestCheckJSONOutput(t *testing.T) {
	code, out, _ := check(t, goldenFull, "--format", "json") // flags after the file
	if code != checkOK {
		t.Fatalf("code=%d out=%s", code, out)
	}
	var got checkOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if !got.OK || !got.Canonical || len(got.Problems) != 0 || len(got.RequiredSecrets) != 3 || got.RequiredParameters[0] != "idp_base_url" {
		t.Fatalf("unexpected JSON: %+v", got)
	}
}

func TestCheckFindsProblems(t *testing.T) {
	cases := map[string]struct {
		edit func(string) string
		code string
	}{
		"tier violation": {func(s string) string {
			return strings.Replace(s, "name    = \"MFA subflow\"\n    tier    = 2", "name    = \"MFA subflow\"\n    tier    = 3", 1)
		}, tfvars.CodeTierOrder},
		"masked secret": {func(s string) string {
			return strings.Replace(s, `"value": "fake-client-id"`, `"value": "******"`, 1)
		}, tfvars.CodeMaskedSecret},
		"hand comment": {func(s string) string {
			return strings.Replace(s, "\ndavinci_flows = {", "\n# my note\ndavinci_flows = {", 1)
		}, codeNotCanonical},
		"expression": {func(s string) string {
			return strings.Replace(s, `name      = "Http"`, `name      = lower("Http")`, 1)
		}, codeParse},
		"unknown reference": {func(s string) string {
			return strings.Replace(s, "@@flow:error_leaf@@", "@@flow:missing_flow@@", 1)
		}, tfvars.CodeRefUnknown},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			p := writeVariant(t, tc.edit)
			code, out, _ := check(t, "--format=json", p)
			if code != checkProblems {
				t.Fatalf("code=%d out=%s", code, out)
			}
			var got checkOutput
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, pr := range got.Problems {
				found = found || pr.Code == tc.code
			}
			if got.OK || !found {
				t.Fatalf("want %s in %+v", tc.code, got.Problems)
			}
			code, text, _ := check(t, p)
			if code != checkProblems || !strings.Contains(text, "["+tc.code+"]") {
				t.Fatalf("text output: %q", text)
			}
		})
	}
}

func TestCheckUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{},
		{goldenFull, goldenFull},
		{"--format", "yaml", goldenFull},
		{"--bogus", goldenFull},
		{filepath.Join(t.TempDir(), "missing.tfvars")},
		{t.TempDir()},
		{"--terraform-fmt=" + filepath.Join(t.TempDir(), "no-terraform"), goldenFull},
	} {
		if code, _, _ := check(t, args...); code != checkUsage {
			t.Errorf("args %q: code=%d, want %d", args, code, checkUsage)
		}
	}
	if code, out, _ := check(t, "--help"); code != checkOK || !strings.Contains(out, "Usage: pingone-promote check") {
		t.Fatalf("help: %d %q", code, out)
	}
}

func TestCheckTerraformFmt(t *testing.T) {
	if _, err := tfvars.ResolveFormatter(""); err != nil {
		t.Skip("terraform or tofu not on PATH")
	}
	code, out, _ := check(t, "--terraform-fmt", "--format", "json", goldenFull)
	if code != checkOK || !strings.Contains(out, `"terraform_fmt": "ok"`) {
		t.Fatalf("code=%d out=%s", code, out)
	}
	p := writeVariant(t, func(s string) string {
		return strings.Replace(s, "tfvars_schema_version  = 1", "tfvars_schema_version = 1", 1)
	})
	code, out, _ = check(t, "--terraform-fmt", p)
	if code != checkProblems || !strings.Contains(out, "[terraform_fmt]") || !strings.Contains(out, "[not_canonical]") {
		t.Fatalf("code=%d out=%s", code, out)
	}
}

func TestCheckHelperProcess(t *testing.T) {
	if os.Getenv("PINGONE_CHECK_HELPER") != "1" {
		return
	}
	os.Args = append([]string{"pingone-promote"}, strings.Split(os.Getenv("PINGONE_CHECK_ARGS"), "\x1f")...)
	main()
}

// check is offline: run the real main() in a child process with every proxy
// variable pointed at a local listener and assert nothing connects to it,
// including the optional terraform fmt step. A second test proves the
// validation packages do not even link the network stack.
func TestCheckMakesNoNetworkConnections(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var connections atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			connections.Add(1)
			c.Close()
		}
	}()
	proxy := "http://" + ln.Addr().String()
	args := []string{"check", "--format=json", "--required-secrets", goldenFull}
	if _, err := tfvars.ResolveFormatter(""); err == nil {
		args = append(args, "--terraform-fmt")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestCheckHelperProcess$")
	cmd.Env = append(os.Environ(),
		"PINGONE_CHECK_HELPER=1",
		"PINGONE_CHECK_ARGS="+strings.Join(args, "\x1f"),
		"HTTP_PROXY="+proxy, "HTTPS_PROXY="+proxy, "ALL_PROXY="+proxy,
		"http_proxy="+proxy, "https_proxy="+proxy, "all_proxy="+proxy, "NO_PROXY=", "no_proxy=",
	)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("check failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), `"ok": true`) {
		t.Fatalf("unexpected output: %s", out)
	}
	time.Sleep(50 * time.Millisecond)
	if n := connections.Load(); n != 0 {
		t.Fatalf("check made %d network connection(s)", n)
	}
}

func TestValidationPackagesDoNotLinkNetworking(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go tool not on PATH")
	}
	out, err := exec.Command(goBin, "list", "-deps", "pingone-promote/internal/tfvars", "pingone-promote/internal/keys").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for _, dep := range strings.Fields(string(out)) {
		if dep == "net" || strings.HasPrefix(dep, "net/") || dep == "crypto/tls" {
			t.Fatalf("validation packages depend on %s", dep)
		}
	}
}
