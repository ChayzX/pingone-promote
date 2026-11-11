package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"pingone-promote/internal/server"
)

func runServe(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configFile := flags.String("config", "", "non-secret server configuration JSON file (required)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return exitUsage
	}
	if *configFile == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "serve requires --config FILE")
		return exitUsage
	}
	config, err := server.LoadConfig(*configFile)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	secrets, err := server.LoadSecrets(config.SecretsFile)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	config, cleanup, err := server.PrepareTools(config)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer cleanup()
	app, err := server.New(config, secrets, server.Dependencies{})
	if err != nil {
		fmt.Fprintln(stderr, "cannot start the hosted service; check configuration and GitLab access")
		return 1
	}
	defer app.Close()
	listener, err := net.Listen("tcp", config.Listen)
	if err != nil {
		fmt.Fprintln(stderr, "cannot listen on the configured server address")
		return 1
	}
	service := &http.Server{Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	done := make(chan error, 1)
	go func() {
		if config.TLSCertFile != "" {
			done <- service.ServeTLS(listener, config.TLSCertFile, config.TLSKeyFile)
		} else {
			done <- service.Serve(listener)
		}
	}()
	fmt.Fprintf(stdout, "PingOne Promote listening on %s (names are unverified)\n", listener.Addr())
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = service.Shutdown(shutdown)
		return 0
	case err := <-done:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(stderr, "the HTTP service stopped unexpectedly")
			return 1
		}
		return 0
	}
}
