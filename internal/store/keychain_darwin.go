//go:build darwin

package store

import (
	"fmt"
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
		return fmt.Errorf("store credential in macOS Keychain: %w", err)
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
		return "", fmt.Errorf("read credential from macOS Keychain: %w", err)
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
		return fmt.Errorf("delete credential from macOS Keychain: %w", err)
	}
	return nil
}
