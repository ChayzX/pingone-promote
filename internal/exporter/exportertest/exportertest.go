// Package exportertest builds a fake pingcli-terraformer for tests.
package exportertest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Version is the version the fake reports.
const Version = "0.6.0"

// Fake is a built fake terraformer.
type Fake struct {
	Path, SHA256, Version string
	// RecordPath collects one JSON line per invocation: argv and environ.
	RecordPath string
}

// Invocation is one recorded run.
type Invocation struct {
	Argv []string `json:"argv"`
	Env  []string `json:"env"`
}

// Invocations reads every recorded run.
func (f Fake) Invocations(t testing.TB) []Invocation {
	t.Helper()
	raw, err := os.ReadFile(f.RecordPath)
	if err != nil {
		return nil
	}
	var out []Invocation
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var inv Invocation
		if err := json.Unmarshal([]byte(line), &inv); err != nil {
			t.Fatal(err)
		}
		out = append(out, inv)
	}
	return out
}

func here() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Dir(file)
}

// FixtureDir returns a fixture environment directory by name ("dev").
func FixtureDir(name string) string { return filepath.Join(here(), "fixtures", name) }

// Build compiles the fake for a fixture directory into dir.
func Build(t testing.TB, dir, fixture string) Fake {
	t.Helper()
	bin := filepath.Join(dir, "pingcli-terraformer")
	rec := filepath.Join(dir, "record.jsonl")
	ld := "-X main.fixtureDir=" + fixture + " -X main.recordPath=" + rec + " -X main.version=" + Version
	cmd := exec.Command("go", "build", "-o", bin, "-ldflags", ld, "./faketerraformer")
	cmd.Dir = here()
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fake terraformer: %v\n%s", err, out)
	}
	b, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return Fake{Path: bin, SHA256: hex.EncodeToString(sum[:]), Version: Version, RecordPath: rec}
}
