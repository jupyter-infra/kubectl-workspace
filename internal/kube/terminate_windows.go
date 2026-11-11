/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package kube

import (
	"os"
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// stillActive is the exit code GetExitCodeProcess reports while a process runs.
const stillActive = 259

// processTree ends the credential plugin together with the processes it started.
// Windows has no process groups to signal; a job object holds the plugin and
// everything it starts and ends them all when terminated or when its handle
// closes, which also covers this process being killed by OpenSSH. On a Windows
// that refuses to nest jobs the plugin is killed alone, as before.
type processTree struct {
	cmd *exec.Cmd
	// mu guards the handles: attach sets them after the plugin starts, while
	// terminate runs on the goroutine exec watches the context from.
	mu   sync.Mutex
	job  windows.Handle
	proc windows.Handle
}

// newProcessTree creates the job; cmd must not have started yet.
func newProcessTree(cmd *exec.Cmd, _ bool) *processTree {
	t := &processTree{cmd: cmd}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return t
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return t
	}
	t.job = job
	return t
}

// attach puts the running plugin into the job. A child the plugin starts before
// this call is not covered; kubectl needs far longer to start its plugin.
func (t *processTree) attach() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.job == 0 {
		return
	}
	access := uint32(windows.PROCESS_SET_QUOTA | windows.PROCESS_TERMINATE | windows.PROCESS_QUERY_LIMITED_INFORMATION)
	proc, err := windows.OpenProcess(access, false, uint32(t.cmd.Process.Pid))
	if err != nil {
		t.dropJob()
		return
	}
	if err := windows.AssignProcessToJobObject(t.job, proc); err != nil {
		_ = windows.CloseHandle(proc)
		t.dropJob()
		return
	}
	t.proc = proc
}

// terminate ends the job, and with it the plugin and its children. A plugin that
// had already exited is reported as done, which exec treats as benign.
func (t *processTree) terminate() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	exited := t.exited()
	var err error
	if t.job != 0 {
		err = windows.TerminateJobObject(t.job, 1)
	} else {
		err = t.cmd.Process.Kill()
	}
	if exited {
		return os.ErrProcessDone
	}
	return err
}

// exited reports whether the plugin has ended; the caller holds mu.
func (t *processTree) exited() bool {
	if t.proc == 0 {
		return false
	}
	var code uint32
	return windows.GetExitCodeProcess(t.proc, &code) == nil && code != stillActive
}

// close releases the handles; kill-on-close ends anything still in the job.
func (t *processTree) close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.proc != 0 {
		_ = windows.CloseHandle(t.proc)
		t.proc = 0
	}
	t.dropJob()
}

// dropJob closes the job, leaving the plugin to be killed alone; the caller holds mu.
func (t *processTree) dropJob() {
	if t.job != 0 {
		_ = windows.CloseHandle(t.job)
		t.job = 0
	}
}
