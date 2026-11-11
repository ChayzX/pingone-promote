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
