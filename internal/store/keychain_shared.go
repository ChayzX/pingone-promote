package store

import (
	"errors"
	"strings"
	"unicode"
)

const maxSecurityInteractiveLine = 4095 // security's source uses a 4096-byte line buffer.
const keychainService = "pingone-promote-worker-client"

var errCredentialNotFound = errors.New("OS keyring credential not found")

// quoteSecurityArg quotes one argument for Apple's security -i command parser.
// That parser accepts single/double quoted arguments and backslash escapes.
func quoteSecurityArg(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func buildKeychainAddInput(id, secret string) (string, error) {
	if hasControlCharacter(secret) {
		return "", errors.New("Keychain credentials cannot contain control characters")
	}
	input := "add-generic-password -U -a " + quoteSecurityArg(id) + " -s " + quoteSecurityArg(keychainService) + " -w " + quoteSecurityArg(secret) + "\n"
	if len(input)-1 > maxSecurityInteractiveLine {
		return "", errors.New("Keychain credential exceeds the supported command length")
	}
	return input, nil
}

func hasControlCharacter(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return true
		}
	}
	return false
}
