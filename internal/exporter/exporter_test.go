package exporter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"pingone-promote/internal/exporter/exportertest"
)

var (
	fakeOnce sync.Once
	fakeDir  string
	fake     exportertest.Fake
)

func TestMain(m *testing.M) {
	code := m.Run()
	if fakeDir != "" {
		os.RemoveAll(fakeDir)
	}
	os.Exit(code)
}

func getFake(t *testing.T) exportertest.Fake {
	t.Helper()
	fakeOnce.Do(func() {
		dir, err := os.MkdirTemp("", "fake-terraformer-")
		if err != nil {
			t.Fatal(err)
		}
		fakeDir = dir
		fake = exportertest.Build(t, dir, exportertest.FixtureDir("dev"))
	})
	if fake.Path == "" {
		t.Fatal("fake terraformer was not built")
	}
	_ = os.Remove(fake.RecordPath)
	return fake
}

const devEnv = "11111111-1111-4111-8111-111111111111"

var creds = Credentials{ClientID: "worker-client", ClientSecret: "s3cr3t-value-never-on-argv", WorkerEnvironmentID: devEnv, RegionCode: "NA"}

func runner(f exportertest.Fake) *Runner {
	return &Runner{Config: Config{Version: "v" + f.Version, SHA256: f.SHA256, Path: f.Path}}
}

func TestVerifyChecksumAndVersion(t *testing.T) {
	f := getFake(t)
	ctx := context.Background()
	if err := runner(f).Verify(ctx); err != nil {
		t.Fatal(err)
	}
	r := runner(f)
	r.Config.SHA256 = strings.Repeat("0", 64)
	if err := r.Verify(ctx); !errors.Is(err, ErrChecksum) {
		t.Fatalf("want checksum error, got %v", err)
	}
	r = runner(f)
	r.Config.Version = "0.5.0"
	if err := r.Verify(ctx); !errors.Is(err, ErrVersion) {
		t.Fatalf("want version error, got %v", err)
	}
	// A swapped binary at the configured path is caught before any exec.
	tampered := filepath.Join(t.TempDir(), "tf")
	copyFile(t, f.Path, tampered)
	fh, _ := os.OpenFile(tampered, os.O_APPEND|os.O_WRONLY, 0)
	fh.Write([]byte{0})
	fh.Close()
	r = runner(f)
	r.Config.Path = tampered
	if _, err := r.ListResources(ctx, creds); !errors.Is(err, ErrChecksum) {
		t.Fatalf("want checksum error, got %v", err)
	}
	if n := len(f.Invocations(t)); n != 2 {
		// Only the two successful --version checks ran; the bad sha256 and
		// the tampered copy never executed.
		t.Fatalf("expected 2 executions, got %d", n)
	}
}

func copyFile(t *testing.T, from, to string) {
	src, err := os.Open(from)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(dst, src)
	dst.Close()
}

func TestChildArgvAndEnvAreScrubbed(t *testing.T) {
	f := getFake(t)
	t.Setenv("TF_VAR_davinci_variable_apiKey_value", "leak-1")
	t.Setenv("PINGONE_PROMOTE_GITLAB_TOKEN", "leak-2")
	t.Setenv("CI_JOB_TOKEN", "leak-3")
	t.Setenv("PLUGIN_PROTOCOL_VERSIONS", "1")
	r := runner(f)
	r.Network = Network{HTTPSProxy: "http://proxy.invalid:3128"}
	exp, err := r.Export(context.Background(), creds, ExportOptions{Include: []string{"pingone_davinci_flow.pingcli__Flow-0020-A"}, IncludeUpstream: true, Imports: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(exp.Files) == 0 {
		t.Fatal("no files")
	}
	invs := f.Invocations(t)
	if len(invs) != 2 {
		t.Fatalf("want --version + export, got %d runs", len(invs))
	}
	for _, inv := range invs {
		for _, a := range inv.Argv {
			if strings.Contains(a, creds.ClientSecret) || strings.Contains(a, creds.ClientID) {
				t.Fatalf("credential on argv: %q", inv.Argv)
			}
		}
		allowed := map[string]bool{}
		for _, n := range AllowedEnv {
			allowed[n] = true
		}
		var home string
		for _, kv := range inv.Env {
			name, val, _ := strings.Cut(kv, "=")
			if !allowed[name] {
				t.Fatalf("child received %s", name)
			}
			if strings.Contains(val, "leak-") {
				t.Fatalf("child received a parent secret in %s", name)
			}
			if name == "HOME" {
				home = val
			}
		}
		if home == "" || home == os.Getenv("HOME") {
			t.Fatalf("HOME must be a fresh private dir, got %q", home)
		}
		if _, err := os.Stat(home); !os.IsNotExist(err) {
			t.Fatalf("private HOME %s was not deleted", home)
		}
	}
	// The --version run gets no credentials at all.
	for _, kv := range invs[0].Env {
		if strings.HasPrefix(kv, "PINGCLI_") {
			t.Fatalf("--version received %s", kv)
		}
	}
	argv := invs[1].Argv[1:]
	out := argv[slices.Index(argv, "--out")+1]
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("output dir %s still exists", out)
	}
	want := []string{"export", "--out", out, "--module-dir", "ping-export-module", "--module-name", "ping-export", "--output-format", "hcl",
		"--include-values", "--include-imports", "--include-resources", "pingone_davinci_flow.pingcli__Flow-0020-A", "--include-upstream"}
	if !slices.Equal(argv, want) {
		t.Fatalf("argv\n got %q\nwant %q", argv, want)
	}
}

func TestPrivateDirsAre0700(t *testing.T) {
	w, err := newWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	defer w.close()
	for _, d := range []string{w.root, w.home, w.out, filepath.Dir(w.bin)} {
		fi, err := os.Stat(d)
		if err != nil || fi.Mode().Perm() != 0o700 {
			t.Fatalf("%s: %v %v", d, fi.Mode(), err)
		}
	}
}

func TestPatternsAreValidated(t *testing.T) {
	for _, p := range []string{"a,b", "regex:.*", "x y", "pingone_davinci_flow.a.b", ""} {
		if _, err := (ExportOptions{Include: []string{p}}).Argv("/o"); err == nil {
			t.Fatalf("pattern %q accepted", p)
		}
	}
	if _, err := (ExportOptions{}).Argv("/o"); err == nil {
		t.Fatal("an export without includes must be refused")
	}
}

func TestListResourcesAndSearch(t *testing.T) {
	f := getFake(t)
	list, err := runner(f).ListResources(context.Background(), creds)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 19 {
		t.Fatalf("got %d addresses", len(list))
	}
	cat, err := runner(f).Catalog(context.Background(), creds, true)
	if err != nil {
		t.Fatal(err)
	}
	got := Search(cat, "flow b")
	if len(got) != 1 || got[0].Name != "Flow B" || got[0].ID != "f0000000000000000000000000000002" {
		t.Fatalf("exact name should win: %+v", got)
	}
	if got := Search(cat, "flow"); len(got) != 4 {
		t.Fatalf("substring search: %+v", got)
	}
	if got := Search(cat, "F0000000000000000000000000000004"); len(got) != 1 || got[0].Name != "Flow C" {
		t.Fatalf("id search: %+v", got)
	}
	if got := Search(cat, "Subfl"); len(got) != 1 || got[0].Label != "pingcli__Flow-0020-B-0020-Subflow" {
		t.Fatalf("subflow: %+v", got)
	}
	if got := Search(cat, "nope"); len(got) != 0 {
		t.Fatal("no match expected")
	}
	// Decoding works without IDs too.
	cat, _ = runner(f).Catalog(context.Background(), creds, false)
	if got := Search(cat, "Flow B Subflow"); len(got) != 1 {
		t.Fatalf("decoded names: %+v", got)
	}
}

func TestScopedExportFollowsUpstreamAndCompanions(t *testing.T) {
	f := getFake(t)
	exp, err := runner(f).ScopedExport(context.Background(), creds, []string{"pingone_davinci_flow.pingcli__Flow-0020-B"})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := Addresses(exp)
	want := []string{
		"pingone_davinci_connector_instance.pingcli__Http",
		"pingone_davinci_connector_instance.pingcli__PingOne",
		"pingone_davinci_flow.pingcli__Flow-0020-B",
		"pingone_davinci_flow.pingcli__Flow-0020-B-0020-Subflow",
		"pingone_davinci_flow_deploy.pingcli__Flow-0020-B",
		"pingone_davinci_flow_deploy.pingcli__Flow-0020-B-0020-Subflow",
		"pingone_davinci_flow_enable.pingcli__Flow-0020-B",
		"pingone_davinci_flow_enable.pingcli__Flow-0020-B-0020-Subflow",
		"pingone_davinci_variable.pingcli__apiKey_company",
		"pingone_davinci_variable.pingcli__attempts_flowInstance",
		"pingone_davinci_variable.pingcli__companyName_company",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got\n%s", strings.Join(got, "\n"))
	}
	if len(exp.Findings) != 0 {
		t.Fatalf("findings: %+v", exp.Findings)
	}
}

func TestDuplicateLabelsAreTyped(t *testing.T) {
	fixture := t.TempDir()
	if err := os.WriteFile(filepath.Join(fixture, "fail-duplicate-labels"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f := exportertest.Build(t, t.TempDir(), fixture)
	_, err := runner(f).Export(context.Background(), creds, ExportOptions{Include: []string{"*"}})
	var lc *LabelCollisionError
	if !errors.As(err, &lc) {
		t.Fatalf("want LabelCollisionError, got %v", err)
	}
	if strings.Contains(err.Error(), creds.ClientSecret) {
		t.Fatal("secret in error")
	}
}

func TestCredentialsAreValidated(t *testing.T) {
	f := getFake(t)
	bad := creds
	bad.RegionCode = "XX"
	if _, err := runner(f).ListResources(context.Background(), bad); err == nil {
		t.Fatal("bad region accepted")
	}
	if len(f.Invocations(t)) != 0 {
		t.Fatal("nothing may run with invalid credentials")
	}
}

// TestRealTerraformer verifies a real pingcli-terraformer build when
// PINGONE_PROMOTE_REAL_TERRAFORMER names one (it was built from source at
// v0.6.0 for this change). It never contacts PingOne.
func TestRealTerraformer(t *testing.T) {
	bin := os.Getenv("PINGONE_PROMOTE_REAL_TERRAFORMER")
	if bin == "" {
		t.Skip("set PINGONE_PROMOTE_REAL_TERRAFORMER to a pingcli-terraformer v0.6.0 binary")
	}
	b, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	r := &Runner{Config: Config{Version: "v0.6.0", SHA256: hex.EncodeToString(sum[:]), Path: bin}}
	if err := r.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
}
