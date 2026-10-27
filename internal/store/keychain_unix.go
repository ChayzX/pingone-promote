//go:build !darwin

package store

import "errors"

func keychainSet(id, secret string) error {
	return errors.New("macOS Keychain credentials are only available on macOS")
}
func keychainGet(id string) (string, error) {
	return "", errors.New("macOS Keychain credentials are only available on macOS")
}
func keychainDelete(id string) error { return nil }
