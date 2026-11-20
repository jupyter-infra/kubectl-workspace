/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package cli

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jupyter-infra/kubectl-workspace/internal/kube"
	"github.com/jupyter-infra/kubectl-workspace/internal/sshconfig"
)

type openOptions struct {
	*rootOptions
	namespace     string
	clusterName   string
	remotePath    string
	sshConfigFile string
	printOnly     bool
	editor        string
}

// editorFor resolves an --editor value to the editor's display name. Each value
// is also the editor's URL scheme: a fork of VS Code that ships Remote-SSH opens VS
// Code's link under its own scheme.
func editorFor(value string) (string, error) {
	switch value {
	case editorVSCode:
		return "VS Code", nil
	case editorCursor:
		return "Cursor", nil
	}
	return "", fmt.Errorf("unknown --editor %q, supported: %s, %s", value, editorVSCode, editorCursor)
}

func newOpenCommand(root *rootOptions) *cobra.Command {
	o := &openOptions{rootOptions: root}
	cmd := &cobra.Command{
		Use:   "open <workspace>",
		Short: "Open a workspace in a desktop editor",
		Long: `open signs in with the kubeconfig credential if needed, asks the operator for a
connection to check that the workspace is running and reachable, and launches the
editor on the workspace through VS Code's Remote-SSH link: VS Code by default, or the
one --editor names. The sign-in happens here, in the terminal, so it never races the
editor's connection timeout.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return o.run(cmd.Context(), args[0])
		},
	}
	cmd.Flags().StringVarP(&o.namespace, "namespace", "n", "", "namespace of the workspace (default the context's)")
	cmd.Flags().StringVar(&o.clusterName, "cluster-name", "", clusterNameHelp)
	cmd.Flags().StringVar(&o.remotePath, "remote-path", defaultRemotePath, "folder to open in the workspace")
	cmd.Flags().StringVar(&o.sshConfigFile, "ssh-config-file", "", sshConfigFileHelp)
	cmd.Flags().BoolVar(&o.printOnly, "print-only", false, "print the link and the ssh command instead of launching")
	cmd.Flags().StringVar(&o.editor, "editor", defaultEditor,
		"editor to open the workspace in: "+editorVSCode+" or "+editorCursor)
	return cmd
}

func (o *openOptions) run(ctx context.Context, workspace string) error {
	if !strings.HasPrefix(o.remotePath, "/") {
		return fmt.Errorf("--remote-path must be absolute, got %q", o.remotePath)
	}
	editorName, err := editorFor(o.editor)
	if err != nil {
		return err
	}
	target, err := o.target()
	if err != nil {
		return err
	}
	if err := checkStandsAlone(target); err != nil {
		return err
	}
	namespace := o.namespace
	if namespace == "" {
		namespace = target.Namespace
	}
	// The editor is about to connect through the file, so a damaged entry for the
	// context or a file that cannot be read stops here, named, instead of the hint to
	// write a second entry under the derived name; printing needs no file.
	file, err := loadSSHConfig(o.sshConfigFile)
	if err != nil && !o.printOnly {
		return err
	}
	cluster, err := clusterForContext(o.clusterName, target.Context, target.Kubeconfig, file, !o.printOnly)
	if err != nil {
		return err
	}
	alias, err := sshconfig.NewAlias(workspace, namespace, cluster)
	if err != nil {
		return err
	}
	link := editorLink(o.editor, alias, o.remotePath)

	if err := o.checkEntry(file, cluster, target.Context, target.Kubeconfig); err != nil {
		return err
	}
	if o.printOnly {
		_, _ = fmt.Fprintln(o.streams.Out, link)
		_, _ = fmt.Fprintf(o.streams.Out, "ssh %s\n", alias)
		return nil
	}
	o.logf("resolving the credential for context %q", target.Context)
	credential := kube.CredentialOptions{Interactive: true, Stdin: o.streams.In, Stderr: o.streams.Err}
	if err := kube.ResolveCredential(ctx, target, credential); err != nil {
		return err
	}
	if _, err := kube.CreateConnection(ctx, target.Config, namespace, workspace); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(o.streams.Out, "Opening %s on %s (%s)\n", editorName, alias, link)
	_, _ = fmt.Fprintf(o.streams.Out, "Terminal: ssh %s\n", alias)
	return launchURL(ctx, link)
}

// checkEntry fails with the setup command when the cluster has no SSH entry yet, which
// the editor would otherwise report as an unreachable host, and refuses an entry under
// the cluster's name that routes another cluster or that the plugin cannot read. In
// print mode a file that could not be read, a missing entry or damage is tolerated,
// since printing needs no file; the refusal stays, since the printed alias would land in
// the other cluster.
func (o *openOptions) checkEntry(file *sshconfig.File, cluster, contextName, kubeconfig string) error {
	if file == nil {
		return nil
	}
	installed, ok, err := file.EntryFor(cluster)
	if err != nil {
		return o.unlessPrinting(err)
	}
	if !ok {
		return o.unlessPrinting(fmt.Errorf("%s has no entry for cluster %q; run: %s", file.Path(), cluster,
			o.writeHint(contextName, cluster)))
	}
	return o.refuseOtherCluster(file.Path(), installed, cluster, contextName, kubeconfig)
}

// unlessPrinting returns err, or nil when the command only prints and needs nothing
// from the file.
func (o *openOptions) unlessPrinting(err error) error {
	if o.printOnly {
		return nil
	}
	return err
}

// refuseOtherCluster refuses the installed entry under the cluster's name when it routes
// another cluster, with the command that replaces it on purpose, or when the plugin
// cannot read what it routes, as after a hand edit of its ProxyCommand; --cluster-name,
// given on purpose, overrides the second.
func (o *openOptions) refuseOtherCluster(path string, installed sshconfig.InstalledEntry, cluster, contextName,
	kubeconfig string) error {
	if installed.Context == "" {
		if o.clusterName != "" {
			return nil
		}
		return fmt.Errorf("%s has an entry for the cluster name %q whose ProxyCommand the plugin cannot read; "+
			"pass --cluster-name %s to connect through it anyway", path, cluster, cluster)
	}
	if !installed.Routes(contextName, kubeconfig) {
		theirs, ours := routePair(installed, sshconfig.InstalledEntry{Context: contextName, Kubeconfig: kubeconfig})
		return fmt.Errorf("%s routes %s under the cluster name %q, not %s; pass --cluster-name to put %s under another "+
			"name, or replace that entry with: %s", path, theirs, cluster, ours, ours, o.writeHint(contextName, cluster))
	}
	return nil
}

// writeHint is the ssh-config --write command that installs the entry for the cluster, or
// replaces it on purpose, with the kubeconfig and configuration file open was given.
func (o *openOptions) writeHint(contextName, cluster string) string {
	return kubectlWorkspaceCommand("ssh-config", o.kubeconfig, contextName, cluster,
		sshConfigHintArgs(writeFlag, o.sshConfigFile)...)
}

// editorLink is VS Code's Remote-SSH link under the editor's scheme; windowId=_blank
// opens a new window instead of reusing the last active one.
func editorLink(scheme string, alias sshconfig.Alias, remotePath string) string {
	escaped := (&url.URL{Path: remotePath}).EscapedPath()
	return fmt.Sprintf("%s://vscode-remote/ssh-remote+%s%s?windowId=_blank", scheme, alias, escaped)
}

// launchURL hands a link to the operating system's default handler. It is a
// variable so tests can replace it.
var launchURL = func(ctx context.Context, link string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "open", link)
	case goosWindows:
		cmd = exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", link)
	default:
		cmd = exec.CommandContext(ctx, "xdg-open", link)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to launch %s: %w (%s)", link, err, strings.TrimSpace(string(out)))
	}
	return nil
}
