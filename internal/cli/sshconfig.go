/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package cli

import (
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/jupyter-infra/kubectl-workspace/internal/fsutil"
	"github.com/jupyter-infra/kubectl-workspace/internal/sshconfig"
	"github.com/jupyter-infra/kubectl-workspace/internal/tunnel"
)

type sshConfigOptions struct {
	*rootOptions
	write             bool
	remove            bool
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
		Use:   "ssh-config [--write | --remove]",
		Short: "Print, install or remove the SSH configuration entry for a cluster",
		Long: `ssh-config renders the SSH configuration entry that routes every workspace alias
of one cluster, ` + sshconfig.AliasFormat + `, through
"kubectl workspace connect". Without --write it prints the entry; with --write it
installs it in ~/.ssh/config, replacing an earlier entry for the same cluster; with
--remove it deletes the entry installed for the context, or the one --cluster-name names.

The cluster name comes from the entry already installed for the context, else from
the context name or the cluster name inside a context named like an EKS cluster ARN;
--cluster-name overrides it.`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return o.run()
		},
	}
	cmd.Flags().BoolVar(&o.write, writeFlag, false, "install the entry into the SSH configuration file")
	cmd.Flags().BoolVar(&o.remove, removeFlag, false,
		"remove the entry installed for the context, or the one --cluster-name names, from the SSH configuration file")
	cmd.MarkFlagsMutuallyExclusive(writeFlag, removeFlag)
	cmd.Flags().StringVar(&o.clusterName, "cluster-name", "", clusterNameHelp)
	cmd.Flags().StringVar(&o.sshConfigFile, "ssh-config-file", "", sshConfigFileHelp)
	cmd.Flags().StringVar(&o.command, "command", "",
		"path or name of the kubectl-workspace binary for the ProxyCommand (default this binary)")
	cmd.Flags().StringVar(&o.user, "user", sshconfig.DefaultUser, "SSH user name in the entry")
	cmd.Flags().StringVar(&o.caFile, "ca-file", "", caFileHelp+"; written into the entry")
	cmd.Flags().BoolVar(&o.allowPlaintext, allowPlaintextFlag, false, allowPlaintextHelp+"; written into the entry")
	cmd.Flags().DurationVar(&o.credentialTimeout, "credential-timeout", defaultCredentialTimeout,
		credentialTimeoutHelp+"; written into the entry when it differs from the default")
	return cmd
}

func (o *sshConfigOptions) run() error {
	if o.remove {
		return o.removeEntry()
	}
	// Printing needs no readable file: the cluster name then comes from the context.
	file, err := loadSSHConfig(o.sshConfigFile)
	if err != nil && o.write {
		return err
	}
	entry, err := o.entry(file)
	if err != nil {
		return err
	}
	if !o.write {
		_, _ = fmt.Fprint(o.streams.Out, entry.Render())
		return nil
	}
	warning, err := checkReplacement(file, entry, o.clusterName != "")
	if err != nil {
		return err
	}
	if err := file.Install(entry); err != nil {
		return err
	}
	if err := file.Save(); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(o.streams.Out, "Installed the entry for Host %s in %s.\n", entry.HostPattern(), file.Path())
	if warning != "" {
		warnf(o.streams.Err, "%s", warning)
	}
	o.warnAboutOtherEntries(file, entry)
	alias := strings.Replace(sshconfig.AliasFormat, "<cluster>", entry.Cluster, 1)
	_, _ = fmt.Fprintf(o.streams.Out, "Connect with: ssh %s\n", alias)
	return nil
}

// warnAboutOtherEntries names every other intact entry that routes the same cluster as
// the one just installed, with the command that removes it, since the alias under the
// old name still works; damaged entries routing the context get warnAboutDamage.
func (o *sshConfigOptions) warnAboutOtherEntries(file *sshconfig.File, entry sshconfig.Entry) {
	warnAboutDamage(o.streams.Err, file, entry.Context)
	removeArgs := sshConfigHintArgs(removeFlag, o.sshConfigFile)
	// The only error, a damaged entry, was reported just above.
	clusters, _ := installedClusters(entry.Context, entry.Kubeconfig, file)
	for _, other := range clusters {
		if other == entry.Cluster {
			continue
		}
		// A name edited into a marker by hand may not be safe on a command line.
		if err := sshconfig.ValidateClusterName(other); err != nil {
			warnf(o.streams.Err, "%s also routes context %q under the cluster name %q, which cannot be the cluster name "+
				"in the alias (%v); remove it by hand", file.Path(), entry.Context, other, err)
			continue
		}
		hint := kubectlWorkspaceCommand("ssh-config", o.kubeconfig, entry.Context, other, removeArgs...)
		warnf(o.streams.Err, "%s also routes context %q under the cluster name %q; remove it with: %s", file.Path(),
			entry.Context, other, hint)
	}
}

// checkReplacement looks at the entry a --write is about to replace. Under a derived
// cluster name, an entry that routes another cluster is refused: two EKS clusters named
// alike derive the same name, and the write would silently take over the first's entry.
// With --cluster-name the replacement is the user's choice. An entry whose ProxyCommand
// the plugin cannot read, as after a hand edit, is refused the same way, since it may be
// another cluster's. Any replaced entry whose route changes, the context or the
// kubeconfig file, earns a warning, since ssh then reaches another cluster or resolves the
// context in another file.
func checkReplacement(file *sshconfig.File, entry sshconfig.Entry, named bool) (warning string, err error) {
	installed, ok, err := file.EntryFor(entry.Cluster)
	if err != nil || !ok {
		return "", err
	}
	if installed.Context == "" {
		if named {
			return "", nil
		}
		return "", fmt.Errorf("%s has an entry for the cluster name %q whose ProxyCommand the plugin cannot read; "+
			"pass --cluster-name %s to replace it", file.Path(), entry.Cluster, entry.Cluster)
	}
	theirs, ours := routePair(installed, sshconfig.InstalledEntry{Context: entry.Context, Kubeconfig: entry.Kubeconfig})
	if !named && !installed.Routes(entry.Context, entry.Kubeconfig) {
		return "", fmt.Errorf("%s already routes %s under the cluster name %q; pass --cluster-name to put %s under "+
			"another name, or --cluster-name %s to replace that entry", file.Path(), theirs, entry.Cluster, ours, entry.Cluster)
	}
	if theirs != ours {
		return fmt.Sprintf("the entry for cluster %q routed %s and now routes %s", entry.Cluster, theirs, ours), nil
	}
	return "", nil
}

// removeEntry deletes the entry clusterToRemove names.
func (o *sshConfigOptions) removeEntry() error {
	file, err := loadSSHConfig(o.sshConfigFile)
	if err != nil {
		return err
	}
	cluster, route, err := o.clusterToRemove(file)
	if err != nil {
		return err
	}
	if cluster == "" {
		_, _ = fmt.Fprintf(o.streams.Out, "No entry in %s routes %s; pass --cluster-name to remove an entry by its "+
			"name.\n", file.Path(), routeOf(route))
		return nil
	}
	if route.Context == "" {
		// With --cluster-name the kubeconfig is not read, so the context for the damage
		// warning below comes from the entry itself; a lookup error is left to Remove, which
		// reports it.
		if installed, ok, err := file.EntryFor(cluster); err == nil && ok {
			route.Context = installed.Context
		}
	}
	removed, err := file.Remove(cluster)
	if err != nil {
		return err
	}
	if !removed {
		_, _ = fmt.Fprintf(o.streams.Out, "No entry for cluster %q in %s.\n", cluster, file.Path())
		return nil
	}
	if err := file.Save(); err != nil {
		return err
	}
	host := sshconfig.Entry{Cluster: cluster}.HostPattern()
	_, _ = fmt.Fprintf(o.streams.Out, "Removed the entry for Host %s from %s.\n", host, file.Path())
	if route.Context != "" {
		warnAboutDamage(o.streams.Err, file, route.Context)
	}
	return nil
}

// clusterToRemove resolves the entry --remove acts on: --cluster-name as written, since
// Remove matches the marker text and builds no alias, so an entry under a name edited by
// hand can go too; else the entry that routes the context in its kubeconfig file, which
// it returns as the route. With the flag the kubeconfig is not read, so an entry whose
// context is gone can still be removed. The derived default never counts: it could name
// another context's entry. The cluster is empty when nothing routes the context; a
// damaged entry routing the context is an error.
func (o *sshConfigOptions) clusterToRemove(file *sshconfig.File) (cluster string, route sshconfig.InstalledEntry,
	err error) {
	if o.clusterName != "" {
		return o.clusterName, route, nil
	}
	target, err := o.target()
	if err != nil {
		return "", route, fmt.Errorf("%w; pass --cluster-name to remove the entry without reading the kubeconfig", err)
	}
	route = sshconfig.InstalledEntry{Context: target.Context, Kubeconfig: target.Kubeconfig}
	clusters, err := installedClusters(target.Context, target.Kubeconfig, file)
	if err != nil {
		return "", route, err
	}
	if cluster, err = pickInstalledCluster(clusters, file.Path(), target.Context); err != nil {
		return "", route, err
	}
	return cluster, route, nil
}

// entry builds and validates the entry from the flags and the kubeconfig context. file
// is nil when the SSH configuration could not be read and the entry is only printed.
func (o *sshConfigOptions) entry(file *sshconfig.File) (sshconfig.Entry, error) {
	if err := checkCredentialTimeout(o.credentialTimeout); err != nil {
		return sshconfig.Entry{}, err
	}
	target, err := o.target()
	if err != nil {
		return sshconfig.Entry{}, err
	}
	if err := checkStandsAlone(target); err != nil {
		return sshconfig.Entry{}, err
	}
	cluster, err := clusterForContext(o.clusterName, target.Context, target.Kubeconfig, file, false)
	if err != nil {
		return sshconfig.Entry{}, err
	}
	command, err := commandForEntry(o.command)
	if err != nil {
		return sshconfig.Entry{}, err
	}
	entry := sshconfig.Entry{
		Cluster:        cluster,
		Context:        target.Context,
		Kubeconfig:     target.Kubeconfig,
		Command:        command,
		User:           o.user,
		Windows:        runtime.GOOS == goosWindows,
		AllowPlaintext: o.allowPlaintext,
	}
	if o.credentialTimeout != defaultCredentialTimeout {
		entry.CredentialTimeout = o.credentialTimeout
	}
	if o.caFile != "" {
		// Read now, so a wrong path fails here and not in every ssh session.
		if _, err := tunnel.LoadRoots(o.caFile); err != nil {
			return sshconfig.Entry{}, err
		}
		caFile, err := fsutil.Abs("CA file", o.caFile)
		if err != nil {
			return sshconfig.Entry{}, err
		}
		entry.CAFile = caFile
	}
	if err := entry.Validate(); err != nil {
		return sshconfig.Entry{}, err
	}
	return entry, nil
}
