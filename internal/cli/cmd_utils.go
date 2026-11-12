/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/jupyter-infra/kubectl-workspace/internal/sshconfig"
)

// commandForEntry resolves --command for the ProxyCommand line: this binary when
// empty, a path made absolute because OpenSSH runs the command from its own working
// directory, and a bare name left to the PATH lookup at connection time.
func commandForEntry(command string) (string, error) {
	switch {
	case command == "":
		return executablePath()
	case strings.ContainsAny(command, `/\`):
		return absPath("command", command)
	default:
		return command, nil
	}
}

// executablePath returns the path OpenSSH should run. When this binary is on PATH
// under its own name, that path is preferred: krew's PATH entry is a symlink into
// a versioned store directory, and the resolved path would break at the next
// upgrade.
func executablePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("failed to locate this binary: %w", err)
	}
	if onPath, err := exec.LookPath(filepath.Base(exe)); err == nil && sameFile(onPath, exe) {
		return onPath, nil
	}
	return exe, nil
}

// kubectlWorkspaceCommand renders a command the user can paste into a shell;
// --cluster-name appears only when the alias label is not the context name, as
// with an EKS ARN.
func kubectlWorkspaceCommand(subcommand, kubeconfig, contextName, cluster string, args ...string) string {
	parts := []string{"kubectl", "workspace", subcommand}
	parts = append(parts, args...)
	if kubeconfig != "" {
		parts = append(parts, "--kubeconfig", kubeconfig)
	}
	parts = append(parts, "--context", contextName)
	if cluster != contextName {
		parts = append(parts, "--cluster-name", cluster)
	}
	quote := sshconfig.ShellQuote
	if runtime.GOOS == goosWindows {
		quote = sshconfig.WindowsQuote
	}
	for i, part := range parts {
		parts[i] = quote(part)
	}
	return strings.Join(parts, " ")
}
