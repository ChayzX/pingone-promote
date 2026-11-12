package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type serveReadyWriter struct{ ready chan string }

func (w serveReadyWriter) Write(p []byte) (int, error) {
	select {
	case w.ready <- string(p):
	default:
	}
	return len(p), nil
}

// Exercise the actual serve command and TCP lifecycle without a tenant, an
// external GitLab, or a credential with management access. Exporter tool fakes
// answer only --version; no export is requested by this startup smoke test.
func TestServeCommandHTTPStartupAndGracefulShutdown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows is not a supported hosted platform")
	}
	dir := t.TempDir()
	tool := func(name, version string) (string, string) {
		t.Helper()
		p := filepath.Join(dir, name)
		raw := []byte("#!/bin/sh\nprintf '%s\\n' '" + version + "'\n")
		if err := os.WriteFile(p, raw, 0700); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		return p, hex.EncodeToString(sum[:])
	}
	terraform, tfSum := tool("terraform", "Terraform v1.16.7")
	terraformer, exportSum := tool("pingcli-terraformer", "pingcli-terraformer version 0.6.0 (commit: fixture)")
	provider := filepath.Join(dir, "provider.zip")
	archive := []byte("fixture archive not executed by startup")
	if err := os.WriteFile(provider, archive, 0600); err != nil {
		t.Fatal(err)
	}
	archiveSum := sha256.Sum256(archive)
	secrets := filepath.Join(dir, "secrets.env")
	if err := os.WriteFile(secrets, []byte("PINGONE_PROMOTE_WORKER_CLIENT_ID=22222222-2222-4222-8222-222222222222\nPINGONE_PROMOTE_WORKER_CLIENT_SECRET=fixture-startup-worker\nPINGONE_PROMOTE_GITLAB_TOKEN=fixture-startup-gitlab\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{
		"listen": "127.0.0.1:0", "secretsFile": secrets, "sources": []string{"dev"}, "rollout": []string{"qa"}, "branchPrefix": "CIAM-",
		"gitlab":  map[string]any{"baseUrl": "https://gitlab.example.invalid", "projectId": "123"},
		"pingone": map[string]any{"regionCode": "NA", "workerEnvironmentId": "11111111-1111-4111-8111-111111111111", "services": []string{"base", "sso"}},
		"tools":   map[string]any{"terraform": terraform, "terraformVersion": "1.16.7", "terraformSha256": tfSum, "terraformer": terraformer, "terraformerVersion": "0.6.0", "terraformerSha256": exportSum, "providerZip": provider, "providerVersion": "1.24.0", "providerSha256": hex.EncodeToString(archiveSum[:])},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(dir, "config.json")
	if err = os.WriteFile(configFile, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan string, 1)
	done := make(chan int, 1)
	var stderr bytes.Buffer
	go func() {
		done <- dispatch(ctx, []string{"serve", "--config", configFile}, func(string) string { return "" }, serveReadyWriter{ready}, &stderr)
	}()
	stopped := false
	defer func() {
		cancel()
		if !stopped {
			select {
			case <-done:
			case <-time.After(15 * time.Second):
				t.Error("serve did not stop")
			}
		}
	}()
	var line string
	select {
	case line = <-ready:
	case <-done:
		stopped = true
		t.Fatal("serve exited before listening")
	case <-time.After(30 * time.Second):
		t.Fatal("serve startup timed out")
	}
	prefix := "PingOne Promote listening on "
	if !strings.HasPrefix(line, prefix) {
		t.Fatal("unexpected startup status")
	}
	authority := strings.Fields(strings.TrimPrefix(line, prefix))[0]
	host, port, err := net.SplitHostPort(authority)
	if err != nil || host != "127.0.0.1" || port == "0" {
		t.Fatal("serve did not bind its loopback listener")
	}
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: 5 * time.Second, Transport: transport}
	for _, host := range []string{authority, "untrusted.example"} {
		req, err := http.NewRequest("GET", "http://"+authority+"/", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = host
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal("serve HTTP request failed")
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if host == authority {
			if resp.StatusCode != 200 || !strings.Contains(string(body), `name="source"`) || resp.Header.Get("Content-Security-Policy") == "" {
				t.Fatal("serve homepage or security headers unavailable")
			}
		} else if resp.StatusCode != 400 {
			t.Fatal("serve accepted an untrusted Host")
		}
		if strings.Contains(string(body), "fixture-startup-") {
			t.Fatal("startup credentials appeared on a page")
		}
	}
	cancel()
	select {
	case code := <-done:
		stopped = true
		if code != 0 {
			t.Fatal("serve shutdown failed")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve shutdown timed out")
	}
	if strings.Contains(stderr.String(), "fixture-startup-") {
		t.Fatal("startup credentials appeared in diagnostics")
	}
}
