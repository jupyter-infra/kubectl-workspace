/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/jupyter-infra/kubectl-workspace/internal/kube"
	"github.com/jupyter-infra/kubectl-workspace/internal/sshconfig"
	"github.com/jupyter-infra/kubectl-workspace/internal/tunnel"
)

// defaultCredentialTimeout leaves room inside Remote-SSH's 15-second connection
// timeout for the connection request and the WebSocket handshake.
const defaultCredentialTimeout = 5 * time.Second

type connectOptions struct {
	*rootOptions
	stdio             bool
	directURL         string
	credentialTimeout time.Duration
}

func newConnectCommand(root *rootOptions) *cobra.Command {
	o := &connectOptions{rootOptions: root}
	cmd := &cobra.Command{
		Use:   "connect --stdio " + sshconfig.AliasFormat,
		Short: "Relay one SSH connection to a workspace (the SSH ProxyCommand)",
		Long: `connect is the ProxyCommand the ssh-config entry installs. OpenSSH runs it with
the workspace alias and speaks SSH through its stdin and stdout. It asks the
operator for a connection to the workspace with the kubeconfig credential,
opens the WebSocket the operator names with the token in a header, and relays
bytes until either side closes.

The credential plugin named in the kubeconfig runs with a deadline and no
terminal: a sign-in cannot happen inside an SSH connection, so when the
credential has expired connect fails fast and names the command to run instead.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return o.run(cmd.Context(), args[0])
		},
	}
	// The only mode; the flag marks the ProxyCommand line in ssh_config as speaking
	// SSH on stdio, the convention `coder ssh --stdio` and `devpod ssh --stdio` set.
	cmd.Flags().BoolVar(&o.stdio, "stdio", false, "relay SSH between stdin/stdout and the workspace (required)")
	_ = cmd.MarkFlagRequired("stdio")
	cmd.Flags().DurationVar(&o.credentialTimeout, "credential-timeout", defaultCredentialTimeout,
		"how long the kubeconfig's credential plugin may take before connect gives up")
	cmd.Flags().StringVar(&o.directURL, "url", "",
		"WebSocket URL to connect to directly instead of asking the cluster (development)")
	_ = cmd.Flags().MarkHidden("url")
	return cmd
}

func (o *connectOptions) run(ctx context.Context, host string) error {
	if err := checkCredentialTimeout(o.credentialTimeout); err != nil {
		return err
	}
	alias, err := sshconfig.ParseAlias(host)
	if err != nil {
		return err
	}

	err = o.session(ctx, alias)
	if reason, ok := tunnel.CloseReason(err); ok {
		_, _ = fmt.Fprintf(o.streams.Err, "%s: the workspace closed the connection: %s\n", programName, reason)
		return nil
	}
	// A signal from ssh is how a finished session ends, not a failure, whichever
	// phase it interrupts.
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// session requests the connection, dials the tunnel and relays until it ends.
func (o *connectOptions) session(ctx context.Context, alias sshconfig.Alias) error {
	conn, err := o.connection(ctx, alias)
	if err != nil {
		return err
	}
	ws, err := tunnel.Dial(ctx, conn.URL, conn.Token, o.userAgent)
	if err != nil {
		return err
	}
	defer ws.Close()
	o.logf("connected to %s", conn.URL.Host)
	return tunnel.Relay(ctx, ws, o.streams.In, o.streams.Out)
}

func (o *connectOptions) connection(ctx context.Context, alias sshconfig.Alias) (*kube.Connection, error) {
	if o.directURL != "" {
		conn, err := kube.ParseConnectionURL(o.directURL)
		if err != nil {
			return nil, fmt.Errorf("invalid --url: %w", err)
		}
		return conn, nil
	}

	target, err := kube.Load(o.kubeconfig, o.context, o.userAgent)
	if err != nil {
		return nil, err
	}
	o.logf("context %q, workspace %s/%s", target.Context, alias.Namespace, alias.Workspace)

	credential := kube.CredentialOptions{Stderr: o.streams.Err, Timeout: o.credentialTimeout}
	if err := kube.ResolveCredential(ctx, target, credential); err != nil {
		return nil, o.credentialError(err, target.Context, alias)
	}

	conn, err := kube.CreateConnection(ctx, target.Config, alias.Namespace, alias.Workspace)
	if err != nil {
		return nil, err
	}
	o.logf("connection granted, opening %s", conn.URL.Host)
	return conn, nil
}

// credentialError adds the way out to a credential failure a terminal can fix: a
// plugin that waited for a browser or exited non-zero, not one that is missing. A
// plugin that only took too long is told how to raise the deadline in the entry.
func (o *connectOptions) credentialError(err error, contextName string, alias sshconfig.Alias) error {
	var signIn *kube.SignInRequiredError
	var exited *kube.PluginExitError
	if !errors.As(err, &signIn) && !errors.As(err, &exited) {
		return err
	}
	msg := "Sign in from a terminal first, then reconnect:\n  " + kubectlWorkspaceCommand("open",
		o.kubeconfig, contextName, alias.Cluster, alias.Workspace, "--namespace", alias.Namespace)
	if signIn != nil {
		msg += "\nA plugin that is slow rather than signing in needs a longer deadline in the SSH entry: rerun " +
			"your ssh-config --write command for this cluster with --credential-timeout added, for a default entry\n  " +
			kubectlWorkspaceCommand("ssh-config", o.kubeconfig, contextName, alias.Cluster,
				"--write", "--credential-timeout", (3*o.credentialTimeout).String())
	}
	return fmt.Errorf("%w\n%s", err, msg)
}

func checkCredentialTimeout(d time.Duration) error {
	if d <= 0 {
		return fmt.Errorf("--credential-timeout must be positive, got %s", d)
	}
	return nil
}
