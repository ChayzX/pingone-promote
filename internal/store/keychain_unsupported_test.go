//go:build !darwin && !linux

package store

import (
	"strings"
	"testing"
)

func TestUnsupportedPlatformKeyringFailsClosed(t *testing.T) {
	if err := keychainSet("synthetic-profile", "synthetic-secret"); err == nil || !strings.Contains(err.Error(), "supported only on macOS and Linux") {
		t.Fatalf("keychainSet error = %v, want explicit unsupported-platform error", err)
	}
	if _, err := keychainGet("synthetic-profile"); err == nil || !strings.Contains(err.Error(), "supported only on macOS and Linux") {
		t.Fatalf("keychainGet error = %v, want explicit unsupported-platform error", err)
	}
	if err := keychainDelete("synthetic-profile"); err == nil || !strings.Contains(err.Error(), "supported only on macOS and Linux") {
		t.Fatalf("keychainDelete error = %v, want explicit unsupported-platform error", err)
	}
}
