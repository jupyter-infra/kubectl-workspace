//go:build e2e

/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

// Package kind runs the plugin against a Kind cluster that carries the operator with
// token signing on, the auth middleware and Traefik with a certificate from the rig's
// own certificate authority: the path a connection takes in a deployment. `make
// setup-e2e-kind` installs those cluster-level pieces; this package applies the
// workspace fixtures, writes the SSH entry with the plugin and the rig's CA, and
// drives ssh and the plugin's commands with the cluster's own kubeconfig.
package kind

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jupyter-infra/kubectl-workspace/internal/testutil"
	"github.com/jupyter-infra/kubectl-workspace/test/e2e/internal/harness"
)

const (
	defaultCluster = "kubectl-workspace-e2e"
	namespace      = "team-a"
	workspace      = "ws1"
	traefikNS      = "traefik"
	// readyTimeout bounds the workspace's start; the sidecar image is pulled from
	// GHCR on a fresh cluster.
	readyTimeout = 4 * time.Minute
)

// rig is the cluster's kubeconfig, the plugin binary, the port-forward to Traefik,
// the rig's certificate authority and the SSH entry the plugin wrote.
type rig struct {
	dir        string
	kubeconfig string
	context    string
	plugin     string
	manifest   string
	forward    *exec.Cmd
	port       string
	caFile     string
	sshConfig  string
	knownHosts string
	alias      string
}

var shared *rig

func TestMain(m *testing.M) {
	r, err := startRig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "kind rig:", err)
		r.cleanup()
		os.Exit(1)
	}
	shared = r
	code := m.Run()
	r.cleanup()
	os.Exit(code)
}

func startRig() (*rig, error) {
	for _, bin := range []string{"kubectl", "ssh", "sftp", "go"} {
		if _, err := exec.LookPath(bin); err != nil {
			return &rig{}, fmt.Errorf("%s is not on PATH", bin)
		}
	}
	cluster := os.Getenv("E2E_KIND_CLUSTER")
	if cluster == "" {
		cluster = defaultCluster
	}
	sidecarImage, workspaceImage, err := harness.Images()
	if err != nil {
		return &rig{}, err
	}
	r := &rig{context: "kind-" + cluster}
	dir, err := os.MkdirTemp("", "kubectl-workspace-kind-")
	if err != nil {
		return r, err
	}
	r.dir = dir

	// The cluster's admin kubeconfig in a file of the rig's own: the user's
	// kubeconfig is read once here and never written.
	r.kubeconfig = filepath.Join(dir, "kubeconfig")
	out, err := harness.Output("kubectl", "config", "view", "--raw", "--minify", "--flatten", "--context", r.context)
	if err != nil {
		return r, fmt.Errorf("exporting the kubeconfig for %s: %v", r.context, err)
	}
	if err := os.WriteFile(r.kubeconfig, []byte(out), 0o600); err != nil {
		return r, err
	}
	r.plugin = filepath.Join(dir, "kubectl-workspace")
	if err := harness.BuildPlugin(r.plugin); err != nil {
		return r, err
	}

	// Traefik's TLS port is reached through a port-forward, so the cluster needs no
	// port mapping of its own and the suite runs against any Kind cluster.
	port, err := harness.FreePort()
	if err != nil {
		return r, err
	}
	r.port = port
	var forwardOutput bytes.Buffer
	r.forward = exec.Command("kubectl", "--kubeconfig", r.kubeconfig, "-n", traefikNS, "port-forward", "svc/traefik", port+":443")
	dieWithParent(r.forward)
	r.forward.Stdout, r.forward.Stderr = &forwardOutput, &forwardOutput
	if err := r.forward.Start(); err != nil {
		return r, fmt.Errorf("starting the port-forward to Traefik: %v", err)
	}
	if err := harness.Poll(60*time.Second, 500*time.Millisecond, func() (bool, string) {
		conn, err := tls.Dial("tcp", "127.0.0.1:"+port, &tls.Config{InsecureSkipVerify: true})
		if err != nil {
			return false, err.Error()
		}
		_ = conn.Close()
		return true, ""
	}); err != nil {
		r.stopForward()
		return r, fmt.Errorf("Traefik is not answering through the port-forward: %v\n%s", err, forwardOutput.String())
	}

	// The rig's certificate authority, which the plugin is told to trust.
	caData, err := r.kubectl("-n", traefikNS, "get", "secret", "traefik-e2e-tls", "-o", `jsonpath={.data.ca\.crt}`)
	if err != nil {
		return r, fmt.Errorf("reading the rig's CA: %v\n%s", err, caData)
	}
	ca, err := base64.StdEncoding.DecodeString(strings.TrimSpace(caData))
	if err != nil || len(ca) == 0 {
		return r, fmt.Errorf("the rig's CA secret carries no ca.crt (%v)", err)
	}
	r.caFile = filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(r.caFile, ca, 0o600); err != nil {
		return r, err
	}

	// Right after setup the operator's webhook can still be warming up, and it reads
	// the template and the strategy from an informer cache that may not have seen
	// them yet, or holds a version the controller has since updated, so a refused
	// apply is retried; a denial for any other reason is final.
	fixtures, err := os.ReadFile(filepath.Join("..", "testdata", "kind", "fixtures.yaml"))
	if err != nil {
		return r, err
	}
	r.manifest = strings.NewReplacer("__PORT__", port, "__SIDECAR_IMAGE__", sidecarImage,
		"__WORKSPACE_IMAGE__", workspaceImage).Replace(string(fixtures))
	var denied error
	err = harness.Poll(2*time.Minute, 5*time.Second, func() (bool, string) {
		out, err := r.kubectlStdin(r.manifest, "apply", "-f", "-")
		if err != nil && strings.Contains(out, "denied the request") &&
			!strings.Contains(out, "not found") && !strings.Contains(out, "has been modified") {
			denied = fmt.Errorf("%s", strings.TrimSpace(out))
			return true, ""
		}
		return err == nil, strings.TrimSpace(out)
	})
	if err == nil {
		err = denied
	}
	if err != nil {
		return r, fmt.Errorf("applying the fixtures: %v", err)
	}
	if out, err := r.waitAvailable(); err != nil {
		pods, _ := r.kubectl("-n", namespace, "get", "pods", "-o", "wide")
		status, _ := r.kubectl("-n", namespace, "get", "workspace", workspace, "-o", "yaml")
		return r, fmt.Errorf("the workspace did not become Available: %v\n%s\npods:\n%s\nworkspace:\n%s", err, out, pods, status)
	}

	r.sshConfig = filepath.Join(dir, "ssh_config")
	r.knownHosts = filepath.Join(dir, "known_hosts")
	if err := r.writeEntry(r.sshConfig, r.kubeconfig, r.context, "--ca-file", r.caFile); err != nil {
		return r, err
	}
	r.alias = workspace + "." + namespace + "." + r.context + ".jk8s"
	if err := r.waitReachable(); err != nil {
		return r, fmt.Errorf("the workspace is not reachable through the ingress: %v", err)
	}
	return r, nil
}

// waitReachable polls a session through the entry until the ingress routes it. The
// Available condition is the operator's view; Traefik learns of the pod's endpoint a
// moment later and answers 503 until then.
func (r *rig) waitReachable() error {
	args := append([]string{"-F", r.sshConfig}, r.sshOptions()...)
	args = append(args, r.alias, "true")
	return harness.Poll(60*time.Second, 2*time.Second, func() (bool, string) {
		out, err := harness.Command("ssh", args...)
		return err == nil, strings.TrimSpace(out)
	})
}

// cleanup removes the fixtures and the rig's files; with E2E_KIND_KEEP set both stay,
// so the rig can be driven by hand with the kubeconfig and ssh_config it prints.
func (r *rig) cleanup() {
	r.stopForward()
	if os.Getenv("E2E_KIND_KEEP") != "" {
		fmt.Fprintf(os.Stderr, "kind rig kept: fixtures in the cluster, files in %s; its ssh_config needs\n"+
			"  kubectl --kubeconfig %s -n %s port-forward svc/traefik %s:443\n", r.dir, r.kubeconfig, traefikNS, r.port)
		return
	}
	if r.manifest != "" {
		_, _ = r.kubectlStdin(r.manifest, "delete", "--ignore-not-found", "--wait=false", "-f", "-")
	}
	if r.dir != "" {
		_ = os.RemoveAll(r.dir)
	}
}

// stopForward ends the port-forward and waits for it, after which its output is complete.
func (r *rig) stopForward() {
	if r.forward == nil || r.forward.Process == nil {
		return
	}
	_ = r.forward.Process.Kill()
	_ = r.forward.Wait()
	r.forward = nil
}

func (r *rig) kubectl(args ...string) (string, error) {
	return harness.Command("kubectl", append([]string{"--kubeconfig", r.kubeconfig}, args...)...)
}

func (r *rig) kubectlStdin(stdin string, args ...string) (string, error) {
	return harness.CommandWithStdin(stdin, "kubectl", append([]string{"--kubeconfig", r.kubeconfig}, args...)...)
}

// waitAvailable blocks until the workspace reports Available, up to readyTimeout.
func (r *rig) waitAvailable() (string, error) {
	return r.kubectl("-n", namespace, "wait", "--for=condition=Available",
		"--timeout="+readyTimeout.String(), "workspace/"+workspace)
}

// mustKubectl runs kubectl and fails the test on an error.
func (r *rig) mustKubectl(t *testing.T, args ...string) string {
	t.Helper()
	out, err := r.kubectl(args...)
	if err != nil {
		t.Fatalf("kubectl %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// writeEntry has the plugin write the SSH entry for the kubeconfig context into path.
func (r *rig) writeEntry(path, kubeconfig, context string, extra ...string) error {
	args := append([]string{"ssh-config", "--write", "--ssh-config-file", path,
		"--kubeconfig", kubeconfig, "--context", context, "--command", r.plugin}, extra...)
	if out, err := harness.Command(r.plugin, args...); err != nil {
		return fmt.Errorf("writing the SSH entry: %v\n%s", err, out)
	}
	return nil
}

// sshOptions keeps host keys in the rig's own file and silences OpenSSH's notices,
// so stderr carries only what the plugin writes.
func (r *rig) sshOptions() []string {
	return []string{"-o", "UserKnownHostsFile=" + r.knownHosts, "-o", "LogLevel=ERROR"}
}

// ssh runs the system's ssh through an entry the plugin wrote.
func (r *rig) ssh(t *testing.T, sshConfig string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	return harness.SSH(t, sshConfig, nil, append(r.sshOptions(), args...)...)
}

// serviceAccountKubeconfig returns a kubeconfig whose user is a service account with
// no RBAC, under the context "nobody".
func (r *rig) serviceAccountKubeconfig(t *testing.T, name string) string {
	t.Helper()
	account := fmt.Sprintf("apiVersion: v1\nkind: ServiceAccount\nmetadata:\n  name: %s\n  namespace: %s\n", name, namespace)
	if out, err := r.kubectlStdin(account, "apply", "-f", "-"); err != nil {
		t.Fatalf("creating the service account: %v\n%s", err, out)
	}
	token := strings.TrimSpace(r.mustKubectl(t, "-n", namespace, "create", "token", name, "--duration=15m"))
	server := strings.TrimSpace(r.mustKubectl(t, "config", "view", "--raw", "-o", "jsonpath={.clusters[0].cluster.server}"))
	caData := strings.TrimSpace(r.mustKubectl(t, "config", "view", "--raw", "-o",
		"jsonpath={.clusters[0].cluster.certificate-authority-data}"))
	ca, err := base64.StdEncoding.DecodeString(caData)
	if err != nil {
		t.Fatalf("the cluster's CA is not base64: %v", err)
	}
	return testutil.Kubeconfig{Name: "nobody", Server: server, CAPEM: string(ca), Namespace: namespace, Token: token}.Write(t)
}

func TestSSHReachesTheWorkspaceThroughTheIngress(t *testing.T) {
	stdout, stderr, code := shared.ssh(t, shared.sshConfig, shared.alias, "echo kind-ok")
	if code != 0 || stdout != "kind-ok\n" {
		t.Errorf("ssh exit %d, stdout %q (stderr %q)", code, stdout, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want silence on a clean session", stderr)
	}
}

func TestOpenPrintsTheLinkForTheInstalledEntry(t *testing.T) {
	out, err := harness.Command(shared.plugin, "open", "--print-only", "--kubeconfig", shared.kubeconfig,
		"--context", shared.context, "--ssh-config-file", shared.sshConfig, "--namespace", namespace, workspace)
	if err != nil {
		t.Fatalf("open --print-only: %v\n%s", err, out)
	}
	for _, want := range []string{"vscode://vscode-remote/ssh-remote+" + shared.alias, "ssh " + shared.alias} {
		if !strings.Contains(out, want) {
			t.Errorf("open printed %q, want it to contain %q", out, want)
		}
	}
}

func TestConnectRefusesTheIngressWithoutItsCA(t *testing.T) {
	sshConfig := filepath.Join(t.TempDir(), "ssh_config")
	if err := shared.writeEntry(sshConfig, shared.kubeconfig, shared.context); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := shared.ssh(t, sshConfig, shared.alias, "true")
	if code == 0 {
		t.Fatal("ssh succeeded without the rig's CA, so the ingress certificate was not verified")
	}
	if !strings.Contains(stderr, "certificate") {
		t.Errorf("stderr = %q, want the certificate verification failure", stderr)
	}
}

func TestConnectIsRefusedWithoutAccessToTheWorkspace(t *testing.T) {
	nobody := shared.serviceAccountKubeconfig(t, "e2e-nobody")
	sshConfig := filepath.Join(t.TempDir(), "ssh_config")
	if err := shared.writeEntry(sshConfig, nobody, "nobody", "--ca-file", shared.caFile); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := shared.ssh(t, sshConfig, workspace+"."+namespace+".nobody.jk8s", "true")
	if code == 0 {
		t.Fatal("ssh succeeded for a service account without access to the workspace")
	}
	if !strings.Contains(stderr, "HTTP 403") {
		t.Errorf("stderr = %q, want the refusal with HTTP 403", stderr)
	}
}

func TestConnectIsRefusedWhenTheWorkspaceIsStopped(t *testing.T) {
	setDesiredStatus := func(status string) {
		shared.mustKubectl(t, "-n", namespace, "patch", "workspace", workspace, "--type", "merge",
			"-p", fmt.Sprintf(`{"spec":{"desiredStatus":%q}}`, status))
	}
	setDesiredStatus("Stopped")
	t.Cleanup(func() {
		setDesiredStatus("Running")
		if out, err := shared.waitAvailable(); err != nil {
			t.Errorf("the workspace did not come back after the test: %v\n%s", err, out)
		}
	})
	// The operator answers from an informer cache, so the refusal is polled for
	// rather than inferred from the Workspace status.
	if err := harness.Poll(2*time.Minute, 2*time.Second, func() (bool, string) {
		_, stderr, code := shared.ssh(t, shared.sshConfig, shared.alias, "true")
		refused := code != 0 && strings.Contains(stderr, "HTTP 400") && strings.Contains(stderr, "not available")
		return refused, fmt.Sprintf("exit %d, stderr %q", code, stderr)
	}); err != nil {
		t.Errorf("the stopped workspace was not refused with the operator's HTTP 400: %v", err)
	}
}
