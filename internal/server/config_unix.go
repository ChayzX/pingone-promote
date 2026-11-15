//go:build !windows

package server

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// openSecretsFile opens without following a final symbolic link and
// without blocking on a FIFO.
func openSecretsFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}

// checkSecretsFileInfo enforces the owner-only mode and that the server's
// user owns the file. A systemd credential (LoadCredential=) is the one
// exception: systemd hands the service a root-owned copy (mode 0440 with an
// ACL for the service user) inside $CREDENTIALS_DIRECTORY, a private,
// memory-backed directory only this service can reach. That is accepted
// when the file is directly in that directory, the directory and the file
// give nothing to other users, and root owns both.
func checkSecretsFileInfo(path string, fi os.FileInfo) error {
	if ok, err := systemdCredential(path, fi); ok || err != nil {
		return err
	}
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

// credentialsRoot is where systemd places per-service credential
// directories; only root creates directories there. Tests change it.
var credentialsRoot = "/run/credentials/"

// systemdCredential reports whether path is a systemd credential of this
// service; err explains a credential that is not private. The directory
// must be $CREDENTIALS_DIRECTORY, under /run/credentials/, already resolved
// (no symbolic links, no ".."); file and directory may grant nothing beyond
// read (and directory search), and belong to root or the server's own user
// and group.
func systemdCredential(path string, fi os.FileInfo) (bool, error) {
	credDir := os.Getenv("CREDENTIALS_DIRECTORY")
	if credDir == "" || credDir != filepath.Clean(credDir) || !strings.HasPrefix(credDir, credentialsRoot) {
		return false, nil
	}
	if filepath.Dir(path) != credDir {
		return false, nil
	}
	if r, err := filepath.EvalSymlinks(credDir); err != nil || r != credDir {
		return false, nil
	}
	di, err := os.Lstat(credDir)
	if err != nil || !di.IsDir() {
		return false, nil
	}
	fst, ok1 := fi.Sys().(*syscall.Stat_t)
	dst, ok2 := di.Sys().(*syscall.Stat_t)
	switch {
	case !ok1 || !ok2:
		return true, errors.New("cannot read the owner of the systemd credential")
	case fi.Mode().Perm()&^0o440 != 0 || di.Mode().Perm()&^0o550 != 0:
		return true, errors.New("the systemd credential is accessible to other users")
	case fst.Uid != 0 && int64(fst.Uid) != int64(os.Getuid()):
		return true, errors.New("the systemd credential has an unexpected owner")
	case fst.Gid != 0 && int64(fst.Gid) != int64(os.Getgid()):
		return true, errors.New("the systemd credential has an unexpected group")
	case dst.Uid != 0 && int64(dst.Uid) != int64(os.Getuid()):
		return true, errors.New("the systemd credentials directory has an unexpected owner")
	}
	return true, nil
}
