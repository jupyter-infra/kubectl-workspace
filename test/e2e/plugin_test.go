//go:build e2e

/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

// Package e2e runs the plugin as the ProxyCommand of the system's OpenSSH against
// the published sidecar image and the SSH server from the published carrier image,
// the two artifacts users connect to. The operator is not part of this rig: the
// connection is dialed with --url, so the credential and the connection request
// stay covered by the unit tests against the fake operator and by the Kind suite.
package e2e

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jupyter-infra/kubectl-workspace/test/e2e/internal/harness"
)

const (
	sidecarImage = "ghcr.io/jupyter-infra/workspace-websocket-proxy:v0.1.0-rc.2"
	// workspaceImage is built from testdata/workspace.Dockerfile, which copies the
	// SSH server out of the published carrier image into a base with a shell and a user.
	workspaceImage = "kubectl-workspace-e2e-workspace:local"
	alias          = "ws1.default.e2e.jk8s"
	startupTimeout = 90 * time.Second
)

// sshConfig routes the alias through the plugin to the sidecar container.
var sshConfig string

func TestMain(m *testing.M) {
	config, cleanup, err := startRig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e rig:", err)
		os.Exit(1)
	}
	sshConfig = config
	code := m.Run()
	cleanup()
	os.Exit(code)
}

// startRig builds the plugin, starts the two containers and writes the ssh_config
// file, returning its path and the function that removes it all.
func startRig() (string, func(), error) {
	tool := os.Getenv("CONTAINER_TOOL")
	if tool == "" {
		tool = "docker"
	}
	for _, bin := range []string{tool, "ssh", "sftp", "go"} {
		if _, err := exec.LookPath(bin); err != nil {
			return "", nil, fmt.Errorf("%s is not on PATH", bin)
		}
	}
	dir, err := os.MkdirTemp("", "kubectl-workspace-e2e-")
	if err != nil {
		return "", nil, err
	}
	var containers []string
	cleanup := func() {
		for _, c := range containers {
			_ = exec.Command(tool, "rm", "-f", c).Run()
		}
		_ = os.RemoveAll(dir)
	}
	fail := func(err error) (string, func(), error) {
		cleanup()
		return "", nil, err
	}

	plugin := filepath.Join(dir, "kubectl-workspace")
	if err := harness.BuildPlugin(plugin); err != nil {
		return fail(err)
	}
	if out, err := harness.Command(tool, "build", "-t", workspaceImage, "-f", "testdata/workspace.Dockerfile", "testdata"); err != nil {
		return fail(fmt.Errorf("building the workspace image: %v\n%s", err, out))
	}

	port, err := harness.FreePort()
	if err != nil {
		return fail(err)
	}
	suffix := fmt.Sprintf("%d", os.Getpid())
	workspace, proxy := "kw-e2e-workspace-"+suffix, "kw-e2e-proxy-"+suffix
	// The sidecar joins the workspace container's network namespace, as the two
	// containers share one in a pod, and reaches the SSH server on loopback. The
	// published port belongs to that namespace, so it is declared on the first
	// container.
	if out, err := harness.Command(tool, "run", "-d", "--name", workspace,
		"-p", "127.0.0.1:"+port+":8080", workspaceImage); err != nil {
		return fail(fmt.Errorf("starting the workspace container: %v\n%s", err, out))
	}
	containers = append(containers, workspace)
	if out, err := harness.Command(tool, "run", "-d", "--name", proxy, "--network", "container:"+workspace,
		"-e", "LISTEN_ADDR=:8080", "-e", "TARGET_HOST=127.0.0.1", "-e", "TARGET_PORT=2222",
		"-e", "TARGET_HEALTH_INTERVAL=3s", sidecarImage); err != nil {
		return fail(fmt.Errorf("starting the sidecar container: %v\n%s", err, out))
	}
	containers = append(containers, proxy)

	proxyAddr := "127.0.0.1:" + port
	if err := waitForTarget(proxyAddr); err != nil {
		logs, _ := harness.Command(tool, "logs", proxy)
		return fail(fmt.Errorf("%v\nsidecar logs:\n%s", err, logs))
	}

	// The entry is written by hand rather than by `ssh-config`, which has no --url:
	// the operator is not in this rig. Host keys are not checked, since the server
	// generates a fresh one on every start.
	config := filepath.Join(dir, "ssh_config")
	entry := fmt.Sprintf("Host *.e2e.jk8s\n  User jovyan\n  ProxyCommand %s connect --stdio --url ws://%s/ %%h\n"+
		"  StrictHostKeyChecking no\n  UserKnownHostsFile /dev/null\n  LogLevel ERROR\n", plugin, proxyAddr)
	if err := os.WriteFile(config, []byte(entry), 0o600); err != nil {
		return fail(err)
	}
	return config, cleanup, nil
}

// waitForTarget polls the sidecar's target health until it has read the SSH
// server's greeting, which is when a session can be opened.
func waitForTarget(proxyAddr string) error {
	err := harness.Poll(startupTimeout, 500*time.Millisecond, func() (bool, string) {
		resp, err := http.Get("http://" + proxyAddr + "/health/target")
		if err != nil {
			return false, err.Error()
		}
		body := make([]byte, 512)
		n, _ := resp.Body.Read(body)
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK, fmt.Sprintf("%d %s", resp.StatusCode, body[:n])
	})
	if err != nil {
		return fmt.Errorf("the sidecar did not report the SSH server reachable: %v", err)
	}
	return nil
}

func TestSSHRunsACommandThroughThePlugin(t *testing.T) {
	stdout, stderr, code := harness.SSH(t, sshConfig, nil, alias, "echo e2e-ok")
	if code != 0 || stdout != "e2e-ok\n" {
		t.Errorf("ssh exit %d, stdout %q (stderr %q)", code, stdout, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want silence on a clean session", stderr)
	}
}

func TestSSHPassesTheExitStatusThrough(t *testing.T) {
	_, stderr, code := harness.SSH(t, sshConfig, nil, alias, "exit 7")
	if code != 7 {
		t.Errorf("ssh exit %d, want the command's 7 (stderr %q)", code, stderr)
	}
}

func TestSSHAllocatesATerminal(t *testing.T) {
	stdout, stderr, code := harness.SSH(t, sshConfig, nil, "-tt", alias, "tty")
	if code != 0 || !strings.Contains(stdout, "/dev/pts/") {
		t.Errorf("ssh -tt exit %d, stdout %q (stderr %q), want a pseudo-terminal", code, stdout, stderr)
	}
}

func TestSSHCarriesBytesBothWays(t *testing.T) {
	payload := make([]byte, 4<<20)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := harness.SSH(t, sshConfig, payload, alias, "cat")
	if code != 0 || !bytes.Equal([]byte(stdout), payload) {
		t.Errorf("cat through the plugin: exit %d, %d of %d bytes back (stderr %q)", code, len(stdout), len(payload), stderr)
	}
}

func TestSFTPTransfersAFile(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "upload.bin")
	payload := make([]byte, 1<<20)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	back := filepath.Join(dir, "download.bin")
	batch := filepath.Join(dir, "batch")
	if err := os.WriteFile(batch, []byte("put "+local+" /home/jovyan/e2e.bin\nget /home/jovyan/e2e.bin "+back+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := harness.SFTP(sshConfig, batch, alias)
	if err != nil {
		t.Fatalf("sftp: %v\n%s", err, out)
	}
	got, err := os.ReadFile(back)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("the file came back with %d bytes, want %d identical bytes", len(got), len(payload))
	}
}
