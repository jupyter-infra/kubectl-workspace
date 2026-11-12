/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jupyter-infra/kubectl-workspace/internal/testutil"
)

func TestExecutablePathPrefersThePathEntry(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// krew exposes the plugin through a symlink in a directory on PATH.
	dir := t.TempDir()
	link := filepath.Join(dir, filepath.Base(exe))
	testutil.Symlink(t, exe, link)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	got, err := executablePath()
	if err != nil {
		t.Fatal(err)
	}
	if got != link {
		t.Errorf("executablePath() = %q, want the PATH entry %q", got, link)
	}
}

func TestExecutablePathFallsBackToTheBinaryItself(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())

	got, err := executablePath()
	if err != nil {
		t.Fatal(err)
	}
	if got != exe {
		t.Errorf("executablePath() = %q, want %q", got, exe)
	}
}

func TestKubectlWorkspaceCommandQuotesForTheShell(t *testing.T) {
	got := kubectlWorkspaceCommand("open", "/My Files/kubeconfig", "dev", "dev", "ws1")
	want := `kubectl workspace open ws1 --kubeconfig "/My Files/kubeconfig" --context dev`
	if got != want {
		t.Errorf("command = %q, want %q", got, want)
	}
}
