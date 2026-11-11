//go:build linux || darwin

package exporter

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// Terraform starts provider plugins. A deadline must kill the entire isolated
// process group, including plugins that still hold pipes or worker credentials.
func providerContainProcess(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}
