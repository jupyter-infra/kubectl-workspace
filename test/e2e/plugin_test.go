//go:build e2e

/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

// Package e2e runs the plugin as the ProxyCommand of the system's OpenSSH against
// the published sidecar image and the SSH server from the published carrier image,
// the two artifacts users connect to. The operator is not part of this rig: the
// connection is dialed with --url, so the credential and the connection request
// stay covered by the unit tests against the fake operator.
package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	sidecarImage = "ghcr.io/jupyter-infra/workspace-websocket-proxy:v0.1.0-rc.2"
	// workspaceImage is built from testdata/workspace.Dockerfile, which copies the
	// SSH server out of the published carrier image into a base with a shell and a user.
	workspaceImage = "kubectl-workspace-e2e-workspace:local"
	alias          = "ws1.default.e2e.jk8s"
	startupTimeout = 90 * time.Second
	commandTimeout = 60 * time.Second
)

// rig is the plugin binary, the two containers and the SSH configuration that
// routes the alias through the plugin.
type rig struct {
	tool      string
	sshConfig string
	proxyAddr string
}

var shared *rig

func TestMain(m *testing.M) {
	r, cleanup, err := startRig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e rig:", err)
		os.Exit(1)
	}
	shared = r
	code := m.Run()
	cleanup()
	os.Exit(code)
}

func startRig() (*rig, func(), error) {
	tool := os.Getenv("CONTAINER_TOOL")
	if tool == "" {
		tool = "docker"
	}
	for _, bin := range []string{tool, "ssh", "sftp", "go"} {
		if _, err := exec.LookPath(bin); err != nil {
			return nil, nil, fmt.Errorf("%s is not on PATH", bin)
		}
	}
	dir, err := os.MkdirTemp("", "kubectl-workspace-e2e-")
	if err != nil {
		return nil, nil, err
	}
	var containers []string
	cleanup := func() {
		for _, c := range containers {
			_ = exec.Command(tool, "rm", "-f", c).Run()
		}
		_ = os.RemoveAll(dir)
	}
	fail := func(err error) (*rig, func(), error) {
		cleanup()
		return nil, nil, err
	}

	plugin := filepath.Join(dir, "kubectl-workspace")
	if out, err := command("go", "build", "-o", plugin, "../../cmd/kubectl-workspace"); err != nil {
		return fail(fmt.Errorf("building the plugin: %v\n%s", err, out))
	}
	if out, err := command(tool, "build", "-t", workspaceImage, "-f", "testdata/workspace.Dockerfile", "testdata"); err != nil {
		return fail(fmt.Errorf("building the workspace image: %v\n%s", err, out))
	}

	port, err := freePort()
	if err != nil {
		return fail(err)
	}
	suffix := fmt.Sprintf("%d", os.Getpid())
	workspace, proxy := "kw-e2e-workspace-"+suffix, "kw-e2e-proxy-"+suffix
	// The sidecar joins the workspace container's network namespace, as the two
	// containers share one in a pod, and reaches the SSH server on loopback. The
	// published port belongs to that namespace, so it is declared on the first
	// container.
	if out, err := command(tool, "run", "-d", "--name", workspace,
		"-p", "127.0.0.1:"+port+":8080", workspaceImage); err != nil {
		return fail(fmt.Errorf("starting the workspace container: %v\n%s", err, out))
	}
	containers = append(containers, workspace)
	if out, err := command(tool, "run", "-d", "--name", proxy, "--network", "container:"+workspace,
		"-e", "LISTEN_ADDR=:8080", "-e", "TARGET_HOST=127.0.0.1", "-e", "TARGET_PORT=2222",
		"-e", "TARGET_HEALTH_INTERVAL=3s", sidecarImage); err != nil {
		return fail(fmt.Errorf("starting the sidecar container: %v\n%s", err, out))
	}
	containers = append(containers, proxy)

	proxyAddr := "127.0.0.1:" + port
	if err := waitForTarget(proxyAddr); err != nil {
		logs, _ := command(tool, "logs", proxy)
		return fail(fmt.Errorf("%v\nsidecar logs:\n%s", err, logs))
	}

	// The entry is written by hand rather than by `ssh-config`, which has no --url:
	// the operator is not in this rig. Host keys are not checked, since the server
	// generates a fresh one on every start.
	sshConfig := filepath.Join(dir, "ssh_config")
	entry := fmt.Sprintf("Host *.e2e.jk8s\n  User jovyan\n  ProxyCommand %s connect --stdio --url ws://%s/ %%h\n"+
		"  StrictHostKeyChecking no\n  UserKnownHostsFile /dev/null\n  LogLevel ERROR\n", plugin, proxyAddr)
	if err := os.WriteFile(sshConfig, []byte(entry), 0o600); err != nil {
		return fail(err)
	}
	return &rig{tool: tool, sshConfig: sshConfig, proxyAddr: proxyAddr}, cleanup, nil
}

func command(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

func freePort() (string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer func() { _ = listener.Close() }()
	_, port, err := net.SplitHostPort(listener.Addr().String())
	return port, err
}

// waitForTarget polls the sidecar's target health until it has read the SSH
// server's greeting, which is when a session can be opened.
func waitForTarget(proxyAddr string) error {
	deadline := time.Now().Add(startupTimeout)
	var last string
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + proxyAddr + "/health/target")
		if err == nil {
			body := make([]byte, 512)
			n, _ := resp.Body.Read(body)
			_ = resp.Body.Close()
			last = fmt.Sprintf("%d %s", resp.StatusCode, body[:n])
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		} else {
			last = err.Error()
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("the sidecar did not report the SSH server reachable within %s; last answer: %s", startupTimeout, last)
}

// ssh runs the system's ssh against the alias through the plugin and returns
// stdout, stderr and the exit code.
func (r *rig) ssh(t *testing.T, stdin []byte, args ...string) (string, string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", append([]string{"-F", r.sshConfig}, args...)...)
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("ssh did not run: %v (stderr: %s)", err, stderr.String())
	}
	return stdout.String(), stderr.String(), code
}

func TestSSHRunsACommandThroughThePlugin(t *testing.T) {
	stdout, stderr, code := shared.ssh(t, nil, alias, "echo e2e-ok")
	if code != 0 || stdout != "e2e-ok\n" {
		t.Errorf("ssh exit %d, stdout %q (stderr %q)", code, stdout, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want silence on a clean session", stderr)
	}
}

func TestSSHPassesTheExitStatusThrough(t *testing.T) {
	_, stderr, code := shared.ssh(t, nil, alias, "exit 7")
	if code != 7 {
		t.Errorf("ssh exit %d, want the command's 7 (stderr %q)", code, stderr)
	}
}

func TestSSHAllocatesATerminal(t *testing.T) {
	stdout, stderr, code := shared.ssh(t, nil, "-tt", alias, "tty")
	if code != 0 || !strings.Contains(stdout, "/dev/pts/") {
		t.Errorf("ssh -tt exit %d, stdout %q (stderr %q), want a pseudo-terminal", code, stdout, stderr)
	}
}

func TestSSHCarriesBytesBothWays(t *testing.T) {
	payload := make([]byte, 4<<20)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := shared.ssh(t, payload, alias, "cat")
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

	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sftp", "-F", shared.sshConfig, "-b", batch, alias).CombinedOutput()
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
