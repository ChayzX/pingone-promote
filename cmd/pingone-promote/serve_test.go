package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestServeHelpAndRequiredConfig(t *testing.T) {
	var out, errout bytes.Buffer
	code := dispatch(context.Background(), []string{"serve", "--help"}, func(string) string { return "" }, &out, &errout)
	if code != 0 || !strings.Contains(out.String()+errout.String(), "config") {
		t.Fatalf("serve help failed: code %d", code)
	}
	out.Reset()
	errout.Reset()
	code = dispatch(context.Background(), []string{"serve"}, func(string) string { return "" }, &out, &errout)
	if code != exitUsage || !strings.Contains(errout.String(), "config") {
		t.Fatalf("missing config not rejected: code %d", code)
	}
}
