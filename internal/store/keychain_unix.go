//go:build linux

package store

import (
	"errors"

	"github.com/zalando/go-keyring"
)

func keyringProviderName() string {
	return "Linux Secret Service"
}

func keyringActionHint() string {
	return "Start and unlock GNOME Keyring, KWallet, or another Secret Service provider, then retry."
}

func keychainSet(id, secret string) error {
	if err := keyring.Set(keychainService, id, secret); err != nil {
		return safeCredentialProviderFailure(keyringProviderName(), "store", keyringActionHint(), err)
	}
	return nil
}

func keychainGet(id string) (string, error) {
	secret, err := keyring.Get(keychainService, id)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", errCredentialNotFound
	}
	if err != nil {
		return "", safeCredentialProviderFailure(keyringProviderName(), "read", keyringActionHint(), err)
	}
	return secret, nil
}

func keychainDelete(id string) error {
	err := keyring.Delete(keychainService, id)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	if err != nil {
		return safeCredentialProviderFailure(keyringProviderName(), "remove", keyringActionHint(), err)
	}
	return nil
}
