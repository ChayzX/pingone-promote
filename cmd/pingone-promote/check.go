package main

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// Exit codes for `pingone-promote check`.
const (
	checkOK       = 0 // the root is valid
	checkProblems = 1 // the root has problems
	checkUsage    = 2 // bad arguments, unreadable root, or formatter unavailable
)

// fmtFlag is a boolean-or-path flag: --terraform-fmt or --terraform-fmt=PATH.
type fmtFlag struct {
	enabled bool
	path    string
}

func (f *fmtFlag) String() string   { return f.path }
func (f *fmtFlag) IsBoolFlag() bool { return true }
func (f *fmtFlag) Set(v string) error {
	switch v {
	case "true":
		f.enabled, f.path = true, ""
	case "false":
		f.enabled, f.path = false, ""
	case "":
		return errors.New("empty formatter path")
	default:
		f.enabled, f.path = true, v
	}
	return nil
}

// runCheck validates a v2 config repository root. The earlier single-file
// tfvars mode is retired, so --root is required.
func runCheck(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if !hasRootFlag(args) && !hasHelpFlag(args) {
		fmt.Fprintln(stderr, "pingone-promote check: --root DIR is required")
		fmt.Fprint(stderr, rootCheckUsage)
		return checkUsage
	}
	return runRootCheck(ctx, args, stdout, stderr)
}

func hasHelpFlag(args []string) bool {
	for _, a := range args {
		switch a {
		case "-h", "-help", "--help":
			return true
		}
	}
	return false
}
