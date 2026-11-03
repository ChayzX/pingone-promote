package tffmt

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestFormatWithTerraformDetectsMismatch(t *testing.T) {
	if _, err := ResolveFormatter(""); err != nil {
		t.Skip("terraform or tofu not on PATH")
	}
	_, err := FormatWithTerraform(context.Background(), "", []byte("a   = 1\nbb = 2\n"))
	var mismatch *FormatMismatchError
	if !errors.As(err, &mismatch) || mismatch.Line != 1 {
		t.Fatalf("want mismatch at line 1, got %v", err)
	}
	_, err = FormatWithTerraform(context.Background(), "", []byte("a = {\n"))
	var failed *FormatterFailedError
	if !errors.As(err, &failed) {
		t.Fatalf("want formatter failure for invalid HCL, got %v", err)
	}
}

func TestFormatWithTerraformMissingBinary(t *testing.T) {
	_, err := FormatWithTerraform(context.Background(), filepath.Join(t.TempDir(), "no-terraform"), []byte("a = 1\n"))
	var missing *FormatterNotFoundError
	if !errors.As(err, &missing) {
		t.Fatalf("want FormatterNotFoundError, got %v", err)
	}
}

// The formatter child gets a fixed environment: nothing inherited, proxies
// pointed at a closed loopback port, and an empty CLI configuration.
func TestFormatterEnvironmentIsSealed(t *testing.T) {
	t.Setenv("PINGONE_SECRET_CANARY", "do-not-leak")
	t.Setenv("HTTPS_PROXY", "http://proxy.example.com:3128")
	env := formatterEnv("/tmp/x")
	seen := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		seen[k] = v
	}
	if _, leaked := seen["PINGONE_SECRET_CANARY"]; leaked {
		t.Fatal("parent environment leaked into the formatter")
	}
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		if seen[k] != "http://127.0.0.1:9" {
			t.Fatalf("%s=%q, want the loopback discard port", k, seen[k])
		}
	}
	if seen["CHECKPOINT_DISABLE"] != "1" || seen["TF_CLI_CONFIG_FILE"] == "" || seen["PATH"] != "" {
		t.Fatalf("unexpected env %v", env)
	}
}
