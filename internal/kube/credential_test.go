/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package kube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	clientauthv1 "k8s.io/client-go/pkg/apis/clientauthentication/v1"
	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// fakePluginEnv selects the fake plugin's behavior when the test binary is
// re-executed as the exec credential plugin.
const fakePluginEnv = "KUBE_TEST_FAKE_PLUGIN"

const testAPIVersion = "client.authentication.k8s.io/v1"

func TestMain(m *testing.M) {
	if mode := os.Getenv(fakePluginEnv); mode != "" {
		os.Exit(fakePlugin(mode))
	}
	os.Exit(m.Run())
}

// fakePlugin echoes KUBERNETES_EXEC_INFO to stderr, then behaves as mode says.
func fakePlugin(mode string) int {
	fmt.Fprintln(os.Stderr, "exec-info:"+os.Getenv(execInfoEnv))
	credential := func(status string) int {
		fmt.Printf(`{"apiVersion":%q,"kind":"ExecCredential","status":%s}`, testAPIVersion, status)
		return 0
	}
	switch mode {
	case "sleep":
		time.Sleep(time.Minute)
		return 0
	case "sleep-with-child":
		child := exec.Command(os.Args[0])
		child.Env = append(os.Environ(), fakePluginEnv+"=sleep")
		if err := child.Start(); err != nil {
			return 2
		}
		fmt.Fprintf(os.Stderr, "child-pid:%d\n", child.Process.Pid)
		time.Sleep(time.Minute)
		return 0
	case "fail":
		return 3
	case "wrong-version":
		fmt.Printf(`{"apiVersion":"client.authentication.k8s.io/v1beta1","kind":"ExecCredential","status":{"token":"t"}}`)
		return 0
	case "no-status":
		fmt.Printf(`{"apiVersion":%q,"kind":"ExecCredential"}`, testAPIVersion)
		return 0
	case "cert-only":
		return credential(`{"clientCertificateData":"CERT"}`)
	case "credential-then-fail":
		credential(`{"token":"tok-123"}`)
		return 3
	case "cert":
		return credential(`{"clientCertificateData":"CERT","clientKeyData":"KEY"}`)
	case "stdin":
		data, _ := io.ReadAll(os.Stdin)
		return credential(fmt.Sprintf(`{"token":"stdin:%d"}`, len(data)))
	default:
		return credential(`{"token":"tok-123"}`)
	}
}

func fakeTarget(mode string, provideClusterInfo bool) *Target {
	return &Target{
		Context: "test",
		Config: &rest.Config{
			Host:            "https://example.invalid:6443",
			TLSClientConfig: rest.TLSClientConfig{CAData: []byte("CA")},
			ExecProvider: &clientcmdapi.ExecConfig{
				Command:            os.Args[0],
				APIVersion:         testAPIVersion,
				Env:                []clientcmdapi.ExecEnvVar{{Name: fakePluginEnv, Value: mode}},
				ProvideClusterInfo: provideClusterInfo,
				Config:             &k8sruntime.Unknown{Raw: []byte(`{"region":"us-west-2"}`)},
				InstallHint:        "install it from example.invalid",
				InteractiveMode:    clientcmdapi.IfAvailableExecInteractiveMode,
			},
		},
	}
}

// execInfoSeen parses the KUBERNETES_EXEC_INFO the fake plugin echoed.
func execInfoSeen(t *testing.T, stderr string) clientauthv1.ExecCredential {
	t.Helper()
	for _, line := range strings.Split(stderr, "\n") {
		if raw, ok := strings.CutPrefix(line, "exec-info:"); ok {
			var cred clientauthv1.ExecCredential
			if err := json.Unmarshal([]byte(raw), &cred); err != nil {
				t.Fatalf("invalid exec info %q: %v", raw, err)
			}
			return cred
		}
	}
	t.Fatalf("the plugin did not report its exec info; stderr:\n%s", stderr)
	return clientauthv1.ExecCredential{}
}

func TestResolveCredentialReplacesThePluginWithItsToken(t *testing.T) {
	target := fakeTarget("token", false)
	var stderr bytes.Buffer

	err := ResolveCredential(context.Background(), target, CredentialOptions{Stderr: &stderr, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if target.Config.BearerToken != "tok-123" {
		t.Errorf("BearerToken = %q, want tok-123", target.Config.BearerToken)
	}
	if target.Config.ExecProvider != nil {
		t.Error("ExecProvider still set; client-go would run the plugin again")
	}
	info := execInfoSeen(t, stderr.String())
	if info.APIVersion != testAPIVersion || info.Kind != "ExecCredential" {
		t.Errorf("exec info type = %s %s", info.APIVersion, info.Kind)
	}
	if info.Spec.Interactive {
		t.Error("exec info declared interactive for a non-interactive run")
	}
	if info.Spec.Cluster != nil {
		t.Error("exec info carried cluster info without provideClusterInfo")
	}
}

func TestResolveCredentialPassesClusterInfoWhenAsked(t *testing.T) {
	caFile := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(caFile, []byte("CA FROM FILE"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		tls    rest.TLSClientConfig
		wantCA string
	}{
		{name: "embedded CA data", tls: rest.TLSClientConfig{CAData: []byte("CA")}, wantCA: "CA"},
		{name: "CA file", tls: rest.TLSClientConfig{CAFile: caFile}, wantCA: "CA FROM FILE"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := fakeTarget("token", true)
			target.Config.TLSClientConfig = tt.tls
			var stderr bytes.Buffer
			if err := ResolveCredential(context.Background(), target, CredentialOptions{Stderr: &stderr}); err != nil {
				t.Fatal(err)
			}
			cluster := execInfoSeen(t, stderr.String()).Spec.Cluster
			if cluster == nil {
				t.Fatal("exec info carried no cluster")
			}
			if cluster.Server != "https://example.invalid:6443" || string(cluster.CertificateAuthorityData) != tt.wantCA {
				t.Errorf("exec info cluster = %s with CA %q, want the kubeconfig cluster with %q",
					cluster.Server, cluster.CertificateAuthorityData, tt.wantCA)
			}
			if string(cluster.Config.Raw) != `{"region":"us-west-2"}` {
				t.Errorf("exec info cluster config = %s, want the kubeconfig's exec config block", cluster.Config.Raw)
			}
		})
	}
}

func TestResolveCredentialGivesUpOnAPluginWaitingForASignIn(t *testing.T) {
	target := fakeTarget("sleep", false)
	start := time.Now()

	err := ResolveCredential(context.Background(), target, CredentialOptions{Timeout: 200 * time.Millisecond})

	var signIn *SignInRequiredError
	if !errors.As(err, &signIn) {
		t.Fatalf("error = %v, want *SignInRequiredError", err)
	}
	if signIn.Context != "test" || signIn.Timeout != 200*time.Millisecond {
		t.Errorf("error fields = %+v", signIn)
	}
	if !strings.Contains(err.Error(), "browser sign-in") {
		t.Errorf("message = %q, want it to name the browser sign-in", err)
	}
	// killDelay bounds how long a plugin that ignores SIGTERM can hold the connection.
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond+killDelay+2*time.Second {
		t.Errorf("gave up after %s, want about the timeout", elapsed)
	}
	if target.Config.ExecProvider == nil {
		t.Error("ExecProvider cleared although no credential was obtained")
	}
}

func TestResolveCredentialReportsACancelledContextAsSuch(t *testing.T) {
	target := fakeTarget("sleep", false)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	err := ResolveCredential(ctx, target, CredentialOptions{Timeout: 10 * time.Second})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled, not a plugin failure", err)
	}
	if target.Config.ExecProvider == nil {
		t.Error("ExecProvider cleared although no credential was obtained")
	}
}

func TestResolveCredentialKeepsStdinFromANonInteractivePlugin(t *testing.T) {
	target := fakeTarget("stdin", false)
	stdin := strings.NewReader("ssh bytes that belong to the tunnel")

	err := ResolveCredential(context.Background(), target, CredentialOptions{Stdin: stdin, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if target.Config.BearerToken != "stdin:0" {
		t.Errorf("the plugin read %q from stdin; a non-interactive plugin must get none", target.Config.BearerToken)
	}
	if stdin.Len() == 0 {
		t.Error("stdin was consumed")
	}
}

// pretendTerminal makes stdin count as a terminal for the test.
func pretendTerminal(t *testing.T, terminal bool) {
	t.Helper()
	previous := isTerminal
	isTerminal = func(io.Reader) bool { return terminal }
	t.Cleanup(func() { isTerminal = previous })
}

// The fake plugin's token counts the bytes it read from stdin: 5 for the "hello"
// handed over, 0 when it got no stdin.
func TestResolveCredentialFollowsTheKubeconfigsInteractiveMode(t *testing.T) {
	tests := []struct {
		name            string
		mode            clientcmdapi.ExecInteractiveMode
		terminal        bool
		wantTok         string
		wantInteractive bool
		wantErr         string
	}{
		{
			name: "IfAvailable with a terminal", mode: clientcmdapi.IfAvailableExecInteractiveMode, terminal: true,
			wantTok: "stdin:5", wantInteractive: true,
		},
		{name: "IfAvailable without a terminal", mode: clientcmdapi.IfAvailableExecInteractiveMode, wantTok: "stdin:0"},
		{name: "unset without a terminal", mode: "", wantTok: "stdin:0"},
		{name: "Never with a terminal", mode: clientcmdapi.NeverExecInteractiveMode, terminal: true, wantTok: "stdin:0"},
		{
			name: "Always with a terminal", mode: clientcmdapi.AlwaysExecInteractiveMode, terminal: true,
			wantTok: "stdin:5", wantInteractive: true,
		},
		{name: "Always without a terminal", mode: clientcmdapi.AlwaysExecInteractiveMode, wantErr: "requires a terminal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pretendTerminal(t, tt.terminal)
			target := fakeTarget("stdin", false)
			target.Config.ExecProvider.InteractiveMode = tt.mode
			var stderr bytes.Buffer
			opts := CredentialOptions{Interactive: true, Stdin: strings.NewReader("hello"), Stderr: &stderr}

			err := ResolveCredential(context.Background(), target, opts)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error = %v, want it to mention %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if target.Config.BearerToken != tt.wantTok {
				t.Errorf("token = %q, want %q", target.Config.BearerToken, tt.wantTok)
			}
			if got := execInfoSeen(t, stderr.String()).Spec.Interactive; got != tt.wantInteractive {
				t.Errorf("exec info interactive = %v, want %v", got, tt.wantInteractive)
			}
		})
	}
}

func TestIsTerminalIsFalseForAPipe(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if isTerminal(r) || isTerminal(strings.NewReader("")) {
		t.Error("a pipe or a buffer counted as a terminal")
	}
}

func TestResolveCredentialAppliesAClientCertificate(t *testing.T) {
	target := fakeTarget("cert", false)
	if err := ResolveCredential(context.Background(), target, CredentialOptions{}); err != nil {
		t.Fatal(err)
	}
	tls := target.Config.TLSClientConfig
	if string(tls.CertData) != "CERT" || string(tls.KeyData) != "KEY" {
		t.Errorf("TLS client data = %q / %q", tls.CertData, tls.KeyData)
	}
	if target.Config.BearerToken != "" {
		t.Errorf("BearerToken = %q, want empty", target.Config.BearerToken)
	}
}

func TestResolveCredentialRejectsBadPluginOutput(t *testing.T) {
	tests := []struct {
		mode    string
		wantErr string
	}{
		{mode: "fail", wantErr: "exited with status 3"},
		{mode: "wrong-version", wantErr: "returned \"client.authentication.k8s.io/v1beta1\""},
		{mode: "no-status", wantErr: "no status"},
		{mode: "cert-only", wantErr: "only one of certificate and key"},
		{mode: "credential-then-fail", wantErr: "exited with status 3"},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			target := fakeTarget(tt.mode, false)
			err := ResolveCredential(context.Background(), target, CredentialOptions{Timeout: 10 * time.Second})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want it to mention %q", err, tt.wantErr)
			}
			if target.Config.ExecProvider == nil {
				t.Error("ExecProvider cleared although no credential was obtained")
			}
		})
	}
}

func TestResolveCredentialReportsAMissingPluginWithTheInstallHint(t *testing.T) {
	target := fakeTarget("token", false)
	target.Config.ExecProvider.Command = "kubectl-workspace-test-missing-plugin"

	err := ResolveCredential(context.Background(), target, CredentialOptions{})
	if err == nil || !strings.Contains(err.Error(), "install it from example.invalid") {
		t.Errorf("error = %v, want the kubeconfig's install hint", err)
	}
}

func TestResolveCredentialReportsAPluginThatCannotStart(t *testing.T) {
	target := fakeTarget("token", false)
	// A file that exists but cannot run: no execute bit on Unix, not a program on Windows.
	plugin := filepath.Join(t.TempDir(), "plugin")
	if runtime.GOOS == "windows" {
		plugin += ".exe"
	}
	if err := os.WriteFile(plugin, []byte("not a program"), 0o600); err != nil {
		t.Fatal(err)
	}
	target.Config.ExecProvider.Command = plugin

	err := ResolveCredential(context.Background(), target, CredentialOptions{})
	if err == nil || !strings.Contains(err.Error(), "failed to run credential plugin") {
		t.Errorf("error = %v", err)
	}
}

func TestResolveCredentialWithoutAPluginIsANoOp(t *testing.T) {
	target := &Target{Context: "static", Config: &rest.Config{Host: "https://example.invalid", BearerToken: "static"}}
	if err := ResolveCredential(context.Background(), target, CredentialOptions{}); err != nil {
		t.Fatal(err)
	}
	if target.Config.BearerToken != "static" {
		t.Errorf("BearerToken = %q, want static", target.Config.BearerToken)
	}
}

func TestResolveCredentialTerminatesThePluginsChildrenAtTheDeadline(t *testing.T) {
	target := fakeTarget("sleep-with-child", false)
	var stderr bytes.Buffer

	err := ResolveCredential(context.Background(), target, CredentialOptions{Stderr: &stderr, Timeout: time.Second})
	var signIn *SignInRequiredError
	if !errors.As(err, &signIn) {
		t.Fatalf("error = %v, want *SignInRequiredError", err)
	}

	var childPID int
	for _, line := range strings.Split(stderr.String(), "\n") {
		if raw, ok := strings.CutPrefix(line, "child-pid:"); ok {
			childPID, _ = strconv.Atoi(raw)
		}
	}
	if childPID == 0 {
		t.Fatalf("the plugin did not report its child; stderr:\n%s", stderr.String())
	}
	// On Unix a zombie still answers until init reaps it once the plugin is gone.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if processGone(childPID) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	if child, err := os.FindProcess(childPID); err == nil {
		_ = child.Kill()
	}
	t.Errorf("the plugin's child %d survived the deadline", childPID)
}

func TestProcessTreeReportsAFinishedPluginAsDone(t *testing.T) {
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), fakePluginEnv+"=token")
	tree := newProcessTree(cmd, true)
	defer tree.close()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	tree.attach()
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := tree.terminate(); !errors.Is(err, os.ErrProcessDone) {
		t.Errorf("terminate(finished plugin) = %v, want os.ErrProcessDone", err)
	}
}
