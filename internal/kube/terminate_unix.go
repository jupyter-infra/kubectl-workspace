//go:build !windows

/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package kube

import (
	"os"
	"os/exec"
	"syscall"
)

// setProcessGroup starts the plugin in its own process group, so terminate can
// reach children the plugin spawned. Interactive plugins keep the terminal's
// group, or reading the terminal would stop them.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminate asks the plugin to exit; cmd.WaitDelay kills it if it does not.
func terminate(p *os.Process, group bool) error {
	if group {
		return syscall.Kill(-p.Pid, syscall.SIGTERM)
	}
	return p.Signal(syscall.SIGTERM)
}
