/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/jupyter-infra/kubectl-workspace/internal/fsutil"
	"github.com/jupyter-infra/kubectl-workspace/internal/kube"
	"github.com/jupyter-infra/kubectl-workspace/internal/sshconfig"
)

// commandForEntry resolves --command for the ProxyCommand line: this binary when
// empty, a path made absolute because OpenSSH runs the command from its own working
// directory, and a bare name left to the PATH lookup at connection time.
func commandForEntry(command string) (string, error) {
	switch {
	case command == "":
		return executablePath()
	case strings.ContainsAny(command, `/\`):
		return fsutil.Abs("command", command)
	default:
		return command, nil
	}
}

// executablePath returns the path OpenSSH should run. When this binary is on PATH
// under its own name, that path is preferred: krew's PATH entry is a symlink into
// a versioned store directory, and the resolved path would break at the next
// upgrade.
func executablePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("failed to locate this binary: %w", err)
	}
	if onPath, err := exec.LookPath(filepath.Base(exe)); err == nil && fsutil.SameFile(onPath, exe) {
		return onPath, nil
	}
	return exe, nil
}

// clusterForContext resolves the cluster name in the alias for a context: the
// --cluster-name flag when given, else the name of the entry installed for the context
// and kubeconfig file in the SSH configuration, else the cluster name clusterName derives
// from the context name. The configuration is nil when it could not be read and the
// command only prints. A damaged entry routing the context is an error when strict and
// counts as no entry otherwise: a write reports the damage after acting on the file. Two
// matching entries, or a name edited into a marker by hand, are errors on every path,
// since the name derived instead could be another cluster's.
func clusterForContext(flag, contextName, kubeconfig string, file *sshconfig.File, strict bool) (string, error) {
	if flag != "" || file == nil {
		return clusterName(flag, contextName)
	}
	clusters, err := installedClusters(contextName, kubeconfig, file)
	if err != nil && strict {
		return "", err
	}
	cluster, err := pickInstalledCluster(clusters, file.Path(), contextName)
	if err != nil || cluster != "" {
		return cluster, err
	}
	return clusterName("", contextName)
}

// checkStandsAlone makes sure connect resolves the context from the entry's kubeconfig
// file alone the way the command just did: under a KUBECONFIG list, the context's cluster
// or user can come from another file than the context, and open would then check the
// workspace in one cluster while the editor's ssh reaches another.
func checkStandsAlone(target *kube.Target) error {
	if err := target.StandsAlone(); err != nil {
		return fmt.Errorf("%w; connect reads the entry's kubeconfig file alone, so merge the files into one, "+
			"for example with `kubectl config view --flatten`, and pass it as --kubeconfig", err)
	}
	return nil
}

// pickInstalledCluster returns the one cluster name among the entries installed for the
// context, empty when there is none. Two entries need --cluster-name to tell them
// apart, and a name edited into a marker by hand has to be a host-name label.
func pickInstalledCluster(clusters []string, path, contextName string) (string, error) {
	switch len(clusters) {
	case 0:
		return "", nil
	case 1:
		if err := sshconfig.ValidateClusterName(clusters[0]); err != nil {
			return "", fmt.Errorf("%s has an entry for context %q under %q, which cannot be the cluster name "+
				"in the alias (%w); pass --cluster-name", path, contextName, clusters[0], err)
		}
		return clusters[0], nil
	}
	return "", fmt.Errorf("%s has entries for context %q under the cluster names %s; pass --cluster-name",
		path, contextName, strings.Join(clusters, ", "))
}

// installedClusters lists the cluster names of the intact entries that route the context
// in the kubeconfig file, sorted. An entry naming another kubeconfig file routes another
// cluster of the same context name and does not count. The error is a damaged entry
// routing the context when no intact one does.
func installedClusters(contextName, kubeconfig string, file *sshconfig.File) ([]string, error) {
	var clusters []string
	var damaged error
	for _, e := range file.EntriesForContext(contextName) {
		switch {
		case e.Damaged != nil:
			if damaged == nil {
				damaged = fmt.Errorf("%s: %w", file.Path(), e.Damaged)
			}
		case e.Routes(contextName, kubeconfig):
			clusters = append(clusters, e.Cluster)
		}
	}
	if len(clusters) == 0 {
		return nil, damaged
	}
	sort.Strings(clusters)
	return clusters, nil
}

// warnAboutDamage reports each distinct damage among the entries routing the context,
// once: OpenSSH still honours the Host line of a block whose markers are broken, and
// only a hand can repair it.
func warnAboutDamage(w io.Writer, file *sshconfig.File, contextName string) {
	said := map[string]bool{}
	for _, e := range file.EntriesForContext(contextName) {
		if e.Damaged == nil || said[e.Damaged.Error()] {
			continue
		}
		said[e.Damaged.Error()] = true
		warnf(w, "%s: %v", file.Path(), e.Damaged)
	}
}

// routeOf names what an entry routes, for messages.
func routeOf(e sshconfig.InstalledEntry) string {
	if e.Kubeconfig == "" {
		return fmt.Sprintf("context %q", e.Context)
	}
	return fmt.Sprintf("context %q of kubeconfig %s", e.Context, e.Kubeconfig)
}

// routePair names what two entries route, for a message that contrasts them: the
// kubeconfig file appears only when it tells them apart.
func routePair(a, b sshconfig.InstalledEntry) (string, string) {
	if fsutil.SameFile(a.Kubeconfig, b.Kubeconfig) {
		a.Kubeconfig, b.Kubeconfig = "", ""
	}
	return routeOf(a), routeOf(b)
}

// sshConfigHintArgs returns the flags of a pasted ssh-config hint: the action flag,
// and the configuration file when it is not the default, so the pasted command acts
// on the same file.
func sshConfigHintArgs(action, sshConfigFile string) []string {
	args := []string{"--" + action}
	if sshConfigFile != "" {
		args = append(args, "--ssh-config-file", sshConfigFile)
	}
	return args
}

// kubectlWorkspaceCommand renders a command the user can paste into a shell. It
// always names the cluster: the cluster name is otherwise read from the installed entries,
// and the pasted command has to act on the cluster it was built for whatever entries
// exist by then.
func kubectlWorkspaceCommand(subcommand, kubeconfig, contextName, cluster string, args ...string) string {
	parts := []string{"kubectl", "workspace", subcommand}
	parts = append(parts, args...)
	if kubeconfig != "" {
		parts = append(parts, "--kubeconfig", kubeconfig)
	}
	parts = append(parts, "--context", contextName, "--cluster-name", cluster)
	quote := sshconfig.ShellQuote
	if runtime.GOOS == goosWindows {
		quote = sshconfig.WindowsQuote
	}
	for i, part := range parts {
		parts[i] = quote(part)
	}
	return strings.Join(parts, " ")
}

// clusterName resolves the cluster name in the alias: the --cluster-name flag when given, else
// the context name, or for a context named like an EKS cluster ARN the cluster name
// inside it, lowercased; either has to be a host-name label.
func clusterName(flag, contextName string) (string, error) {
	if flag != "" {
		return flag, sshconfig.ValidateClusterName(flag)
	}
	candidate := contextName
	if name, ok := eksClusterName(contextName); ok {
		candidate = name
	}
	// OpenSSH lowercases host names before matching, and ParseAlias lowercases too.
	candidate = strings.ToLower(candidate)
	if err := sshconfig.ValidateClusterName(candidate); err != nil {
		return "", fmt.Errorf("%q cannot be the cluster name in the alias (%w); pass --cluster-name", candidate, err)
	}
	return candidate, nil
}

// eksClusterARN matches the context name `aws eks update-kubeconfig` creates by
// default, arn:<partition>:eks:<region>:<account>:cluster/<name>.
var eksClusterARN = regexp.MustCompile(`^arn:[^:]+:eks:[^:]*:[^:]*:cluster/([^/]+)$`)

// eksClusterName returns the cluster name of a context named like an EKS cluster ARN.
func eksClusterName(contextName string) (string, bool) {
	m := eksClusterARN.FindStringSubmatch(contextName)
	if m == nil {
		return "", false
	}
	return m[1], true
}
