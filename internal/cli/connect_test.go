/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package cli

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jupyter-infra/kubectl-workspace/internal/testutil"
)

func TestConnectRelaysThroughTheDirectURL(t *testing.T) {
	echo := testutil.NewEchoServer(t)
	r := startConnect(t, "connect", "--stdio", "--url", echo.URL("/").String(), alias)

	if got := r.echo(t, "SSH-2.0-OpenSSH_10.5\r\n"); got != "SSH-2.0-OpenSSH_10.5\r\n" {
		t.Errorf("stdout = %q", got)
	}
	if err := r.finish(t); err != nil {
		t.Errorf("connect returned %v", err)
	}
	if upgrade := echo.LastHandshake(t); upgrade.Authorization != "" {
		t.Errorf("Authorization = %q, want none for a direct URL", upgrade.Authorization)
	}
	if r.stderr.Len() != 0 {
		t.Errorf("stderr = %q, want silence on a clean session", r.stderr.String())
	}
}

func TestConnectUsesTheOperatorsTokenInAHeaderAndNeverLogsIt(t *testing.T) {
	echo := testutil.NewEchoServer(t)
	op := testutil.NewFakeOperator(t, http.StatusCreated,
		testutil.ConnectionBody(echo.URL(sshPath).String()+"?token=abc.def"))
	kubeconfig := testutil.Kubeconfig{Server: op.URL(), CAPEM: op.CAPEM(), Namespace: "team-a"}.Write(t)
	r := startConnect(t, "-v", "connect", "--stdio", "--kubeconfig", kubeconfig, alias)

	if got := r.echo(t, "hello"); got != "hello" {
		t.Errorf("stdout = %q", got)
	}
	if err := r.finish(t); err != nil {
		t.Errorf("connect returned %v", err)
	}

	req := op.LastRequest(t)
	if req.Path != wantAPIPath || req.Authorization != "Bearer "+testutil.Token {
		t.Errorf("operator saw %s with %q", req.Path, req.Authorization)
	}
	if req.Body.Spec.WorkspaceName != "ws1" {
		t.Errorf("operator saw workspace %q", req.Body.Spec.WorkspaceName)
	}
	upgrade := echo.LastHandshake(t)
	if upgrade.Authorization != "Bearer abc.def" {
		t.Errorf("WebSocket Authorization = %q, want the operator's token", upgrade.Authorization)
	}
	if upgrade.RawQuery != "" {
		t.Errorf("WebSocket query = %q, want the token removed from the URL", upgrade.RawQuery)
	}
	if strings.Contains(r.stderr.String(), "abc.def") {
		t.Errorf("stderr leaks the token: %q", r.stderr.String())
	}
	if !strings.Contains(r.stderr.String(), "connection granted") {
		t.Errorf("stderr = %q, want the verbose progress lines", r.stderr.String())
	}
}

func TestConnectKeepsStdoutForTheSSHStreamWhenVerbose(t *testing.T) {
	echo := testutil.NewEchoServer(t)
	r := startConnect(t, "-v", "connect", "--stdio", "--url", echo.URL("/").String(), alias)

	got := r.echo(t, "only these bytes")
	if err := r.finish(t); err != nil {
		t.Fatal(err)
	}
	if rest, _ := io.ReadAll(r.stdout); got+string(rest) != "only these bytes" {
		t.Errorf("stdout = %q, want the echoed bytes alone", got+string(rest))
	}
	if !strings.Contains(r.stderr.String(), "kubectl-workspace: connected to ") {
		t.Errorf("stderr = %q, want the progress line", r.stderr.String())
	}
}

func TestConnectReportsTheWorkspacesCloseReason(t *testing.T) {
	echo := testutil.NewEchoServer(t)
	echo.SetOnConn(testutil.CloseWithReason("session duration limit reached"))
	r := startConnect(t, "connect", "--stdio", "--url", echo.URL("/").String(), alias)

	if err := r.finish(t); err != nil {
		t.Errorf("connect returned %v, want nil for a close the workspace explained", err)
	}
	if !strings.Contains(r.stderr.String(), "the workspace closed the connection: session duration limit reached") {
		t.Errorf("stderr = %q", r.stderr.String())
	}
}

func TestConnectReportsTheOperatorsRefusal(t *testing.T) {
	op := testutil.NewFakeOperator(t, http.StatusForbidden, testutil.StatusBody(403, "user is not the owner"))
	kubeconfig := testutil.Kubeconfig{Server: op.URL(), CAPEM: op.CAPEM(), Namespace: "team-a"}.Write(t)

	_, err := run(t, "connect", "--stdio", "--kubeconfig", kubeconfig, alias)
	if err == nil || !strings.Contains(err.Error(), "user is not the owner") {
		t.Errorf("error = %v, want the operator's message", err)
	}
}

func TestConnectRequiresTheStdioFlag(t *testing.T) {
	_, err := run(t, "connect", alias)
	if err == nil || !strings.Contains(err.Error(), "stdio") {
		t.Errorf("error = %v, want the missing --stdio flag", err)
	}
}

func TestConnectRejectsAHostThatIsNotAnAlias(t *testing.T) {
	_, err := run(t, "connect", "--stdio", "--url", "ws://127.0.0.1:1/", "github.com")
	if err == nil || !strings.Contains(err.Error(), "not a workspace alias") {
		t.Errorf("error = %v", err)
	}
}

func TestConnectFailsFastWhenTheCredentialNeedsASignIn(t *testing.T) {
	// A plugin that never answers stands in for one waiting for the browser.
	kubeconfig := pluginKubeconfig(t, "sleep")
	tests := []struct {
		name     string
		context  string
		wantHint string
	}{
		{
			name:     "context usable as the cluster name",
			context:  "dev",
			wantHint: "kubectl workspace open ws1 --namespace team-a --kubeconfig " + kubeconfig + " --context dev",
		},
		{
			name:    "EKS ARN context needs the cluster name",
			context: testutil.ARNContext,
			wantHint: "kubectl workspace open ws1 --namespace team-a --kubeconfig " + kubeconfig +
				" --context " + testutil.ARNContext + " --cluster-name dev",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start := time.Now()
			_, err := run(t, "connect", "--stdio", "--kubeconfig", kubeconfig, "--context", tt.context,
				"--credential-timeout", "300ms", alias)
			if err == nil {
				t.Fatal("expected an error")
			}
			slowHint := "kubectl workspace ssh-config --write --credential-timeout 900ms --kubeconfig " + kubeconfig
			for _, want := range []string{"browser sign-in", tt.wantHint, slowHint} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Errorf("connect took %s, want it to give up at the credential timeout", elapsed)
			}
		})
	}
}

func TestConnectAttachesTheSignInHintToAFailingPlugin(t *testing.T) {
	kubeconfig := pluginKubeconfig(t, "fail")

	_, err := run(t, "connect", "--stdio", "--kubeconfig", kubeconfig, alias)
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"exited with status 3", "kubectl workspace open ws1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "--credential-timeout") {
		t.Errorf("error %q suggests a longer deadline for a plugin that failed, not one that was slow", err)
	}
}

// pluginKubeconfig writes a kubeconfig whose credential plugin is this test binary
// in the given pluginModeEnv mode.
func pluginKubeconfig(t *testing.T, mode string) string {
	t.Helper()
	return testutil.Kubeconfig{
		Server: "https://127.0.0.1:1", Namespace: "team-a",
		Exec: &testutil.ExecPlugin{Command: os.Args[0], Env: map[string]string{pluginModeEnv: mode}},
	}.Write(t)
}

// OpenSSH ends the ProxyCommand with SIGHUP, which cancels the command's context,
// whichever phase the connection is in.
func TestConnectEndsQuietlyWhenSignalled(t *testing.T) {
	tests := []struct {
		name  string
		start func(t *testing.T) *connectRun
	}{
		{name: "during the session", start: func(t *testing.T) *connectRun {
			echo := testutil.NewEchoServer(t)
			r := startConnect(t, "connect", "--stdio", "--url", echo.URL("/").String(), alias)
			r.echo(t, "ping")
			return r
		}},
		{name: "during the sign-in", start: func(t *testing.T) *connectRun {
			kubeconfig := pluginKubeconfig(t, "sleep")
			r := startConnect(t, "connect", "--stdio", "--kubeconfig", kubeconfig, "--credential-timeout", "20s", alias)
			time.Sleep(300 * time.Millisecond)
			return r
		}},
		{name: "during the dial", start: func(t *testing.T) *connectRun {
			r := startConnect(t, "connect", "--stdio", "--url", "ws://"+testutil.StartSilentListener(t)+"/", alias)
			time.Sleep(200 * time.Millisecond)
			return r
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := tt.start(t)
			r.cancel()
			if err := r.wait(t); err != nil {
				t.Errorf("connect returned %v after the signal, want nil", err)
			}
			if r.stderr.Len() != 0 {
				t.Errorf("stderr = %q, want silence", r.stderr.String())
			}
		})
	}
}

func TestConnectDoesNotEchoATokenGivenInTheURL(t *testing.T) {
	_, err := run(t, "connect", "--stdio", "--url", "ws://127.0.0.1:1/?token=SECRET-TOKEN", alias)
	if err == nil {
		t.Fatal("expected the dial to fail")
	}
	if strings.Contains(err.Error(), "SECRET-TOKEN") {
		t.Errorf("error %q leaks the token", err)
	}
}

func TestConnectDoesNotSuggestASignInForAMissingPlugin(t *testing.T) {
	kubeconfig := testutil.Kubeconfig{
		Server:    "https://127.0.0.1:1",
		Namespace: "team-a",
		Exec: &testutil.ExecPlugin{
			Command: "kubectl-workspace-test-missing-plugin", InstallHint: "install it from example.invalid",
		},
	}.Write(t)

	_, err := run(t, "connect", "--stdio", "--kubeconfig", kubeconfig, alias)
	if err == nil || !strings.Contains(err.Error(), "install it from example.invalid") {
		t.Fatalf("error = %v, want the kubeconfig's install hint", err)
	}
	if strings.Contains(err.Error(), "Sign in from a terminal") {
		t.Errorf("error %q suggests a sign-in that would run the same missing plugin", err)
	}
}

func TestConnectRejectsANonPositiveCredentialTimeout(t *testing.T) {
	_, err := run(t, "connect", "--stdio", "--kubeconfig", staticKubeconfig(t), "--credential-timeout", "0", alias)
	if err == nil || !strings.Contains(err.Error(), "--credential-timeout must be positive") {
		t.Errorf("error = %v", err)
	}
}

func TestConnectTrustsTheCAFile(t *testing.T) {
	echo := testutil.NewTLSEchoServer(t)
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, echo.CAPEM(), 0o600); err != nil {
		t.Fatal(err)
	}
	r := startConnect(t, "connect", "--stdio", "--url", echo.URL("/").String(), "--ca-file", caFile, alias)
	if got := r.echo(t, "through the private ca"); got != "through the private ca" {
		t.Errorf("echoed %q", got)
	}
	if err := r.finish(t); err != nil {
		t.Errorf("connect returned %v", err)
	}
}

func TestConnectRejectsAMissingCAFileBeforeConnecting(t *testing.T) {
	_, err := run(t, "connect", "--stdio", "--url", "wss://127.0.0.1:1/", "--ca-file", "/nonexistent/ca.pem", alias)
	if err == nil || !strings.Contains(err.Error(), "CA file") {
		t.Errorf("error = %v, want the CA file named", err)
	}
}
