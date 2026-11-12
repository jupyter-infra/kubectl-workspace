/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

// Package harness holds what the end-to-end suites share: commands with a timeout,
// a free loopback port, the plugin build, polling, and ssh and sftp runs through an
// ssh_config file.
package harness

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"
)

const (
	commandTimeout = 5 * time.Minute
	sessionTimeout = 60 * time.Second
)

// Command runs name with args and returns its combined output.
func Command(name string, args ...string) (string, error) {
	return CommandWithStdin("", name, args...)
}

// Output runs name with args and returns its stdout alone; stderr goes into the error.
func Output(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%w\n%s", err, stderr.String())
	}
	return string(out), nil
}

// CommandWithStdin runs name with args, feeding stdin, and returns its combined output.
func CommandWithStdin(stdin string, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = bytes.NewBufferString(stdin)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// Images returns the sidecar image and the stand-in workspace image the Makefile pins
// and passes to the suites in E2E_SIDECAR_IMAGE and E2E_WORKSPACE_IMAGE.
func Images() (sidecar, workspace string, err error) {
	sidecar, workspace = os.Getenv("E2E_SIDECAR_IMAGE"), os.Getenv("E2E_WORKSPACE_IMAGE")
	if sidecar == "" || workspace == "" {
		return "", "", errors.New("E2E_SIDECAR_IMAGE and E2E_WORKSPACE_IMAGE are not set; run the suite through make")
	}
	return sidecar, workspace, nil
}

// FreePort returns a loopback port that was free when asked.
func FreePort() (string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer func() { _ = listener.Close() }()
	_, port, err := net.SplitHostPort(listener.Addr().String())
	return port, err
}

// BuildPlugin builds kubectl-workspace from this module into path.
func BuildPlugin(path string) error {
	out, err := Command("go", "build", "-o", path, "github.com/jupyter-infra/kubectl-workspace/cmd/kubectl-workspace")
	if err != nil {
		return fmt.Errorf("building the plugin: %v\n%s", err, out)
	}
	return nil
}

// Poll calls check every interval until it reports done or timeout passes. The
// error carries the last status check returned.
func Poll(timeout, interval time.Duration, check func() (done bool, status string)) error {
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		done, status := check()
		if done {
			return nil
		}
		last = status
		time.Sleep(interval)
	}
	return fmt.Errorf("not ready within %s; last: %s", timeout, last)
}

// SSH runs the system's ssh with the ssh_config file and the arguments, feeding
// stdin, and returns stdout, stderr and the exit code.
func SSH(t *testing.T, sshConfig string, stdin []byte, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), sessionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", append([]string{"-F", sshConfig}, args...)...)
	cmd.Stdin = bytes.NewReader(stdin)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	return out.String(), errOut.String(), exitCode(t, "ssh", err, errOut.String())
}

// SFTP runs the system's sftp with the ssh_config file and a batch file against
// alias and returns its combined output and exit code.
func SFTP(t *testing.T, sshConfig, batch, alias string) (output string, code int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), sessionTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sftp", "-F", sshConfig, "-b", batch, alias).CombinedOutput()
	return string(out), exitCode(t, "sftp", err, string(out))
}

// exitCode reads the exit status out of a finished command's error; a command that
// did not run at all fails the test.
func exitCode(t *testing.T, name string, err error, output string) int {
	t.Helper()
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		return exitErr.ExitCode()
	case err != nil:
		t.Fatalf("%s did not run: %v\n%s", name, err, output)
	}
	return 0
}
