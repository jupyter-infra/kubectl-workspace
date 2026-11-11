/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package kube

import "golang.org/x/sys/windows"

// processGone reports whether the process has exited or no longer exists.
func processGone(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return true
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var code uint32
	return windows.GetExitCodeProcess(h, &code) != nil || code != stillActive
}
