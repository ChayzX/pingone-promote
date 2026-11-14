package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"pingone-promote/internal/multiexport"
	"syscall"

	"pingone-promote/internal/exporter"
	"pingone-promote/internal/request"
	"pingone-promote/internal/server"
)

const serveUsage = `Usage: pingone-promote serve --config FILE [--check-config] [--debug]

Runs the hosted server (ADR 0018): server-rendered pages to search a source
environment, preview a request and commit it to a GitLab request branch,
optionally opening a merge request. It never plans or applies.

The configuration file (JSON) holds no secrets; it names the secrets file
(mode 0600 or 0400) that holds the read-only worker credentials and the
GitLab token. Neither is ever printed.

  --config FILE    the configuration file
  --check-config   check the configuration, the secrets file and the pinned
                   tools, then exit (0 when everything is usable)
  --debug          also log the detail of failures whose text may quote
                   tenant content (an export tool's output), redacted
`

// newExporter builds the exporter the server uses. It is a variable so the
// core PingOne exporter can replace the terraformer runner without
// changing the server: anything implementing request.Exporter works.
var newExporter = func(cfg server.Config) request.Exporter {
	// DaVinci through the pinned pingcli-terraformer, every other PingOne
	// type through the native read-only exporter (ADR 0019).
	return &multiexport.Exporter{DaVinci: &exporter.Runner{
		Config:  exporter.Config{Version: cfg.Tools.TerraformerVersion, SHA256: cfg.Tools.TerraformerSHA256, Path: cfg.Tools.Terraformer},
		Network: exporter.Network{HTTPSProxy: os.Getenv("HTTPS_PROXY"), NoProxy: os.Getenv("NO_PROXY"), CAFile: os.Getenv("SSL_CERT_FILE")},
	}}
}

// verifier is implemented by exporters that can check their pinned tool.
type verifier interface {
	Verify(ctx context.Context) error
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// checkTools verifies the pinned tools.
func checkTools(ctx context.Context, cfg server.Config, exp request.Exporter) error {
	sum, err := fileSHA256(cfg.Tools.Terraform)
	if err != nil {
		return errors.New("tools.terraform cannot be read")
	}
	if sum != cfg.Tools.TerraformSHA256 {
		return errors.New("tools.terraform does not match tools.terraformSha256")
	}
	if v, ok := exp.(verifier); ok {
		if err := v.Verify(ctx); err != nil {
			return fmt.Errorf("the exporter tool failed its check: %w", err)
		}
	}
	return nil
}

func runServe(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfgPath := fs.String("config", "", "")
	check := fs.Bool("check-config", false, "")
	debug := fs.Bool("debug", false, "")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, serveUsage)
			return 0
		}
		fmt.Fprintln(stderr, "pingone-promote serve:", err)
		fmt.Fprint(stderr, serveUsage)
		return exitUsage
	}
	if *cfgPath == "" || fs.NArg() > 0 {
		fmt.Fprint(stderr, "pingone-promote serve: --config is required\n"+serveUsage)
		return exitUsage
	}
	cfg, err := server.LoadConfig(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "pingone-promote serve:", err)
		return 1
	}
	secrets, err := server.LoadSecrets(cfg.SecretsFile)
	if err != nil {
		fmt.Fprintln(stderr, "pingone-promote serve:", err)
		return 1
	}
	exp := newExporter(cfg)
	if err := checkTools(ctx, cfg, exp); err != nil {
		fmt.Fprintln(stderr, "pingone-promote serve:", err)
		return 1
	}
	if *check {
		fmt.Fprintln(stdout, "configuration, secrets file and tools are usable")
		return 0
	}
	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))
	s, err := server.New(server.Options{Config: cfg, Secrets: secrets, Exporter: exp, Logger: logger})
	if err != nil {
		fmt.Fprintln(stderr, "pingone-promote serve:", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := s.Run(ctx, nil); err != nil {
		fmt.Fprintln(stderr, "pingone-promote serve:", err)
		return 1
	}
	return 0
}
