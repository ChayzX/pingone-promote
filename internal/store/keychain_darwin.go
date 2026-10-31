//go:build darwin

package store

import (
	"io"
	"os/exec"
	"strings"
)

// Apple's interactive loop runs each input line and returns its final command
// status at EOF. The password and command arrive on stdin, never process args.
func keychainSet(id, secret string) error {
	input, err := buildKeychainAddInput(id, secret)
	if err != nil {
		return err
	}
	cmd := exec.Command("/usr/bin/security", "-i")
	cmd.Stdin = strings.NewReader(input)
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return safeCredentialProviderFailure("macOS Keychain", "store", "Unlock your login keychain and approve any access prompt, then retry.", err)
	}
	return nil
}

func keychainGet(id string) (string, error) {
	input := "find-generic-password -a " + quoteSecurityArg(id) + " -s " + quoteSecurityArg(keychainService) + " -w\n"
	cmd := exec.Command("/usr/bin/security", "-i")
	cmd.Stdin = strings.NewReader(input)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if strings.Contains(stdout.String()+stderr.String(), "could not be found") {
			return "", errCredentialNotFound
		}
		return "", safeCredentialProviderFailure("macOS Keychain", "read", "Unlock your login keychain and approve any access prompt, then retry.", err)
	}
	return strings.TrimSuffix(stdout.String(), "\n"), nil
}

func keychainDelete(id string) error {
	input := "delete-generic-password -a " + quoteSecurityArg(id) + " -s " + quoteSecurityArg(keychainService) + "\n"
	cmd := exec.Command("/usr/bin/security", "-i")
	cmd.Stdin = strings.NewReader(input)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if strings.Contains(stdout.String()+stderr.String(), "could not be found") {
			return nil
		}
		return safeCredentialProviderFailure("macOS Keychain", "remove", "Unlock your login keychain and approve any access prompt, then retry.", err)
	}
	return nil
}
