package gitlab

import (
	"context"
	"errors"
	"testing"
)

// An existing protected branch under the request prefix is never written.
func TestHostedCommitRefusesProtectedRequestBranch(t *testing.T) {
	f, h, bc := hostedSetup(t)
	f.CreateBranch("CIAM-req-1", "main", nil)
	f.Protected["CIAM-req-1"] = true
	before := len(f.CommitOrder)
	_, err := h.CommitToBranch(context.Background(), bc)
	var pre *PreflightError
	if !errors.As(err, &pre) {
		t.Fatalf("want a preflight refusal, got %v", err)
	}
	if len(f.CommitOrder) != before || !f.HasBranch("CIAM-req-1") {
		t.Fatal("the protected branch was written or deleted")
	}
}
