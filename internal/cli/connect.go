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

type connectOptions struct {
	*rootOptions
	directURL         string
	caFile            string
	credentialTimeout time.Duration
	allowPlaintext    bool
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
	// Required and the only mode, the convention set by `coder ssh --stdio` and `devpod ssh --stdio`.
	cmd.Flags().Bool("stdio", false, "relay SSH between stdin/stdout and the workspace (required)")
	_ = cmd.MarkFlagRequired("stdio")
	cmd.Flags().DurationVar(&o.credentialTimeout, "credential-timeout", defaultCredentialTimeout, credentialTimeoutHelp)
	cmd.Flags().StringVar(&o.directURL, "url", "",
		"WebSocket URL to connect to directly instead of asking the cluster (development)")
	_ = cmd.Flags().MarkHidden("url")
	cmd.Flags().StringVar(&o.caFile, "ca-file", "", caFileHelp)
	cmd.Flags().BoolVar(&o.allowPlaintext, allowPlaintextFlag, false, allowPlaintextHelp)
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
	roots, err := tunnel.LoadRoots(o.caFile)
	if err != nil {
		return err
	}
	conn, err := o.connection(ctx, alias)
	if err != nil {
		return err
	}
	ws, err := tunnel.Dial(ctx, conn.URL, tunnel.DialOptions{Token: conn.Token, UserAgent: o.userAgent, RootCAs: roots})
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
	if conn.URL.Scheme != "wss" && !o.allowPlaintext {
		return nil, fmt.Errorf("the operator returned a %s:// connection URL; refusing to send the token without TLS, "+
			"pass --%s for a local cluster", conn.URL.Scheme, allowPlaintextFlag)
	}
	o.logf("connection granted, opening %s", conn.URL.Host)
	return conn, nil
}

// slowPluginFactor scales the active deadline into the one the hint suggests.
const slowPluginFactor = 3

// credentialError adds the sign-in command to failures a terminal can fix; a missing
// plugin already carries the kubeconfig's install hint.
func (o *connectOptions) credentialError(err error, contextName string, alias sshconfig.Alias) error {
	var signIn *kube.SignInRequiredError
	var exited *kube.PluginExitError
	if !errors.As(err, &signIn) && !errors.As(err, &exited) {
		return err
	}
	msg := "Sign in from a terminal first, then reconnect:\n  " + kubectlWorkspaceCommand("open",
		o.kubeconfig, contextName, alias.Cluster, alias.Workspace, "--namespace", alias.Namespace)
	if signIn != nil {
		// --write replaces the entry, so the command carries the flags connect knows of.
		args := []string{"--write", "--credential-timeout", (slowPluginFactor * o.credentialTimeout).String()}
		if o.caFile != "" {
			args = append(args, "--ca-file", o.caFile)
		}
		if o.allowPlaintext {
			args = append(args, "--"+allowPlaintextFlag)
		}
		msg += "\nIf the plugin is slow rather than waiting for a sign-in, add --credential-timeout to your " +
			"ssh-config --write command, for example:\n  " +
			kubectlWorkspaceCommand("ssh-config", o.kubeconfig, contextName, alias.Cluster, args...)
	}
	return fmt.Errorf("%w\n%s", err, msg)
}
