package server

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pingone-promote/internal/exporter"
	"pingone-promote/internal/gitlab/gitlabtest"
	"pingone-promote/internal/multiexport"
)

// readTree reads every file under dir.
func readTree(dir string) map[string]string {
	out := map[string]string{}
	_ = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		b, _ := os.ReadFile(p)
		out[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	return out
}

// TestLiveBrowserServer serves the real server against a real source
// environment (read-only) and a fake GitLab, for testing in a browser:
//
//	PINGONE_PROMOTE_DEMO_ADDR          listen address (127.0.0.1:18090)
//	PINGONE_PROMOTE_LIVE_ENV           credentials file (see promote.TestLiveTenant)
//	PINGONE_PROMOTE_REAL_TERRAFORMER   pingcli-terraformer v0.6.0
//	PINGONE_PROMOTE_DEMO_LOG           server log file (optional)
//	PINGONE_PROMOTE_DEMO_STOP          a file whose creation stops the server
//	PINGONE_PROMOTE_DEMO_REPO          a directory keeping the fake GitLab's main
//	                                   branch across restarts (optional)
//	PINGONE_PROMOTE_DEMO_MERGE         a file whose creation merges every open
//	                                   request branch into main, as a reviewer
//	                                   merging the MR would (optional)
//
// Nothing reaches a real GitLab; PingOne is only read.
func TestLiveBrowserServer(t *testing.T) {
	addr, envFile := os.Getenv("PINGONE_PROMOTE_DEMO_ADDR"), os.Getenv("PINGONE_PROMOTE_LIVE_ENV")
	if addr == "" || envFile == "" {
		t.Skip("set PINGONE_PROMOTE_DEMO_ADDR and PINGONE_PROMOTE_LIVE_ENV to serve against a real environment")
	}
	vals := map[string]string{}
	f, err := os.Open(envFile)
	if err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "="); ok {
			vals[k] = v
		}
	}
	f.Close()
	repoDir := os.Getenv("PINGONE_PROMOTE_DEMO_REPO")
	start := repoFiles(t)
	if repoDir != "" {
		if saved := readTree(repoDir); len(saved) > 0 {
			start = saved
		}
	}
	gl := gitlabtest.New(t, start)
	save := func() {
		if repoDir == "" {
			return
		}
		_ = os.RemoveAll(repoDir)
		for p, c := range gl.Files(gl.DefaultBranch) {
			full := filepath.Join(repoDir, filepath.FromSlash(p))
			_ = os.MkdirAll(filepath.Dir(full), 0o700)
			_ = os.WriteFile(full, []byte(c), 0o600)
		}
	}
	defer save()
	st := gitlabtest.NewStatus(gl)
	cfg := testConfig(t, gl)
	host, _, _ := net.SplitHostPort(addr)
	cfg.AllowedHosts = append(cfg.AllowedHosts, addr, host, "localhost")
	cfg.PingOne.RegionCode = vals["PINGONE_PROMOTE_REGION_CODE"]
	cfg.PingOne.WorkerEnvironmentID = vals["PINGONE_PROMOTE_WORKER_ENVIRONMENT_ID"]
	if src := vals["PINGONE_PROMOTE_SOURCE_ENVIRONMENT_ID"]; src != "" {
		cfg.SourceEnvironmentIDs = map[string]string{"dev": src}
	}
	exp := &multiexport.Exporter{}
	if bin := os.Getenv("PINGONE_PROMOTE_REAL_TERRAFORMER"); bin != "" {
		b, err := os.Open(bin)
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.New()
		_, _ = io.Copy(h, b)
		b.Close()
		exp.DaVinci = &exporter.Runner{Config: exporter.Config{Path: bin, SHA256: hex.EncodeToString(h.Sum(nil)), Version: "0.6.0"}}
	}
	var logOut io.Writer = io.Discard
	if p := os.Getenv("PINGONE_PROMOTE_DEMO_LOG"); p != "" {
		lf, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer lf.Close()
		logOut = lf
	}
	s, err := New(Options{Config: cfg,
		Secrets:  Secrets{WorkerClientID: vals["PINGONE_PROMOTE_WORKER_CLIENT_ID"], WorkerClientSecret: vals["PINGONE_PROMOTE_WORKER_CLIENT_SECRET"], GitLabToken: gitlabtest.Token},
		Exporter: exp, Logger: slog.New(slog.NewTextHandler(logOut, &slog.HandlerOptions{Level: slog.LevelDebug})), GitLabHTTP: st.Client(), Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	defer srv.Close()
	t.Logf("live server on http://%s (fake GitLab at %s)", addr, gl.BaseURL())
	stop, merge := os.Getenv("PINGONE_PROMOTE_DEMO_STOP"), os.Getenv("PINGONE_PROMOTE_DEMO_MERGE")
	for deadline := time.Now().Add(4 * time.Hour); time.Now().Before(deadline); time.Sleep(time.Second) {
		if merge != "" {
			if _, err := os.Stat(merge); err == nil {
				for b := range gl.Branches() {
					if strings.HasPrefix(b, "pingone-promote/") {
						gl.Merge(b)
						t.Logf("merged %s into %s", b, gl.DefaultBranch)
					}
				}
				save()
				_ = os.Remove(merge)
			}
		}
		if stop != "" {
			if _, err := os.Stat(stop); err == nil {
				return
			}
		}
	}
}
