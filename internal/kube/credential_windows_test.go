/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package kube

import (
	"os"
	"os/exec"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

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

// endProcess ends a process the tests started and no longer need.
func endProcess(pid int) {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return
	}
	_ = windows.TerminateProcess(h, 1)
	_ = windows.CloseHandle(h)
}

// The job ends with the process that created it: when OpenSSH ends connect in the
// middle of a sign-in, the plugin goes too instead of holding the callback port.
// What the plugin started goes with it only when it ran without a terminal; the
// browser an interactive plugin opened keeps running. Unix has no portable
// equivalent for an abrupt end of this process, so this is Windows-only.
func TestJobEndsThePluginWhenThisProcessIsKilled(t *testing.T) {
	tests := []struct {
		name          string
		mode          string
		wantChildGone bool
	}{
		{name: "non-interactive plugin and its child", mode: "resolve-sleeping-child", wantChildGone: true},
		{name: "interactive plugin, its child left alone", mode: "resolve-interactive-sleeping-child"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			helper := exec.Command(os.Args[0])
			helper.Env = append(os.Environ(), fakePluginEnv+"="+tt.mode)
			stderr, err := helper.StderrPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := helper.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = helper.Process.Kill(); _ = helper.Wait() })
			pids := scanPIDs(t, stderr, pluginPIDPrefix, childPIDPrefix)
			plugin, child := pids[pluginPIDPrefix], pids[childPIDPrefix]
			t.Cleanup(func() { endProcess(child); endProcess(plugin) })

			if err := helper.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if !goneWithin(plugin, 3*time.Second) {
				t.Fatalf("the plugin %d survived the end of the process that started it", plugin)
			}
			if gone := goneWithin(child, time.Second); gone != tt.wantChildGone {
				t.Errorf("plugin's child gone = %v, want %v", gone, tt.wantChildGone)
			}
		})
	}
}
