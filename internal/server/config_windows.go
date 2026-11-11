//go:build windows

package server

import (
	"errors"
	"os"
)

// checkSecretsFileInfo refuses: Windows is not a supported host, and its
// ACLs cannot be checked as owner-only file modes. Secret parsing is still
// tested on Windows through parseSecrets.
func checkSecretsFileInfo(os.FileInfo) error {
	return errors.New("the secrets file permissions cannot be verified on Windows; run the server on macOS or Linux")
}
