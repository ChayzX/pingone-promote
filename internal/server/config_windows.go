//go:build windows

package server

import (
	"errors"
	"os"
)

// openSecretsFile opens the file; Windows is refused by
// checkSecretsFileInfo before it is used.
func openSecretsFile(path string) (*os.File, error) { return os.Open(path) }

// checkSecretsFileInfo refuses: Windows is not a supported host, and its
// ACLs cannot be checked as owner-only file modes. Secret parsing is still
// tested on Windows through parseSecrets.
func checkSecretsFileInfo(string, os.FileInfo) error {
	return errors.New("the secrets file permissions cannot be verified on Windows; run the server on macOS or Linux")
}

// credentialsRoot is unused on Windows (not a supported host).
var credentialsRoot = "/run/credentials/"
