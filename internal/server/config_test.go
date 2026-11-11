package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixtureSecrets = "PINGONE_PROMOTE_WORKER_CLIENT_ID=fixture-id\nPINGONE_PROMOTE_WORKER_CLIENT_SECRET=fixture-value\nPINGONE_PROMOTE_GITLAB_TOKEN=fixture-token\n"

func TestSecretsFilePrivate(t *testing.T) {
	for _, mode := range []os.FileMode{0600, 0400, 0640, 0440, 0644, 0660, 0700} {
		t.Run(mode.String(), func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "secrets")
			if err := os.WriteFile(p, []byte(fixtureSecrets), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(p, mode); err != nil {
				t.Fatal(err)
			}
			s, err := LoadSecrets(p)
			wantOK := mode == 0600 || mode == 0400
			if (err == nil) != wantOK {
				t.Fatalf("mode %04o accepted=%v, want=%v", mode, err == nil, wantOK)
			}
			if wantOK && (s.WorkerClientID != "fixture-id" || s.WorkerClientSecret != "fixture-value" || s.GitLabToken != "fixture-token") {
				t.Fatal("values were not loaded")
			}
		})
	}
}

func TestSecretsRejectDuplicatesAndDoNotEchoInput(t *testing.T) {
	for _, suffix := range []string{"PINGONE_PROMOTE_GITLAB_TOKEN=another-fixture-value\n", "unexpected-fixture-name=another-fixture-value\n", "malformed-fixture-value\n"} {
		p := filepath.Join(t.TempDir(), "secrets")
		if err := os.WriteFile(p, []byte(fixtureSecrets+suffix), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := LoadSecrets(p)
		if err == nil {
			t.Fatal("invalid file accepted")
		}
		for _, value := range []string{"fixture-value", "fixture-token", "unexpected-fixture-name"} {
			if strings.Contains(err.Error(), value) {
				t.Fatal("error echoes untrusted input")
			}
		}
	}
}

func TestConfigRejectTrailingDataAndSecretInput(t *testing.T) {
	for _, raw := range []string{`{"secretsFile":"x"} {}`, `{"gitlab":{"token":"fixture-private"}}`, `{"workerClientSecret":"fixture-private"}`} {
		p := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(p, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := LoadConfig(p)
		if err == nil {
			t.Fatal("invalid config accepted")
		}
		if strings.Contains(err.Error(), "fixture-private") {
			t.Fatal("config error leaked value")
		}
	}
}

func hostedConfigFixture() Config {
	var c Config
	c.SecretsFile = "fixture.env"
	c.GitLab.BaseURL = "https://gitlab.example.test"
	c.GitLab.ProjectID = "123"
	c.PingOne.RegionCode = "NA"
	c.PingOne.WorkerEnvironmentID = "11111111-1111-4111-8111-111111111111"
	c.Sources = []string{"dev"}
	c.Rollout = []string{"qa"}
	c.Tools.Terraform = "fixture-terraform"
	c.Tools.TerraformSHA256 = strings.Repeat("a", 64)
	c.Tools.Terraformer = "fixture-terraformer"
	c.Tools.TerraformerVersion = "0.6.0"
	c.Tools.TerraformerSHA256 = strings.Repeat("b", 64)
	c.Tools.ProviderZip = "fixture-provider.zip"
	c.Tools.ProviderVersion = "1.24.0"
	c.Tools.ProviderSHA256 = strings.Repeat("c", 64)
	return c
}
func TestHostedConfigRequiresProviderAndKnownServices(t *testing.T) {
	c := hostedConfigFixture()
	if err := c.check(); err != nil {
		t.Fatal(err)
	}
	c.Tools.ProviderZip = ""
	if err := c.check(); err == nil {
		t.Fatal("missing provider accepted")
	}
	c = hostedConfigFixture()
	c.PingOne.Services = []string{"users"}
	if err := c.check(); err == nil {
		t.Fatal("unsafe service accepted")
	}
	c = hostedConfigFixture()
	c.PingOne.Services = []string{"base", "sso", "mfa"}
	if err := c.check(); err != nil {
		t.Fatal(err)
	}
}
func TestHostedConfigRejectsMalformedOriginAuthority(t *testing.T) {
	for _, origin := range []string{"https://localhost:", "https://localhost.", "https://localhost:0", "https://localhost#"} {
		c := hostedConfigFixture()
		c.PublicOrigin = origin
		if err := c.check(); err == nil {
			t.Fatalf("origin accepted: %s", origin)
		}
	}
}
