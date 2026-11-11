package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServeUsage(t *testing.T) {
	if code, _, errs := run("serve"); code != 2 || !strings.Contains(errs, "--config is required") {
		t.Fatalf("code=%d err=%q", code, errs)
	}
	if code, out, _ := run("serve", "--help"); code != 0 || !strings.Contains(out, "--check-config") {
		t.Fatalf("help: code=%d", code)
	}
}

func TestServeCheckConfigRefusesBadConfig(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.json")
	if code, _, errs := run("serve", "--config", missing, "--check-config"); code != 1 || !strings.Contains(errs, "cannot read") {
		t.Fatalf("code=%d err=%q", code, errs)
	}
	bad := filepath.Join(dir, "bad.json")
	os.WriteFile(bad, []byte(`{"gitlab":{"token":"fixture-private-value"}}`), 0o600)
	code, out, errs := run("serve", "--config", bad, "--check-config")
	if code != 1 || strings.Contains(out+errs, "fixture-private-value") {
		t.Fatalf("code=%d out=%q err=%q", code, out, errs)
	}
}
