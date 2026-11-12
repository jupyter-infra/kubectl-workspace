//go:build e2e && !linux

/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package kind

import "os/exec"

// dieWithParent is a no-op: macOS has no parent-death signal, so a port-forward
// left by an interrupted run has to be stopped by hand.
func dieWithParent(*exec.Cmd) {}
