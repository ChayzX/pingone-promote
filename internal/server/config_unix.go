//go:build !windows

package server

import (
	"errors"
	"os"
	"syscall"
)

// checkSecretsFileInfo enforces the owner-only mode and that the server's
// user owns the file.
func checkSecretsFileInfo(fi os.FileInfo) error {
	if err := checkSecretsMode(fi.Mode()); err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("cannot read the owner of the secrets file")
	}
	if int64(st.Uid) != int64(os.Getuid()) {
		return errors.New("the secrets file must be owned by the user the server runs as; run chown on it, or in a pod set runAsUser and fsGroup to match")
	}
	return nil
}
