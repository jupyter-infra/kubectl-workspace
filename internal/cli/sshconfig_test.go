/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package cli

import (
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jupyter-infra/kubectl-workspace/internal/sshconfig"
	"github.com/jupyter-infra/kubectl-workspace/internal/testutil"
)

func TestSSHConfigWritesANonDefaultCredentialTimeout(t *testing.T) {
	kubeconfig := staticKubeconfig(t)
	stdout, err := run(t, "ssh-config", "--kubeconfig", kubeconfig, "--command", kwCommand, "--credential-timeout", "15s")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "--context dev --credential-timeout 15s %h") {
		t.Errorf("entry =\n%s\nwant the timeout in the ProxyCommand", stdout)
	}

	stdout, err = run(t, "ssh-config", "--kubeconfig", kubeconfig, "--command", kwCommand)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout, "--credential-timeout") {
		t.Errorf("entry =\n%s\nwant connect's default left implicit", stdout)
	}

	_, err = run(t, "ssh-config", "--kubeconfig", kubeconfig, "--command", kwCommand, "--credential-timeout", "0")
	if err == nil || !strings.Contains(err.Error(), "--credential-timeout must be positive") {
		t.Errorf("error = %v", err)
	}
}

func TestSSHConfigRendersTheEntryForTheContext(t *testing.T) {
	kubeconfig := staticKubeconfig(t)
	stdout, err := run(t, "ssh-config", "--kubeconfig", kubeconfig, "--command", kwCommand)
	if err != nil {
		t.Fatal(err)
	}
	want := sshconfig.Entry{
		Cluster: "dev", Context: "dev", Kubeconfig: kubeconfig, Command: kwCommand, User: "jovyan",
		Windows: runtime.GOOS == "windows",
	}.Render()
	if stdout != want {
		t.Errorf("stdout =\n%s\nwant\n%s", stdout, want)
	}
}

func TestSSHConfigWritesThePlaintextOptInIntoTheEntry(t *testing.T) {
	kubeconfig := staticKubeconfig(t)
	stdout, err := run(t, "ssh-config", "--kubeconfig", kubeconfig, "--command", kwCommand, "--insecure-allow-plaintext")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "--insecure-allow-plaintext %h") {
		t.Errorf("entry =\n%s\nwant connect's --insecure-allow-plaintext in the ProxyCommand", stdout)
	}
}

func TestSSHConfigNeedsAClusterNameForAnARNContext(t *testing.T) {
	kubeconfig := staticKubeconfig(t)

	_, err := run(t, "ssh-config", "--kubeconfig", kubeconfig, "--context", testutil.ARNContext, "--command", kwCommand)
	if err == nil || !strings.Contains(err.Error(), "--cluster-name") {
		t.Fatalf("error = %v, want a hint to pass --cluster-name", err)
	}

	stdout, err := run(t, "ssh-config", "--kubeconfig", kubeconfig, "--context", testutil.ARNContext,
		"--command", kwCommand, "--cluster-name", "dev")
	if err != nil {
		t.Fatal(err)
	}
	wantLine := "ProxyCommand " + kwCommand + " connect --stdio --kubeconfig " + kubeconfig +
		" --context " + testutil.ARNContext + " %h"
	if !strings.Contains(stdout, "Host *.dev.jk8s\n") || !strings.Contains(stdout, wantLine) {
		t.Errorf("stdout =\n%s\nwant Host *.dev.jk8s and %q", stdout, wantLine)
	}
}

func TestSSHConfigWritesTheKubeconfigIntoTheEntry(t *testing.T) {
	kubeconfig := staticKubeconfig(t)
	wantFlag := "--kubeconfig " + kubeconfig + " --context dev"
	tests := []struct {
		name       string
		env        string
		wantInFile bool
		wantStderr string
	}{
		{name: "single-file KUBECONFIG", env: kubeconfig, wantInFile: true},
		{
			name:       "KUBECONFIG list",
			env:        kubeconfig + string(os.PathListSeparator) + staticKubeconfig(t),
			wantStderr: "KUBECONFIG lists several files",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("KUBECONFIG", tt.env)
			stdout, stderr, err := runWithStderr(t, "ssh-config", "--command", kwCommand)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(stdout, wantFlag) != tt.wantInFile {
				t.Errorf("entry =\n%s\nwant --kubeconfig present: %v", stdout, tt.wantInFile)
			}
			if !strings.Contains(stderr, tt.wantStderr) {
				t.Errorf("stderr = %q, want %q", stderr, tt.wantStderr)
			}
		})
	}
}

// The installed entry is a command line OpenSSH runs verbatim, so it is run here
// the same way, with the alias in place of %h.
func TestInstalledEntryRunsConnect(t *testing.T) {
	echo := testutil.NewTLSEchoServer(t)
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, echo.CAPEM(), 0o600); err != nil {
		t.Fatal(err)
	}
	op := testutil.NewFakeOperator(t, http.StatusCreated,
		testutil.ConnectionBody(echo.URL(sshPath).String()+"?token=abc.def"))
	kubeconfig := testutil.Kubeconfig{Server: op.URL(), CAPEM: op.CAPEM(), Namespace: "team-a"}.Write(t)
	configFile := filepath.Join(t.TempDir(), ".ssh", "config")
	stdout, err := run(t, "ssh-config", "--write", "--kubeconfig", kubeconfig, "--context", testutil.ARNContext,
		"--cluster-name", "dev", "--command", kwCommand, "--ssh-config-file", configFile, "--ca-file", caFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "Connect with: ssh <workspace>.<namespace>.dev.jk8s") {
		t.Errorf("stdout = %q, want the alias form to connect with", stdout)
	}

	content, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatal(err)
	}
	var proxyCommand []string
	for _, line := range strings.Split(string(content), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "ProxyCommand "); ok {
			proxyCommand = shellWords(rest)
		}
	}
	if len(proxyCommand) == 0 || proxyCommand[0] != kwCommand {
		t.Fatalf("no ProxyCommand line in\n%s", content)
	}
	args := proxyCommand[1:]
	args[len(args)-1] = strings.ReplaceAll(args[len(args)-1], "%h", alias)

	r := startConnect(t, args...)
	if got := r.echo(t, "through the installed entry"); got != "through the installed entry" {
		t.Errorf("stdout = %q", got)
	}
	if err := r.finish(t); err != nil {
		t.Errorf("connect returned %v (args %q)", err, args)
	}
	if upgrade := echo.LastHandshake(t); upgrade.Authorization != "Bearer abc.def" {
		t.Errorf("WebSocket Authorization = %q", upgrade.Authorization)
	}
}

// shellWords splits a ProxyCommand line the way a POSIX shell would for the
// quoting ShellQuote produces: double quotes group, a backslash escapes inside them.
func shellWords(line string) []string {
	var words []string
	var current strings.Builder
	inWord, quoted := false, false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quoted && c == '\\' && i+1 < len(line):
			i++
			current.WriteByte(line[i])
		case c == '"':
			quoted = !quoted
			inWord = true
		case !quoted && (c == ' ' || c == '\t'):
			if inWord {
				words = append(words, current.String())
				current.Reset()
				inWord = false
			}
		default:
			current.WriteByte(c)
			inWord = true
		}
	}
	if inWord {
		words = append(words, current.String())
	}
	return words
}

func TestSSHConfigWritesTheCAFilePathAbsoluteIntoTheEntry(t *testing.T) {
	echo := testutil.NewTLSEchoServer(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ca.pem"), echo.CAPEM(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "junk.pem"), []byte("not a certificate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	kubeconfig := staticKubeconfig(t)
	file := filepath.Join(dir, "ssh_config")

	_, err := run(t, "ssh-config", "--write", "--ssh-config-file", file, "--kubeconfig", kubeconfig,
		"--command", kwCommand, "--ca-file", "junk.pem")
	if err == nil || !strings.Contains(err.Error(), "no certificates") {
		t.Errorf("error = %v, want the empty bundle refused", err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("the entry was written although the CA file was refused (stat: %v)", err)
	}

	if _, err := run(t, "ssh-config", "--write", "--ssh-config-file", file, "--kubeconfig", kubeconfig,
		"--command", kwCommand, "--ca-file", "ca.pem"); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if want := "--ca-file " + filepath.Join(dir, "ca.pem") + " %h"; !strings.Contains(string(written), want) {
		t.Errorf("entry =\n%s\nwant the relative CA file path made absolute: %s", written, want)
	}
}

func TestSSHConfigResolvesTheCommandPath(t *testing.T) {
	kubeconfig := staticKubeconfig(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	stdout, err := run(t, "ssh-config", "--kubeconfig", kubeconfig, "--command", "bin/kubectl-workspace")
	if err != nil {
		t.Fatal(err)
	}
	if want := "ProxyCommand " + filepath.Join(cwd, "bin", "kubectl-workspace") + " connect"; !strings.Contains(stdout, want) {
		t.Errorf("entry =\n%s\nwant the relative path made absolute: %s", stdout, want)
	}

	stdout, err = run(t, "ssh-config", "--kubeconfig", kubeconfig, "--command", "kubectl-workspace")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "ProxyCommand kubectl-workspace connect") {
		t.Errorf("entry =\n%s\nwant a bare name left to the PATH lookup", stdout)
	}
}
