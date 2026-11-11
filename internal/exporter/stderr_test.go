package exporter

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The client secret is removed from stderr before the 64 KiB cut, so a cut
// through the secret cannot leave a prefix of it in the error.
func TestStderrRedactedBeforeCapping(t *testing.T) {
	f := getFake(t)
	c := creds
	c.ClientSecret = "stderr-flood-0123456789abcdef"
	_, err := runner(f).ListResources(context.Background(), c)
	var ex *ExecError
	if !errors.As(err, &ex) {
		t.Fatalf("want ExecError, got %v", err)
	}
	if strings.Contains(ex.Stderr, "stderr-fl") || strings.Contains(err.Error(), "stderr-fl") {
		t.Fatal("a prefix of the client secret survived the stderr cut")
	}
	if len(ex.Stderr) > stderrMax {
		t.Fatalf("stderr kept %d bytes", len(ex.Stderr))
	}
}
