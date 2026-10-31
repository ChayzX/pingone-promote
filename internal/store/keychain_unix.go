//go:build windows || linux

package store

import (
	"errors"
	"github.com/zalando/go-keyring"
)

func keychainSet(id, secret string) error {
	return keyring.Set(keychainService, id, secret)
}

func keychainGet(id string) (string, error) {
	secret, err := keyring.Get(keychainService, id)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", errCredentialNotFound
	}
	return secret, err
}

func keychainDelete(id string) error {
	err := keyring.Delete(keychainService, id)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}
