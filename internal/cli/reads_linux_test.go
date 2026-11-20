/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package cli

import (
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/jupyter-infra/kubectl-workspace/internal/sshconfig"
)

// Each command reads the SSH configuration file once: it loads the file, decides and
// edits in memory and writes it back in one replace. inotify reports every open of the
// file, so the count is the contract.
func TestEachCommandReadsTheSSHConfigurationOnce(t *testing.T) {
	kubeconfig := staticKubeconfig(t)
	file := filepath.Join(t.TempDir(), "config")
	installEntry(t, file, sshconfig.Entry{Cluster: "dev", Context: "dev", Kubeconfig: kubeconfig})
	for _, tt := range []struct {
		name string
		args []string
	}{
		{name: "ssh-config --write replacing the entry", args: []string{"ssh-config", "--write", "--command", kwCommand}},
		{name: "open --print-only", args: []string{"open", "--print-only", "ws1"}},
		{name: "ssh-config --remove", args: []string{"ssh-config", "--remove"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			opens := countOpens(t, file, func() {
				if _, err := run(t, append(tt.args, "--kubeconfig", kubeconfig, "--ssh-config-file", file)...); err != nil {
					t.Fatal(err)
				}
			})
			if opens != 1 {
				t.Errorf("the file was opened %d times, want once", opens)
			}
		})
	}
}

// countOpens runs fn and returns how many times the file at path was opened meanwhile.
// The watch sits on the directory, since a write replaces the file through a rename and a
// watch on the file itself would stop at the old one.
func countOpens(t *testing.T, path string, fn func()) int {
	t.Helper()
	fd, err := unix.InotifyInit1(unix.IN_NONBLOCK)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(fd) }()
	// inotify folds consecutive identical events into one; the read and the close between
	// two opens keep each open its own event.
	mask := uint32(unix.IN_OPEN | unix.IN_ACCESS | unix.IN_CLOSE_NOWRITE)
	if _, err := unix.InotifyAddWatch(fd, filepath.Dir(path), mask); err != nil {
		t.Fatal(err)
	}
	fn()
	buf := make([]byte, 256*(unix.SizeofInotifyEvent+unix.NAME_MAX+1))
	opens := 0
	for {
		n, err := unix.Read(fd, buf)
		if err == unix.EAGAIN {
			return opens
		}
		if err != nil {
			t.Fatal(err)
		}
		for off := 0; off < n; {
			event := (*unix.InotifyEvent)(unsafe.Pointer(&buf[off]))
			nameStart := off + unix.SizeofInotifyEvent
			name := strings.TrimRight(string(buf[nameStart:nameStart+int(event.Len)]), "\x00")
			if event.Mask&unix.IN_OPEN != 0 && name == filepath.Base(path) {
				opens++
			}
			off = nameStart + int(event.Len)
		}
	}
}
