// PingOne Promote is a command-line tool for the DaVinci-triggered
// Terraform promotion flow: check, export and request.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"pingone-promote/internal/buildinfo"
)

const usageText = `Usage: pingone-promote <command> [arguments]

Commands:
  serve --config FILE host the source-only promotion service
  check --root DIR   validate a config repository root offline
  export             export, neutralize and merge a source environment
  request            run the CI request job (see docs/gitlab-ci-integration.md)

  --version          print the version and exit

Run a command with --help for its flags.
`

const exitUsage = 2

// dispatch runs one invocation and returns its exit status.
func dispatch(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return exitUsage
	}
	switch args[0] {
	case "serve":
		return runServe(ctx, args[1:], stdout, stderr)
	case "check":
		return runCheck(ctx, args[1:], stdout, stderr)
	case "request":
		return runRequest(ctx, args[1:], getenv, stdout, stderr)
	case "export":
		return runExport(ctx, args[1:], getenv, stdout, stderr)
	case "--version", "-version", "version":
		fmt.Fprintf(stdout, "pingone-promote %s\n", buildinfo.Version)
		return 0
	case "-h", "--help", "-help", "help":
		fmt.Fprint(stdout, usageText)
		return 0
	}
	fmt.Fprintf(stderr, "pingone-promote: unknown command %q\n\n%s", args[0], usageText)
	return exitUsage
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	os.Exit(dispatch(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}
