package store

import (
	"strings"
	"testing"
)

func TestQuoteSecurityArgMatchesAppleInteractiveTokenizerEscapes(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", "''"},
		{"simple", "'simple'"},
		{"with spaces", "'with spaces'"},
		{"single'quote", "'single'\\''quote'"},
		{"double\"quote", "'double\"quote'"},
		{"back\\slash", "'back\\slash'"},
	}
	for _, tt := range tests {
		if got := quoteSecurityArg(tt.in); got != tt.want {
			t.Errorf("quoteSecurityArg(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestBuildKeychainAddInputRejectsControlCharsWithoutEchoingSecret(t *testing.T) {
	for _, secret := range []string{"line\nbreak", "carriage\rreturn", "tab\tvalue", "nul\x00value", "delete\x7fvalue"} {
		input, err := buildKeychainAddInput("profile-1", secret)
		if err == nil {
			t.Errorf("buildKeychainAddInput accepted %q", secret)
			continue
		}
		if strings.Contains(input, secret) || strings.Contains(err.Error(), secret) {
			t.Errorf("rejected secret leaked in result: input %q, error %q", input, err)
		}
	}
}

func TestBuildKeychainAddInputRejectsOverlongLineWithoutEchoingSecret(t *testing.T) {
	secret := strings.Repeat("x", maxSecurityInteractiveLine)
	input, err := buildKeychainAddInput("profile-1", secret)
	if err == nil {
		t.Fatal("expected overlong interactive command to be rejected")
	}
	if strings.Contains(input, secret) || strings.Contains(err.Error(), secret) {
		t.Fatalf("overlong secret leaked in result: input length %d, error %q", len(input), err)
	}
}

func TestBuildKeychainAddInputQuotesSecretOnlyInStdinText(t *testing.T) {
	input, err := buildKeychainAddInput("profile-1", "has spaces and ' quotes")
	if err != nil {
		t.Fatal(err)
	}
	want := "add-generic-password -U -a 'profile-1' -s 'pingone-promote-worker-client' -w 'has spaces and '\\'' quotes'\n"
	if input != want {
		t.Fatalf("input = %q, want %q", input, want)
	}
}
