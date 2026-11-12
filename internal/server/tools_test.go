package server

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func fixtureExecutable(t *testing.T, version string) (string, string) {
	t.Helper()
	raw := []byte("#!/bin/sh\nprintf '%s\\n' '" + version + "'\n")
	p := filepath.Join(t.TempDir(), "tool")
	if err := os.WriteFile(p, raw, 0700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return p, hex.EncodeToString(sum[:])
}

func TestPrepareToolsPinsPrivateCopies(t *testing.T) {
	var c Config
	c.Tools.Terraform, c.Tools.TerraformSHA256 = fixtureExecutable(t, "Terraform v1.16.7")
	c.Tools.TerraformVersion = "1.16.7"
	c.Tools.Terraformer, c.Tools.TerraformerSHA256 = fixtureExecutable(t, "pingcli-terraformer version 0.6.0 (commit: fixture)")
	c.Tools.TerraformerVersion = "0.6.0"
	pinned, cleanup, err := PrepareTools(c)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if pinned.Tools.Terraform == c.Tools.Terraform || pinned.Tools.Terraformer == c.Tools.Terraformer {
		t.Fatal("tools were not privately copied")
	}
	if err := os.WriteFile(c.Tools.Terraform, []byte("tampered"), 0700); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(pinned.Tools.Terraform); err != nil || string(raw) == "tampered" {
		t.Fatal("pinned executable changed with original")
	}
	dir := filepath.Dir(pinned.Tools.Terraform)
	fi, err := os.Stat(dir)
	if err != nil || fi.Mode().Perm() != 0700 {
		t.Fatal("tool directory is not private")
	}
	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("tool copies survive cleanup")
	}
}

func TestPrepareToolsRejectsChecksumAndVersion(t *testing.T) {
	for _, kind := range []string{"checksum", "version"} {
		t.Run(kind, func(t *testing.T) {
			var c Config
			c.Tools.Terraform, c.Tools.TerraformSHA256 = fixtureExecutable(t, "Terraform v1.16.7")
			c.Tools.TerraformVersion = "1.16.7"
			c.Tools.Terraformer, c.Tools.TerraformerSHA256 = fixtureExecutable(t, "pingcli-terraformer version 0.6.0 (commit: fixture)")
			c.Tools.TerraformerVersion = "0.6.0"
			if kind == "checksum" {
				c.Tools.TerraformSHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
			} else {
				c.Tools.TerraformVersion = "1.16.6"
			}
			_, cleanup, err := PrepareTools(c)
			if cleanup != nil {
				cleanup()
			}
			if err == nil {
				t.Fatal("unverified executable accepted")
			}
		})
	}
}

func TestPrepareToolsPingCLIFirstRunBanner(t *testing.T) {
	var c Config
	c.Tools.Terraform, c.Tools.TerraformSHA256 = fixtureExecutable(t, "Terraform v1.16.7")
	c.Tools.TerraformVersion = "1.16.7"
	c.Tools.Terraformer, c.Tools.TerraformerSHA256 = fixtureExecutable(t, "pingcli-terraformer version 0.6.0 (commit: fixture)")
	c.Tools.TerraformerVersion = "0.6.0"
	c.Tools.PingCLI, c.Tools.PingCLISHA256 = fixtureExecutable(t, "Ping CLI configuration file does not exist.\nCreating new Ping CLI configuration file.\npingcli version 0.8.4 (commit: fixture)")
	c.Tools.PingCLIVersion = "0.8.4"
	_, cleanup, err := PrepareTools(c)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestToolVersionRequiresToolIdentity(t *testing.T) {
	for _, output := range []string{"setup version 0.8.4", "pingcli version 0.8.40", "pingcli version 0.8.3\nwarning 0.8.4"} {
		if toolVersionMatches(output, "pingcli version ", "0.8.4") {
			t.Fatal("unrelated or wrong version accepted")
		}
	}
}
