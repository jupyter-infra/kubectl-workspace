/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestHelpNamesThePluginAsKubectlWorkspace(t *testing.T) {
	stdout, err := run(t, "--help")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "kubectl workspace [command]") {
		t.Errorf("help does not show the kubectl workspace usage:\n%s", stdout)
	}
}

func TestMainReturnsTheExitCode(t *testing.T) {
	var out, errOut bytes.Buffer
	streams := Streams{In: strings.NewReader(""), Out: &out, Err: &errOut}

	if code := Main([]string{"--version"}, streams, "1.2.3"); code != 0 || out.String() != "kubectl workspace 1.2.3\n" {
		t.Errorf("--version: code %d, stdout %q", code, out.String())
	}
	if code := Main([]string{"connect", alias}, streams, "1.2.3"); code != 1 {
		t.Errorf("a failing command returned %d, want 1", code)
	}
	if !strings.HasPrefix(errOut.String(), "kubectl-workspace: ") {
		t.Errorf("stderr = %q, want the program name prefix", errOut.String())
	}
}

func TestExecutablePathPrefersThePathEntry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// krew exposes the plugin through a symlink in a directory on PATH.
	dir := t.TempDir()
	link := filepath.Join(dir, filepath.Base(exe))
	if err := os.Symlink(exe, link); err != nil {
		t.Fatal(err)
	}
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
