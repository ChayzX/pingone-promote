package tfvars

import (
	"pingone-promote/internal/tffmt"

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
}

// rootSecretsFile is the reference root stack's secrets.tf, generated from the
// full golden's required secrets.
// (The v2 reference root in terraform/root no longer uses this format; the
// v1 golden lives in testdata until the v1 code is deleted.)
var rootSecretsFile = filepath.Join("testdata", "v1_root_secrets.tf")

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
	cmd.Env = tffmt.SealedEnv(t.TempDir())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("terraform fmt -check testdata: %v\n%s", err, out)
	}
}

func firstDiffLine(a, b []byte) int {
	al, bl := strings.Split(string(a), "\n"), strings.Split(string(b), "\n")
	for i := 0; i < len(al) || i < len(bl); i++ {
		if i >= len(al) || i >= len(bl) || al[i] != bl[i] {
			return i + 1
		}
	}
	return 0
}
