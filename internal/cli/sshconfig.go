/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package cli

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/jupyter-infra/kubectl-workspace/internal/kube"
	"github.com/jupyter-infra/kubectl-workspace/internal/sshconfig"
	"github.com/jupyter-infra/kubectl-workspace/internal/tunnel"
)

type sshConfigOptions struct {
	*rootOptions
	write             bool
	clusterName       string
	sshConfigFile     string
	command           string
	user              string
	caFile            string
	credentialTimeout time.Duration
	allowPlaintext    bool
}

func newSSHConfigCommand(root *rootOptions) *cobra.Command {
	o := &sshConfigOptions{rootOptions: root}
	cmd := &cobra.Command{
		Use:   "ssh-config [--write]",
		Short: "Print or install the SSH configuration entry for a cluster",
		Long: `ssh-config renders the SSH configuration entry that routes every workspace alias
of one cluster, ` + sshconfig.AliasFormat + `, through
"kubectl workspace connect". Without --write it prints the entry; with --write it
installs it in ~/.ssh/config, replacing an earlier entry for the same cluster.

The cluster name defaults to the kubeconfig context name when that is a lowercase
DNS label; contexts named like EKS ARNs need --cluster-name.`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return o.run()
		},
	}
	cmd.Flags().BoolVar(&o.write, "write", false, "install the entry into the SSH configuration file")
	cmd.Flags().StringVar(&o.clusterName, "cluster-name", "", clusterNameHelp)
	cmd.Flags().StringVar(&o.sshConfigFile, "ssh-config-file", "", sshConfigFileHelp)
	cmd.Flags().StringVar(&o.command, "command", "",
		"path of the kubectl-workspace binary for the ProxyCommand (default this binary)")
	cmd.Flags().StringVar(&o.user, "user", sshconfig.DefaultUser, "SSH user name in the entry")
	cmd.Flags().StringVar(&o.caFile, "ca-file", "", caFileHelp+"; written into the entry")
	cmd.Flags().BoolVar(&o.allowPlaintext, allowPlaintextFlag, false, allowPlaintextHelp+"; written into the entry")
	cmd.Flags().DurationVar(&o.credentialTimeout, "credential-timeout", defaultCredentialTimeout,
		credentialTimeoutHelp+"; written into the entry when it differs")
	return cmd
}

func (o *sshConfigOptions) run() error {
	if err := checkCredentialTimeout(o.credentialTimeout); err != nil {
		return err
	}
	target, err := kube.Load(o.kubeconfig, o.context, o.userAgent)
	if err != nil {
		return err
	}
	cluster, err := clusterName(o.clusterName, target.Context)
	if err != nil {
		return err
	}
	command, err := commandForEntry(o.command)
	if err != nil {
		return err
	}
	kubeconfig, err := o.kubeconfigForEntry()
	if err != nil {
		return err
	}
	entry := sshconfig.Entry{
		Cluster:    cluster,
		Context:    target.Context,
		Kubeconfig: kubeconfig,
		Command:    command,
		User:       o.user,
		Windows:    runtime.GOOS == goosWindows,

		AllowPlaintext: o.allowPlaintext,
	}
	if o.credentialTimeout != defaultCredentialTimeout {
		entry.CredentialTimeout = o.credentialTimeout
	}
	if o.caFile != "" {
		// Read now, so a wrong path fails here and not in every ssh session.
		if _, err := tunnel.LoadRoots(o.caFile); err != nil {
			return err
		}
		if entry.CAFile, err = absPath("CA file", o.caFile); err != nil {
			return err
		}
	}
	if err := entry.Validate(); err != nil {
		return err
	}

	if !o.write {
		_, _ = fmt.Fprint(o.streams.Out, entry.Render())
		return nil
	}
	path, err := sshConfigPath(o.sshConfigFile)
	if err != nil {
		return err
	}
	if err := sshconfig.Install(path, entry); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(o.streams.Out, "Installed the entry for Host %s in %s.\n", entry.HostPattern(), path)
	alias := strings.Replace(sshconfig.AliasFormat, "<cluster>", cluster, 1)
	_, _ = fmt.Fprintf(o.streams.Out, "Connect with: ssh %s\n", alias)
	return nil
}

// kubeconfigForEntry pins the kubeconfig file into the entry; a KUBECONFIG list
// has no single-flag form and is left to the environment.
func (o *sshConfigOptions) kubeconfigForEntry() (string, error) {
	path := o.kubeconfig
	if path == "" {
		env := os.Getenv("KUBECONFIG")
		if env == "" {
			return "", nil
		}
		if strings.Contains(env, string(os.PathListSeparator)) {
			_, _ = fmt.Fprintf(o.streams.Err, "%s: KUBECONFIG lists several files; the entry relies on ssh "+
				"running with the same KUBECONFIG\n", programName)
			return "", nil
		}
		path = env
	}
	return absPath("kubeconfig", path)
}
