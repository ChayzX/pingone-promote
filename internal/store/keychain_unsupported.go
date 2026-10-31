//go:build !darwin && !linux

package store

import "errors"

var errUnsupportedCredentialVault = errors.New("OS credential vault is unsupported on this platform")

func unsupportedCredentialVaultError() error {
	return safeCredentialProviderFailure("OS credential vault", "access", "Persistent OS credential storage is supported only on macOS and Linux. Use Session only or Environment variable on this platform.", errUnsupportedCredentialVault)
}

func keychainSet(id, secret string) error {
	return unsupportedCredentialVaultError()
}
func keychainGet(id string) (string, error) {
	return "", unsupportedCredentialVaultError()
}
func keychainDelete(id string) error {
	return unsupportedCredentialVaultError()
}
