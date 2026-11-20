/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

// Package cli implements the kubectl workspace commands: connect, the SSH
// ProxyCommand; ssh-config, the one-time setup; and open, which launches the
// editor on a workspace.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/jupyter-infra/kubectl-workspace/internal/kube"
	"github.com/jupyter-infra/kubectl-workspace/internal/sshconfig"
)

// Streams are the process's standard streams, injectable for tests.
type Streams struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

// rootOptions is the state every command shares.
type rootOptions struct {
	kubeconfig  string
	contextName string
	verbose     bool
	streams     Streams
	userAgent   string
}

// warnf writes one line prefixed with the program name to w, which is always
// stderr: nothing but the SSH byte stream may reach stdout in connect.
func warnf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, programName+": "+format+"\n", args...)
}

// logf writes progress to stderr when --verbose is set.
func (o *rootOptions) logf(format string, args ...any) {
	if o.verbose {
		warnf(o.streams.Err, format, args...)
	}
}

// target loads the kubeconfig context the global flags select.
func (o *rootOptions) target() (*kube.Target, error) {
	return kube.Load(o.kubeconfig, o.contextName, o.userAgent)
}

// Main runs the plugin and returns the process exit code.
func Main(args []string, streams Streams, version string) int {
	// OpenSSH ends a ProxyCommand with SIGHUP once the session is over.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()

	root := newRootCommand(streams, version)
	root.SetArgs(args)
	if err := root.ExecuteContext(ctx); err != nil {
		warnf(streams.Err, "%v", err)
		return 1
	}
	return 0
}

// loadSSHConfig reads the file --ssh-config-file names, or the default, once; a command
// looks entries up in, and edits, what it read.
func loadSSHConfig(flag string) (*sshconfig.File, error) {
	path := flag
	if path == "" {
		var err error
		if path, err = sshconfig.DefaultPath(); err != nil {
			return nil, err
		}
	}
	return sshconfig.Load(path, runtime.GOOS == goosWindows, kube.DefaultKubeconfig())
}

// newRootCommand builds the command tree.
func newRootCommand(streams Streams, version string) *cobra.Command {
	o := &rootOptions{streams: streams, userAgent: programName + "/" + version}
	root := &cobra.Command{
		Use:   "workspace",
		Short: "Reach jupyter-k8s workspaces over SSH from this machine",
		Long: `kubectl workspace connects this machine to jupyter-k8s workspaces over SSH,
carried inside a WebSocket through the cluster's HTTPS ingress and authorized by
the kubeconfig credential. Set up once per cluster with "ssh-config --write";
afterwards "ssh ` + sshconfig.AliasFormat + `" and VS Code's Remote-SSH
reach any workspace in that cluster, and "open" launches VS Code on one.`,
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetIn(streams.In)
	root.SetOut(streams.Out)
	root.SetErr(streams.Err)
	// The binary is kubectl-workspace, invoked as `kubectl workspace`; cobra only
	// knows the second word, so the templates add the first.
	root.SetUsageTemplate(strings.NewReplacer(
		"{{.UseLine}}", "kubectl {{.UseLine}}",
		"{{.CommandPath}}", "kubectl {{.CommandPath}}",
	).Replace(root.UsageTemplate()))
	root.SetVersionTemplate("kubectl workspace {{.Version}}\n")

	flags := root.PersistentFlags()
	flags.StringVar(&o.kubeconfig, "kubeconfig", "", "path to the kubeconfig file (default $KUBECONFIG or ~/.kube/config)")
	flags.StringVar(&o.contextName, "context", "", "kubeconfig context to use (default the current context)")
	flags.BoolVarP(&o.verbose, "verbose", "v", false, "write progress to stderr")

	root.AddCommand(newConnectCommand(o), newSSHConfigCommand(o), newOpenCommand(o))
	return root
}

func checkCredentialTimeout(d time.Duration) error {
	if d <= 0 {
		return fmt.Errorf("--credential-timeout must be positive, got %s", d)
	}
	return nil
}
