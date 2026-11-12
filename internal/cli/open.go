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
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jupyter-infra/kubectl-workspace/internal/kube"
	"github.com/jupyter-infra/kubectl-workspace/internal/sshconfig"
)

// defaultRemotePath is the workspace user's home, where the persistent volume is
// mounted.
const defaultRemotePath = "/home/jovyan"

type openOptions struct {
	*rootOptions
	namespace     string
	clusterName   string
	remotePath    string
	sshConfigFile string
	printOnly     bool
	editor        string
}

const defaultEditor = "vscode"

// editors maps each --editor value, which is also the editor's URL scheme, to its
// display name; a fork of VS Code that ships Remote-SSH opens VS Code's link under
// its own scheme.
var editors = map[string]string{
	defaultEditor: "VS Code",
	"cursor":      "Cursor",
}

// editorFor resolves an --editor value to the editor's display name.
func editorFor(value string) (string, error) {
	if name, ok := editors[value]; ok {
		return name, nil
	}
	values := make([]string, 0, len(editors))
	for v := range editors {
		values = append(values, v)
	}
	sort.Strings(values)
	return "", fmt.Errorf("unknown --editor %q, supported: %s", value, strings.Join(values, ", "))
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
	cmd.Flags().StringVar(&o.editor, "editor", defaultEditor, "editor to open the workspace in: vscode or cursor")
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
	target, err := kube.Load(o.kubeconfig, o.context, o.userAgent)
	if err != nil {
		return err
	}
	namespace := o.namespace
	if namespace == "" {
		namespace = target.Namespace
	}
	cluster, err := clusterName(o.clusterName, target.Context)
	if err != nil {
		return err
	}
	alias, err := sshconfig.NewAlias(workspace, namespace, cluster)
	if err != nil {
		return err
	}
	link := editorLink(o.editor, alias, o.remotePath)

	if o.printOnly {
		_, _ = fmt.Fprintln(o.streams.Out, link)
		_, _ = fmt.Fprintf(o.streams.Out, "ssh %s\n", alias)
		return nil
	}

	if err := o.checkEntry(cluster, target.Context); err != nil {
		return err
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

// checkEntry fails with the setup command when the cluster has no SSH entry yet,
// which the editor would otherwise report as an unreachable host.
func (o *openOptions) checkEntry(cluster, contextName string) error {
	path, err := sshConfigPath(o.sshConfigFile)
	if err != nil {
		return err
	}
	installed, err := sshconfig.Installed(path, cluster)
	if err != nil {
		return err
	}
	if !installed {
		args := []string{"--write"}
		if o.sshConfigFile != "" {
			args = append(args, "--ssh-config-file", o.sshConfigFile)
		}
		return fmt.Errorf("%s has no entry for cluster %q; run: %s", path, cluster,
			kubectlWorkspaceCommand("ssh-config", o.kubeconfig, contextName, cluster, args...))
	}
	return nil
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
