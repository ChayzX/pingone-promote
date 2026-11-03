package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestCheckRequiresRoot(t *testing.T) {
	var out, errb bytes.Buffer
	code := runCheck(context.Background(), []string{"env.tfvars"}, &out, &errb)
	if code != checkUsage || !strings.Contains(errb.String(), "--root DIR is required") {
		t.Fatalf("code=%d stderr=%q", code, errb.String())
	}
}

func TestCheckHelp(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runCheck(context.Background(), []string{"--help"}, &out, &errb); code != checkOK || !strings.Contains(out.String(), "Usage: pingone-promote check --root") {
		t.Fatalf("code=%d out=%q", code, out.String())
	}
}
