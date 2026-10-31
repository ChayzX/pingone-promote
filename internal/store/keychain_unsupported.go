//go:build !darwin && !windows && !linux

package store

import "errors"

func keychainSet(id, secret string) error {
	return errors.New("OS keyring storage is supported only on macOS, Windows, and Linux")
}
func keychainGet(id string) (string, error) {
	return "", errors.New("OS keyring storage is supported only on macOS, Windows, and Linux")
}
func keychainDelete(id string) error {
	return errors.New("OS keyring storage is supported only on macOS, Windows, and Linux")
}
