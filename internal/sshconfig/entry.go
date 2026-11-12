/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package sshconfig

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// DefaultUser is the SSH user name written into the entry. The remote access
// server accepts any user name, but without one OpenSSH sends the local login.
const DefaultUser = "jovyan"

// Entry is the SSH configuration entry for one cluster. It matches every
// workspace alias under the cluster and hands the connection to kubectl-workspace.
type Entry struct {
	// Cluster is the alias label the entry matches: Host *.<Cluster>.jk8s.
	Cluster string
	// Context is the kubeconfig context connect uses for this cluster.
	Context string
	// Kubeconfig is the kubeconfig file connect reads, empty for the default
	// lookup. OpenSSH does not run the ProxyCommand in the user's shell session,
	// so a file chosen through a flag or KUBECONFIG has to be named here.
	Kubeconfig string
	// Command is the path of the kubectl-workspace binary.
	Command string
	// User is the SSH user name.
	User string
	// CredentialTimeout, when set, is written as connect's --credential-timeout.
	CredentialTimeout time.Duration
	// CAFile, when set, is written as connect's --ca-file.
	CAFile string
	// Windows selects the argument quoting for OpenSSH on Windows.
	Windows bool
}

// HostPattern returns the Host pattern the entry matches.
func (e Entry) HostPattern() string {
	return "*." + e.Cluster + "." + AliasSuffix
}

// userPattern keeps the User value one ssh_config token: OpenSSH aborts on a
// second token and expands % in it.
var userPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// windowsUnsafe lists what WindowsQuote cannot carry: a double quote ends the
// argument, and cmd.exe or PowerShell act on the others in a pasted hint.
const windowsUnsafe = "\"&|<>^%"

// Validate rejects values that cannot sit on one ssh_config line and, for a Windows
// entry, the characters its quoting cannot carry.
func (e Entry) Validate() error {
	fields := []struct{ name, value string }{
		{"cluster name", e.Cluster}, {"context name", e.Context}, {"kubeconfig path", e.Kubeconfig},
		{"command path", e.Command}, {"user name", e.User}, {"CA file path", e.CAFile},
	}
	for _, field := range fields {
		if strings.ContainsAny(field.value, "\r\n") {
			return fmt.Errorf("the %s %q contains a line break", field.name, field.value)
		}
		if i := strings.IndexAny(field.value, windowsUnsafe); e.Windows && i >= 0 {
			return fmt.Errorf("the %s %q contains %q, which the ProxyCommand cannot carry on Windows; "+
				"rename it or use a simpler path", field.name, field.value, string(field.value[i]))
		}
	}
	if !userPattern.MatchString(e.User) {
		return fmt.Errorf("the user name %q must be letters, digits, '.', '_' or '-'", e.User)
	}
	return ValidateClusterName(e.Cluster)
}

// Render returns the entry text, delimited by marker comments that Install uses
// to find and replace it.
func (e Entry) Render() string {
	var b strings.Builder
	fmt.Fprintln(&b, startMarker(e.Cluster))
	fmt.Fprintf(&b, "Host %s\n", e.HostPattern())
	fmt.Fprintf(&b, "  User %s\n", e.User)
	args := []string{quoteArg(e.Command, e.Windows), "connect", "--stdio"}
	if e.Kubeconfig != "" {
		args = append(args, "--kubeconfig", quoteArg(e.Kubeconfig, e.Windows))
	}
	args = append(args, "--context", quoteArg(e.Context, e.Windows))
	if e.CAFile != "" {
		args = append(args, "--ca-file", quoteArg(e.CAFile, e.Windows))
	}
	if e.CredentialTimeout > 0 {
		args = append(args, "--credential-timeout", e.CredentialTimeout.String())
	}
	// %h is OpenSSH's token for the host name given on the command line.
	args = append(args, "%h")
	fmt.Fprintf(&b, "  ProxyCommand %s\n", strings.Join(args, " "))
	// The sidecar pings the client, which keeps the load balancer from cutting an
	// idle tunnel, but only a keepalive of its own tells ssh that the tunnel died.
	fmt.Fprintln(&b, "  ServerAliveInterval 30")
	// The host key is trusted on first use; the operator does not publish it yet.
	fmt.Fprintln(&b, "  StrictHostKeyChecking accept-new")
	fmt.Fprintln(&b, endMarker(e.Cluster))
	return b.String()
}

const startMarkerPrefix = "# kubectl-workspace: start "

func startMarker(cluster string) string { return startMarkerPrefix + cluster }

func endMarker(cluster string) string { return "# kubectl-workspace: end " + cluster }

// plainArg matches arguments the shell passes through unchanged.
var plainArg = regexp.MustCompile(`^[A-Za-z0-9_./:@%+=,-]+$`)

// ShellQuote quotes s for a POSIX shell: unchanged when plain, otherwise in double
// quotes with the characters the shell would interpret escaped.
func ShellQuote(s string) string {
	if plainArg.MatchString(s) {
		return s
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`, "`", "\\`")
	return `"` + r.Replace(s) + `"`
}

// quoteArg quotes one ProxyCommand argument. OpenSSH expands %-tokens on the whole
// line first, so a literal % is doubled; it then runs the command through the
// user's shell.
func quoteArg(s string, windows bool) string {
	s = strings.ReplaceAll(s, "%", "%%")
	if windows {
		return WindowsQuote(s)
	}
	return ShellQuote(s)
}

// WindowsQuote quotes s for cmd and PowerShell: double quotes around whitespace and
// nothing else, so a path's backslashes arrive unchanged.
func WindowsQuote(s string) string {
	if strings.ContainsAny(s, " \t") {
		return `"` + s + `"`
	}
	return s
}
