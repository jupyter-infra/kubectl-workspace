//go:build e2e && linux

/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package kind

import (
	"os/exec"
	"syscall"
)

// dieWithParent kills the port-forward with the test binary, so a test timeout or a
// panic that skips cleanup leaves no process holding the port.
func dieWithParent(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
