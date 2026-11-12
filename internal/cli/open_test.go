/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package cli

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jupyter-infra/kubectl-workspace/internal/sshconfig"
	"github.com/jupyter-infra/kubectl-workspace/internal/testutil"
)

func TestOpenPrintOnlyPrintsTheLinkAndTheSSHCommand(t *testing.T) {
	kubeconfig := staticKubeconfig(t)

	stdout, err := run(t, "open", "--kubeconfig", kubeconfig, "--print-only", "ws1")
	if err != nil {
		t.Fatal(err)
	}
	want := "vscode://vscode-remote/ssh-remote+ws1.team-a.dev.jk8s/home/jovyan?windowId=_blank\nssh ws1.team-a.dev.jk8s\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}

	stdout, err = run(t, "open", "--kubeconfig", kubeconfig, "--print-only", "-n", "other",
		"--remote-path", "/home/jovyan/my project#1", "ws1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stdout, "vscode://vscode-remote/ssh-remote+ws1.other.dev.jk8s/home/jovyan/my%20project%231?") {
		t.Errorf("stdout = %q, want the path escaped for the link", stdout)
	}
}

func TestOpenBuildsTheLinkForTheChosenEditor(t *testing.T) {
	kubeconfig := staticKubeconfig(t)
	for _, editor := range []string{"vscode", "cursor"} {
		stdout, err := run(t, "open", "--kubeconfig", kubeconfig, "--print-only", "--editor", editor, "ws1")
		if err != nil {
			t.Fatal(err)
		}
		want := editor + "://vscode-remote/ssh-remote+ws1.team-a.dev.jk8s/home/jovyan?windowId=_blank\n"
		if !strings.HasPrefix(stdout, want) {
			t.Errorf("--editor %s: stdout = %q, want it to start with %q", editor, stdout, want)
		}
	}
	_, err := run(t, "open", "--kubeconfig", kubeconfig, "--print-only", "--editor", "emacs", "ws1")
	if err == nil || !strings.Contains(err.Error(), `unknown --editor "emacs", supported: cursor, vscode`) {
		t.Errorf("error = %v, want the supported editors listed", err)
	}
}

func TestOpenDefaultsTheNamespaceWhenTheContextHasNone(t *testing.T) {
	kubeconfig := testutil.Kubeconfig{Server: "https://127.0.0.1:1"}.Write(t)

	stdout, err := run(t, "open", "--kubeconfig", kubeconfig, "--print-only", "ws1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stdout, "vscode://vscode-remote/ssh-remote+ws1.default.dev.jk8s/") {
		t.Errorf("stdout = %q, want the default namespace in the alias", stdout)
	}
}

func TestOpenRejectsARelativeRemotePath(t *testing.T) {
	_, err := run(t, "open", "--kubeconfig", staticKubeconfig(t), "--print-only", "--remote-path", "project", "ws1")
	if err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Errorf("error = %v", err)
	}
}

func TestOpenFailsWithTheSetupCommandWhenTheEntryIsMissing(t *testing.T) {
	kubeconfig := staticKubeconfig(t)
	configFile := filepath.Join(t.TempDir(), "config")
	tests := []struct {
		name     string
		args     []string
		wantHint string
	}{
		{
			name: "context usable as the cluster name",
			args: nil,
			wantHint: "kubectl workspace ssh-config --write --ssh-config-file " + configFile +
				" --kubeconfig " + kubeconfig + " --context dev",
		},
		{
			name: "EKS ARN context needs the cluster name",
			args: []string{"--context", testutil.ARNContext, "--cluster-name", "dev"},
			wantHint: "kubectl workspace ssh-config --write --ssh-config-file " + configFile +
				" --kubeconfig " + kubeconfig + " --context " + testutil.ARNContext + " --cluster-name dev",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The hint has to name the file open looked in, or a rerun installs elsewhere.
			args := append([]string{"open", "--kubeconfig", kubeconfig, "--ssh-config-file", configFile}, tt.args...)
			_, err := run(t, append(args, "ws1")...)
			if err == nil || !strings.Contains(err.Error(), tt.wantHint) {
				t.Errorf("error = %v, want the setup command %q", err, tt.wantHint)
			}
		})
	}
}

// installedEntry writes a configuration file holding the entry for cluster dev.
func installedEntry(t *testing.T) string {
	t.Helper()
	configFile := filepath.Join(t.TempDir(), "config")
	entry := sshconfig.Entry{Cluster: "dev", Context: "dev", Command: "/opt/kw", User: "jovyan"}
	if err := sshconfig.Install(configFile, entry); err != nil {
		t.Fatal(err)
	}
	return configFile
}

// recordLaunches replaces the editor launcher for the test and returns the links it received.
func recordLaunches(t *testing.T) *[]string {
	t.Helper()
	var launched []string
	previous := launchURL
	launchURL = func(_ context.Context, link string) error {
		launched = append(launched, link)
		return nil
	}
	t.Cleanup(func() { launchURL = previous })
	return &launched
}

func TestOpenChecksTheConnectionThenLaunchesTheEditor(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantName string
		wantLink string
	}{
		{
			name: "VS Code by default", wantName: "Opening VS Code on",
			wantLink: "vscode://vscode-remote/ssh-remote+ws1.team-a.dev.jk8s/home/jovyan?windowId=_blank",
		},
		{
			name: "Cursor when asked", args: []string{"--editor", "cursor"}, wantName: "Opening Cursor on",
			wantLink: "cursor://vscode-remote/ssh-remote+ws1.team-a.dev.jk8s/home/jovyan?windowId=_blank",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			op := testutil.NewFakeOperator(t, http.StatusCreated,
				testutil.ConnectionBody("wss://jk8s.example.com"+sshPath+"?token=abc"))
			kubeconfig := testutil.Kubeconfig{Server: op.URL(), CAPEM: op.CAPEM(), Namespace: "team-a"}.Write(t)
			launched := recordLaunches(t)

			args := append([]string{"open", "--kubeconfig", kubeconfig, "--ssh-config-file", installedEntry(t)}, tt.args...)
			stdout, err := run(t, append(args, "ws1")...)
			if err != nil {
				t.Fatal(err)
			}
			if req := op.LastRequest(t); req.Path != wantAPIPath {
				t.Errorf("the operator saw %q, want %q", req.Path, wantAPIPath)
			}
			if len(*launched) != 1 || (*launched)[0] != tt.wantLink {
				t.Errorf("launched %q, want %q", *launched, tt.wantLink)
			}
			for _, want := range []string{tt.wantName, "ssh ws1.team-a.dev.jk8s"} {
				if !strings.Contains(stdout, want) {
					t.Errorf("stdout = %q, want it to mention %q", stdout, want)
				}
			}
		})
	}
}

func TestOpenDoesNotLaunchWhenTheOperatorRefuses(t *testing.T) {
	op := testutil.NewFakeOperator(t, http.StatusBadRequest,
		testutil.StatusBody(400, "workspace is not available. Check workspace status for details"))
	kubeconfig := testutil.Kubeconfig{Server: op.URL(), CAPEM: op.CAPEM(), Namespace: "team-a"}.Write(t)
	launched := recordLaunches(t)

	_, err := run(t, "open", "--kubeconfig", kubeconfig, "--ssh-config-file", installedEntry(t), "ws1")
	if err == nil || !strings.Contains(err.Error(), "workspace is not available") {
		t.Errorf("error = %v, want the operator's message", err)
	}
	if len(*launched) != 0 {
		t.Error("the editor was launched although the workspace is not reachable")
	}
}
