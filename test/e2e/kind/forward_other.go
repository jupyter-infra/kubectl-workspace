//go:build e2e && !linux

/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package kind

import "os/exec"

// dieWithParent is a no-op: only Linux can tie a child's life to its parent's.
func dieWithParent(*exec.Cmd) {}
