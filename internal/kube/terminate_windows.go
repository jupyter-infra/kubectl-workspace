//go:build windows

/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package kube

import (
	"os"
	"os/exec"
)

// setProcessGroup is a no-op: Windows has no process groups to signal.
func setProcessGroup(*exec.Cmd) {}

// terminate kills the plugin; Windows has no SIGTERM to deliver.
func terminate(p *os.Process, _ bool) error {
	return p.Kill()
}
