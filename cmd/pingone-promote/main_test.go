package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
)

func run(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := dispatch(context.Background(), args, os.Getenv, &out, &errb)
	return code, out.String(), errb.String()
}

func TestNoSubcommandPrintsUsageAndExits2(t *testing.T) {
	code, out, errs := run()
	if code != 2 || out != "" || !strings.Contains(errs, "Usage: pingone-promote") {
		t.Fatalf("code=%d out=%q err=%q", code, out, errs)
	}
}

func TestUnknownCommandExits2(t *testing.T) {
	// Unknown commands and retired top-level listen flags must not start anything.
	for _, args := range [][]string{{"unknown"}, {"--listen", "127.0.0.1:8787"}} {
		code, _, errs := run(args...)
		if code != 2 || !strings.Contains(errs, "unknown command") {
			t.Fatalf("%v: code=%d err=%q", args, code, errs)
		}
	}
}

func TestVersion(t *testing.T) {
	code, out, _ := run("--version")
	if code != 0 || out != "pingone-promote dev\n" {
		t.Fatalf("code=%d out=%q", code, out)
	}
}
