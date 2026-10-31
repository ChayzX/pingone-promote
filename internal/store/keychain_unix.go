//go:build windows || linux

package store

import (
	"errors"
	"fmt"
	"runtime"

	"github.com/zalando/go-keyring"
)

func keyringProviderName() string {
	if runtime.GOOS == "windows" {
		return "Windows Credential Manager"
	}
	return "Linux Secret Service"
}

func keyringActionHint() string {
	if runtime.GOOS == "windows" {
		return "Check that the current Windows account can use Credential Manager."
	}
	return "Start and unlock GNOME Keyring, KWallet, or another Secret Service provider, then retry."
}

func keychainSet(id, secret string) error {
	if err := keyring.Set(keychainService, id, secret); err != nil {
		return fmt.Errorf("%s could not store the credential. %s: %w", keyringProviderName(), keyringActionHint(), err)
	}
	return nil
}

func keychainGet(id string) (string, error) {
	secret, err := keyring.Get(keychainService, id)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", errCredentialNotFound
	}
	if err != nil {
		return "", fmt.Errorf("%s could not read the credential. %s: %w", keyringProviderName(), keyringActionHint(), err)
	}
	return secret, nil
}

func keychainDelete(id string) error {
	err := keyring.Delete(keychainService, id)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s could not remove the credential. %s: %w", keyringProviderName(), keyringActionHint(), err)
	}
	return nil
}
