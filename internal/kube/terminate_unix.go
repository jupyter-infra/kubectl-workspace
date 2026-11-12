//go:build !windows

/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package kube

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// processTree ends the credential plugin together with the processes it started.
// A non-interactive plugin runs in its own process group, so one signal reaches
// its children too; an interactive plugin keeps the terminal's group, or reading
// the terminal would stop it.
type processTree struct {
	cmd   *exec.Cmd
	group bool
}

// newProcessTree prepares cmd, which must not have started yet.
func newProcessTree(cmd *exec.Cmd, children bool) *processTree {
	if children {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	return &processTree{cmd: cmd, group: children}
}

// attach runs once the plugin has started; the group needs nothing more.
func (t *processTree) attach() {}

// terminate asks the plugin to exit; cmd.WaitDelay kills it if it does not.
func (t *processTree) terminate() error {
	if t.group {
		err := syscall.Kill(-t.cmd.Process.Pid, syscall.SIGTERM)
		if errors.Is(err, syscall.ESRCH) {
			// The group is gone: the plugin exited before the deadline was acted on,
			// which exec only treats as benign under this error.
			return os.ErrProcessDone
		}
		return err
	}
	return t.cmd.Process.Signal(syscall.SIGTERM)
}

func (t *processTree) close() {}
