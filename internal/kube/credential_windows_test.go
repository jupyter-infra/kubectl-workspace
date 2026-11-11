/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package kube

import (
	"bufio"
	"os"
	"os/exec"
	"strconv"
	"strings"
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

// The job ends with the process that created it: when OpenSSH kills connect in the
// middle of a sign-in, the credential plugin and its children go too, instead of
// holding the sign-in callback port. Unix has no equivalent for a hard kill, so
// this is Windows-only.
func TestJobEndsThePluginWhenThisProcessIsKilled(t *testing.T) {
	helper := exec.Command(os.Args[0])
	helper.Env = append(os.Environ(), fakePluginEnv+"=resolve-sleeping-child")
	stderr, err := helper.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = helper.Process.Kill(); _ = helper.Wait() })

	childPID := 0
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		if raw, ok := strings.CutPrefix(scanner.Text(), "child-pid:"); ok {
			childPID, _ = strconv.Atoi(raw)
			break
		}
	}
	if childPID == 0 {
		t.Fatal("the plugin did not report its child")
	}
	if child, err := os.FindProcess(childPID); err == nil {
		defer func() { _ = child.Kill() }()
	}

	if err := helper.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if processGone(childPID) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("the plugin's child %d survived the death of the process that started the plugin", childPID)
}
