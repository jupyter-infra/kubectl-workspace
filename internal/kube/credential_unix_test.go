//go:build !windows

/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package kube

import "syscall"

// processGone reports whether no process with pid answers; a zombie still
// answers until it is reaped.
func processGone(pid int) bool {
	return syscall.Kill(pid, 0) != nil
}
