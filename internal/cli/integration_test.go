/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package cli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/jupyter-infra/kubectl-workspace/internal/testutil"
)

// runMainEnv makes the test binary run the plugin's Main instead of the tests,
// so a test can start the plugin as the subprocess OpenSSH would.
const runMainEnv = "KUBECTL_WORKSPACE_TEST_RUN_MAIN"

func TestMain(m *testing.M) {
	if os.Getenv(runMainEnv) == "1" {
		os.Exit(Main(os.Args[1:], Streams{In: os.Stdin, Out: os.Stdout, Err: os.Stderr}, "test"))
	}
	os.Exit(m.Run())
}

// pluginProcess is the plugin running as a ProxyCommand: writes go to its stdin,
// reads come from its stdout, as for the SSH client OpenSSH would attach.
type pluginProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr bytes.Buffer
}

func startPlugin(t *testing.T, args ...string) *pluginProcess {
	t.Helper()
	p := &pluginProcess{cmd: exec.Command(os.Args[0], args...)}
	p.cmd.Env = append(os.Environ(), runMainEnv+"=1")
	p.cmd.Stderr = &p.stderr
	var err error
	if p.stdin, err = p.cmd.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	if p.stdout, err = p.cmd.StdoutPipe(); err != nil {
		t.Fatal(err)
	}
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.cmd.Process.Kill() })
	return p
}

// roundTrip sends payload through the plugin and returns what came back.
func (p *pluginProcess) roundTrip(t *testing.T, payload string) string {
	t.Helper()
	if _, err := io.WriteString(p.stdin, payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(p.stdout, got); err != nil {
		t.Fatalf("reading the echo back: %v (stderr: %s)", err, p.stderr.String())
	}
	return string(got)
}

// wait returns the plugin's exit code once it ends.
func (p *pluginProcess) wait(t *testing.T) int {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case err := <-done:
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		if err != nil {
			t.Fatalf("waiting for the plugin: %v", err)
		}
		return 0
	case <-time.After(10 * time.Second):
		t.Fatal("the plugin did not exit")
		return -1
	}
}

// startConnected runs the plugin as a subprocess against the echo server standing
// in for the sidecar, and proves the tunnel with one round trip.
func startConnected(t *testing.T) *pluginProcess {
	t.Helper()
	echo := testutil.NewEchoServer(t)
	plugin := startPlugin(t, "connect", "--stdio", "--url", echo.URL("/").String(), alias)
	if got := plugin.roundTrip(t, "SSH-2.0-client\r\n"); got != "SSH-2.0-client\r\n" {
		t.Fatalf("round trip = %q", got)
	}
	return plugin
}

// The plugin runs in the position OpenSSH gives a ProxyCommand: a subprocess with
// the SSH stream on its standard streams, which must stay clean.
func TestPluginAsSubprocessCarriesTheStreamAndExitsWithTheClient(t *testing.T) {
	plugin := startConnected(t)

	payload := bytes.Repeat([]byte("0123456789abcdef"), 4096)
	if got := plugin.roundTrip(t, string(payload)); got != string(payload) {
		t.Error("a 64 KiB payload did not come back intact")
	}

	// The SSH client exiting closes the plugin's stdin.
	if err := plugin.stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if code := plugin.wait(t); code != 0 {
		t.Errorf("the plugin exited %d after the client closed, want 0", code)
	}
	if rest, _ := io.ReadAll(plugin.stdout); len(rest) != 0 {
		t.Errorf("%d bytes reached stdout after the client closed", len(rest))
	}
	if plugin.stderr.Len() != 0 {
		t.Errorf("stderr = %q, want silence on a clean session", plugin.stderr.String())
	}
}

// OpenSSH ends a ProxyCommand with SIGHUP rather than by closing its stdin.
func TestPluginExitsCleanlyOnSIGHUP(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGHUP is a Unix signal")
	}
	plugin := startConnected(t)

	if err := plugin.cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	if code := plugin.wait(t); code != 0 {
		t.Errorf("the plugin exited %d on SIGHUP, want 0", code)
	}
	if plugin.stderr.Len() != 0 {
		t.Errorf("stderr = %q, want silence", plugin.stderr.String())
	}
}
