package tfvars

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files (and the module test fixture) from the Go fixtures")

// moduleFixtures are the tfvars files the Terraform tests load (the module
// itself, and the reference root stack calling it as a child module). They
// must be byte-identical to testdata/full.tfvars so Go and Terraform agree on
// schema v1.
var moduleFixtures = []string{
	filepath.Join("..", "..", "terraform", "modules", "pingone-davinci", "tests", "golden_full.auto.tfvars"),
	filepath.Join("..", "..", "terraform", "root", "tests", "golden_full.auto.tfvars"),
}

// rootSecretsFile is the reference root stack's secrets.tf, generated from the
// full golden's required secrets.
var rootSecretsFile = filepath.Join("..", "..", "terraform", "root", "secrets.tf")

func TestRootSecretsFileMatchesGolden(t *testing.T) {
	got, err := WriteSecretsTF(SecretKeysOf(fullDocument()))
	if err != nil {
		t.Fatal(err)
	}
	if *update {
		if err := os.WriteFile(rootSecretsFile, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(rootSecretsFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is stale; run go test ./internal/tfvars -update", rootSecretsFile)
	}
	if _, err := ResolveFormatter(""); err == nil {
		if _, err := FormatWithTerraform(context.Background(), "", got); err != nil {
			t.Fatalf("secrets.tf is not terraform fmt clean: %v", err)
		}
	}
}

func TestWriteSecretsTF(t *testing.T) {
	out, err := WriteSecretsTF([]string{"b_key", "A_Key", "b_key"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Count(s, "variable \"") != 2 || strings.Index(s, "\"A_Key\"") > strings.Index(s, "\"b_key\"") || !strings.Contains(s, "sensitive   = true") || !strings.Contains(s, "default     = null") {
		t.Fatalf("unexpected secrets.tf:\n%s", s)
	}
	for _, bad := range []string{"1abc", "has-dash", "parameters", "count", "pingone_secrets"} {
		var e *SecretsFileError
		if _, err := WriteSecretsTF([]string{bad}); !errors.As(err, &e) {
			t.Errorf("accepted %q", bad)
		}
	}
	empty, err := WriteSecretsTF(nil)
	if err != nil || !strings.Contains(string(empty), "pingone_secrets = {}") {
		t.Fatalf("empty: %v %s", err, empty)
	}
}

var goldens = map[string]func() *Document{
	"full":    fullDocument,
	"minimal": emptyDocument,
}

func goldenPath(name string) string { return filepath.Join("testdata", name+".tfvars") }

func TestGoldenWrite(t *testing.T) {
	for name, build := range goldens {
		t.Run(name, func(t *testing.T) {
			got, err := Write(build())
			if err != nil {
				t.Fatal(err)
			}
			if *update {
				if err := os.WriteFile(goldenPath(name), got, 0o644); err != nil {
					t.Fatal(err)
				}
				if name == "full" {
					for _, fixture := range moduleFixtures {
						if err := os.WriteFile(fixture, got, 0o644); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			want, err := os.ReadFile(goldenPath(name))
			if err != nil {
				t.Fatalf("%v (run go test ./internal/tfvars -update)", err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("writer output differs from %s at line %d; run with -update after review", goldenPath(name), firstDiffLine(got, want))
			}
		})
	}
}

func TestGoldenRoundTripIsByteIdentical(t *testing.T) {
	for name := range goldens {
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(goldenPath(name))
			if err != nil {
				t.Fatal(err)
			}
			doc, err := Parse(src, goldenPath(name))
			if err != nil {
				t.Fatal(err)
			}
			out, err := Write(doc)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(out, src) {
				t.Fatalf("Parse then Write changed line %d", firstDiffLine(out, src))
			}
			if r := Validate(doc); !r.OK() {
				t.Fatalf("golden is invalid: %+v", r.Problems)
			}
			if changes := Diff(doc, doc); len(changes) != 0 {
				t.Fatalf("self diff: %+v", changes)
			}
		})
	}
}

func TestModuleFixtureMatchesGolden(t *testing.T) {
	golden, err := os.ReadFile(goldenPath("full"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range moduleFixtures {
		fixture, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(golden, fixture) {
			t.Fatalf("%s is stale; run go test ./internal/tfvars -update", path)
		}
	}
}

func TestFullGoldenRequirements(t *testing.T) {
	src, err := os.ReadFile(goldenPath("full"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := Parse(src, "full.tfvars")
	if err != nil {
		t.Fatal(err)
	}
	r := Validate(doc)
	want := []string{"PingOne_DaVinci_connection_PingOne_clientSecret", "davinci_variable_apiToken_value", "login_hmac_key"}
	if len(r.RequiredSecrets) != len(want) {
		t.Fatalf("required secrets %v, want %v", r.RequiredSecrets, want)
	}
	for i := range want {
		if r.RequiredSecrets[i] != want[i] {
			t.Fatalf("required secrets %v, want %v", r.RequiredSecrets, want)
		}
	}
	if got := *doc.Variables["greeting_flowInstance"].Value.String; got != "Hello ${user.name}" {
		t.Fatal("template sequence in a plain string did not round-trip")
	}
}

// terraform fmt must agree with the writer. Skips when no formatter is on PATH.
func TestTerraformFmtAgreesWithGoldens(t *testing.T) {
	bin, err := ResolveFormatter("")
	if err != nil {
		t.Skip("terraform or tofu not on PATH")
	}
	for name := range goldens {
		src, err := os.ReadFile(goldenPath(name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := FormatWithTerraform(context.Background(), bin, src); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	cmd := exec.Command(bin, "fmt", "-check", "-no-color", "testdata")
	cmd.Env = formatterEnv(t.TempDir())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("terraform fmt -check testdata: %v\n%s", err, out)
	}
}

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
