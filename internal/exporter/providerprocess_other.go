//go:build !linux && !darwin

package exporter

import (
	"errors"
	"os/exec"
)

// Fail closed where the reader cannot guarantee provider-process containment.
func providerContainProcess(cmd *exec.Cmd) error {
	return errors.New("provider reader requires process-group containment")
}
