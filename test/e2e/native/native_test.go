//go:build e2e

/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

// Package native runs the operating system's own OpenSSH client through the entry
// `ssh-config --write` produces, on Linux, macOS and Windows. The server side is
// in-process: a fake operator granting the connection, a WebSocket bridge standing in
// for the sidecar, and a small SSH server. Under test is the client's handling of the
// ProxyCommand line, which Windows OpenSSH parses itself rather than through a shell,
// and the plugin running under it.
package native

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/jupyter-infra/kubectl-workspace/internal/testutil"
	"github.com/jupyter-infra/kubectl-workspace/test/e2e/internal/harness"
)

const alias = "ws1.default." + testutil.Context + ".jk8s"

// plugin is the binary built once for all tests.
var plugin string

func TestMain(m *testing.M) {
	for _, bin := range []string{"ssh", "go"} {
		if _, err := exec.LookPath(bin); err != nil {
			fmt.Fprintf(os.Stderr, "native rig: %s is not on PATH\n", bin)
			os.Exit(1)
		}
	}
	dir, err := os.MkdirTemp("", "kubectl-workspace-native-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "native rig:", err)
		os.Exit(1)
	}
	plugin = filepath.Join(dir, "kubectl-workspace")
	if runtime.GOOS == "windows" {
		plugin += ".exe"
	}
	if err := harness.BuildPlugin(plugin); err != nil {
		fmt.Fprintln(os.Stderr, "native rig:", err)
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// rig is the entry the plugin wrote for alias, which reaches the in-process SSH
// server through the fake operator and the bridge.
type rig struct {
	sshConfig  string
	knownHosts string
}

func startRig(t *testing.T) *rig {
	t.Helper()
	sshAddr := startSSHServer(t)
	// The operator only ever returns wss URLs and the plugin refuses anything else, so
	// the stand-in sidecar serves TLS and the entry carries its CA, as on a cluster.
	sidecar := testutil.NewTLSEchoServer(t)
	sidecar.SetOnConn(func(ws *websocket.Conn) { bridge(ws, sshAddr) })
	op := testutil.NewFakeOperator(t, http.StatusCreated,
		testutil.ConnectionBody(sidecar.URL("/workspaces/default/ws1/ssh-ws").String()+"?token=native"))
	kubeconfig := testutil.Kubeconfig{Server: op.URL(), CAPEM: op.CAPEM(), Namespace: "default"}.Write(t)

	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(caFile, sidecar.CAPEM(), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &rig{sshConfig: filepath.Join(dir, "ssh_config"), knownHosts: filepath.Join(dir, "known_hosts")}
	if out, err := harness.Command(plugin, "ssh-config", "--write", "--ssh-config-file", r.sshConfig,
		"--kubeconfig", kubeconfig, "--context", testutil.Context, "--command", plugin, "--ca-file", caFile); err != nil {
		t.Fatalf("writing the entry: %v\n%s", err, out)
	}
	return r
}

// ssh runs the system's ssh through the entry, with host keys in the rig's own file.
func (r *rig) ssh(t *testing.T, stdin []byte, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	return harness.SSH(t, r.sshConfig, stdin,
		append([]string{"-o", "UserKnownHostsFile=" + r.knownHosts, "-o", "LogLevel=ERROR"}, args...)...)
}

func TestOpenSSHRunsACommandThroughTheWrittenEntry(t *testing.T) {
	r := startRig(t)
	stdout, stderr, code := r.ssh(t, nil, alias, "echo native-ok")
	if code != 0 || stdout != "native-ok\n" {
		t.Errorf("ssh exit %d, stdout %q (stderr %q)", code, stdout, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want silence on a clean session", stderr)
	}
}

func TestOpenSSHPassesTheExitStatusThrough(t *testing.T) {
	r := startRig(t)
	_, stderr, code := r.ssh(t, nil, alias, "exit 7")
	if code != 7 {
		t.Errorf("ssh exit %d, want the command's 7 (stderr %q)", code, stderr)
	}
}

func TestOpenSSHCarriesBytesBothWays(t *testing.T) {
	r := startRig(t)
	payload := make([]byte, 1<<20)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := r.ssh(t, payload, alias, "cat")
	if code != 0 || !bytes.Equal([]byte(stdout), payload) {
		t.Errorf("cat through the plugin: exit %d, %d of %d bytes back (stderr %q)", code, len(stdout), len(payload), stderr)
	}
}
