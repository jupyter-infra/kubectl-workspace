/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package sshconfig

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testEntry() Entry {
	return Entry{
		Cluster: "prod",
		Context: "prod",
		Command: "/Users/me/.krew/bin/kubectl-workspace",
		User:    DefaultUser,
	}
}

func TestRender(t *testing.T) {
	want := strings.Join([]string{
		"# kubectl-workspace: start prod",
		"Host *.prod.jk8s",
		"  User jovyan",
		"  ProxyCommand /Users/me/.krew/bin/kubectl-workspace connect --stdio --context prod %h",
		"  ServerAliveInterval 30",
		"  StrictHostKeyChecking accept-new",
		"# kubectl-workspace: end prod",
		"",
	}, "\n")
	if got := testEntry().Render(); got != want {
		t.Errorf("Render() =\n%s\nwant\n%s", got, want)
	}
}

func TestRenderProxyCommandLine(t *testing.T) {
	tests := []struct {
		name    string
		entry   Entry
		wantCmd string
	}{
		{
			name:    "EKS ARN context needs no quotes",
			entry:   Entry{Cluster: "dev", Context: "arn:aws:eks:us-west-2:123456789012:cluster/dev", Command: "/opt/kw"},
			wantCmd: "ProxyCommand /opt/kw connect --stdio --context arn:aws:eks:us-west-2:123456789012:cluster/dev %h",
		},
		{
			name:    "path with a space is quoted",
			entry:   Entry{Cluster: "dev", Context: "dev", Command: "/Applications/My Tools/kubectl-workspace"},
			wantCmd: `ProxyCommand "/Applications/My Tools/kubectl-workspace" connect --stdio --context dev %h`,
		},
		{
			name:    "shell metacharacters are escaped inside the quotes",
			entry:   Entry{Cluster: "dev", Context: `my "ctx" $HOME`, Command: "/opt/kw"},
			wantCmd: `ProxyCommand /opt/kw connect --stdio --context "my \"ctx\" \$HOME" %h`,
		},
		{
			name:    "kubeconfig file travels in the entry",
			entry:   Entry{Cluster: "dev", Context: "dev", Kubeconfig: "/Users/me/.kube/eks.yaml", Command: "/opt/kw"},
			wantCmd: "ProxyCommand /opt/kw connect --stdio --kubeconfig /Users/me/.kube/eks.yaml --context dev %h",
		},
		{
			name:    "percent is doubled for OpenSSH's token expansion",
			entry:   Entry{Cluster: "dev", Context: "100%dev", Command: "/opt/kw"},
			wantCmd: "ProxyCommand /opt/kw connect --stdio --context 100%%dev %h",
		},
		{
			name:    "credential timeout written when set",
			entry:   Entry{Cluster: "dev", Context: "dev", Command: "/opt/kw", CredentialTimeout: 15 * time.Second},
			wantCmd: "ProxyCommand /opt/kw connect --stdio --context dev --credential-timeout 15s %h",
		},
		{
			name:    "CA file written when set",
			entry:   Entry{Cluster: "dev", Context: "dev", Command: "/opt/kw", CAFile: "/etc/kw/ingress-ca.pem"},
			wantCmd: "ProxyCommand /opt/kw connect --stdio --context dev --ca-file /etc/kw/ingress-ca.pem %h",
		},
		{
			name:    "windows path keeps its backslashes",
			entry:   Entry{Cluster: "dev", Context: "dev", Command: `C:\Users\me\.krew\bin\kubectl-workspace.exe`, Windows: true},
			wantCmd: `ProxyCommand C:\Users\me\.krew\bin\kubectl-workspace.exe connect --stdio --context dev %h`,
		},
		{
			name:    "windows path with a space is quoted without escapes",
			entry:   Entry{Cluster: "dev", Context: "dev", Command: `C:\Program Files\kw\kubectl-workspace.exe`, Windows: true},
			wantCmd: `ProxyCommand "C:\Program Files\kw\kubectl-workspace.exe" connect --stdio --context dev %h`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.entry.Render(); !strings.Contains(got, "  "+tt.wantCmd+"\n") {
				t.Errorf("Render() =\n%s\nwant a line %q", got, tt.wantCmd)
			}
		})
	}
}

func TestValidateKeepsTheUserOneToken(t *testing.T) {
	// OpenSSH aborts on a second token after User and expands % in its value.
	for _, user := range []string{"a b", "jovyan%h", `"quoted"`, ""} {
		entry := testEntry()
		entry.User = user
		if err := entry.Validate(); err == nil || !strings.Contains(err.Error(), "user name") {
			t.Errorf("Validate() with user %q = %v, want a user name error", user, err)
		}
	}
	for _, user := range []string{"jovyan", "ml.user_1", "a-b"} {
		entry := testEntry()
		entry.User = user
		if err := entry.Validate(); err != nil {
			t.Errorf("Validate() with user %q = %v", user, err)
		}
	}
}

func TestShellQuote(t *testing.T) {
	tests := map[string]string{
		"/opt/kw":                           "/opt/kw",
		"arn:aws:eks:us-west-2:1:cluster/d": "arn:aws:eks:us-west-2:1:cluster/d",
		"/My Files/kubeconfig":              `"/My Files/kubeconfig"`,
		`say "hi" $HOME`:                    `"say \"hi\" \$HOME"`,
	}
	for in, want := range tests {
		if got := ShellQuote(in); got != want {
			t.Errorf("ShellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidateRejectsLineBreaks(t *testing.T) {
	entry := testEntry()
	entry.Context = "dev\n  ProxyCommand evil"
	if err := entry.Validate(); err == nil || !strings.Contains(err.Error(), "line break") {
		t.Errorf("Validate() = %v, want a line-break error", err)
	}
	if err := Install(filepath.Join(t.TempDir(), "config"), entry); err == nil {
		t.Error("Install accepted an entry that would split into a second directive")
	}
	if err := testEntry().Validate(); err != nil {
		t.Errorf("Validate() on a plain entry = %v", err)
	}
}
