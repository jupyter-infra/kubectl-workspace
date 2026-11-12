/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package cli

import (
	"bytes"
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
