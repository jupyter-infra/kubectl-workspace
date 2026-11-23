/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package cli

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/jupyter-infra/kubectl-workspace/internal/kube"
	"github.com/jupyter-infra/kubectl-workspace/internal/sshconfig"
	"github.com/jupyter-infra/kubectl-workspace/internal/testutil"
)

func TestOpenPrintOnlyPrintsTheLinkAndTheSSHCommand(t *testing.T) {
	kubeconfig := staticKubeconfig(t)

	stdout, err := run(t, "open", "--kubeconfig", kubeconfig, "--print-only", "ws1")
	if err != nil {
		t.Fatal(err)
	}
	want := "vscode://vscode-remote/ssh-remote+" + alias + "/home/jovyan?windowId=_blank\nssh " + alias + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}

	stdout, err = run(t, "open", "--kubeconfig", kubeconfig, "--print-only", "-n", "other",
		"--remote-path", "/home/jovyan/my project#1", "ws1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stdout, "vscode://vscode-remote/ssh-remote+ws1.other.dev.jk8s/home/jovyan/my%20project%231?") {
		t.Errorf("stdout = %q, want the path escaped for the link", stdout)
	}
}

func TestOpenBuildsTheLinkForTheChosenEditor(t *testing.T) {
	kubeconfig := staticKubeconfig(t)
	for _, editor := range []string{"vscode", "cursor", "kiro"} {
		stdout, err := run(t, "open", "--kubeconfig", kubeconfig, "--print-only", "--editor", editor, "ws1")
		if err != nil {
			t.Fatal(err)
		}
		want := editor + "://vscode-remote/ssh-remote+" + alias + "/home/jovyan?windowId=_blank\n"
		if !strings.HasPrefix(stdout, want) {
			t.Errorf("--editor %s: stdout = %q, want it to start with %q", editor, stdout, want)
		}
	}
	_, err := run(t, "open", "--kubeconfig", kubeconfig, "--print-only", "--editor", "emacs", "ws1")
	if err == nil || !strings.Contains(err.Error(), `unknown --editor "emacs", supported: vscode, cursor, kiro`) {
		t.Errorf("error = %v, want the supported editors listed", err)
	}
}

func TestOpenDefaultsTheNamespaceWhenTheContextHasNone(t *testing.T) {
	kubeconfig := testutil.Kubeconfig{Server: "https://127.0.0.1:1"}.Write(t)

	stdout, err := run(t, "open", "--kubeconfig", kubeconfig, "--print-only", "ws1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stdout, "vscode://vscode-remote/ssh-remote+ws1.default.dev.jk8s/") {
		t.Errorf("stdout = %q, want the default namespace in the alias", stdout)
	}
}

func TestOpenRejectsARelativeRemotePath(t *testing.T) {
	_, err := run(t, "open", "--kubeconfig", staticKubeconfig(t), "--print-only", "--remote-path", "project", "ws1")
	if err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Errorf("error = %v", err)
	}
}

func TestOpenFailsWithTheSetupCommandWhenTheEntryIsMissing(t *testing.T) {
	kubeconfig := staticKubeconfig(t)
	configFile := filepath.Join(t.TempDir(), "config")
	tests := []struct {
		name     string
		args     []string
		wantHint string
	}{
		{
			name: "context usable as the cluster name",
			args: nil,
			wantHint: "kubectl workspace ssh-config --write --ssh-config-file " + configFile +
				" --kubeconfig " + kubeconfig + " --context dev --cluster-name dev",
		},
		{
			name: "EKS ARN context with a chosen cluster name",
			args: []string{"--context", testutil.ARNContext, "--cluster-name", "jd"},
			wantHint: "kubectl workspace ssh-config --write --ssh-config-file " + configFile +
				" --kubeconfig " + kubeconfig + " --context " + testutil.ARNContext + " --cluster-name jd",
		},
		{
			name: "EKS ARN context with the default cluster name",
			args: []string{"--context", testutil.ARNContext},
			wantHint: "kubectl workspace ssh-config --write --ssh-config-file " + configFile +
				" --kubeconfig " + kubeconfig + " --context " + testutil.ARNContext + " --cluster-name dev",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The hint has to name the file open looked in, or a rerun installs elsewhere.
			args := append([]string{"open", "--kubeconfig", kubeconfig, "--ssh-config-file", configFile}, tt.args...)
			_, err := run(t, append(args, "ws1")...)
			if err == nil || !strings.Contains(err.Error(), tt.wantHint) {
				t.Errorf("error = %v, want the setup command %q", err, tt.wantHint)
			}
		})
	}
}

// installedEntry writes a configuration file holding the entry for cluster dev of the
// kubeconfig file.
func installedEntry(t *testing.T, kubeconfig string) string {
	t.Helper()
	configFile := filepath.Join(t.TempDir(), "config")
	installEntry(t, configFile, sshconfig.Entry{Cluster: "dev", Context: "dev", Kubeconfig: kubeconfig})
	return configFile
}

// installEntry installs the entry with this OS's quoting, the way ssh-config --write
// does, filling in the command and the user.
func installEntry(t *testing.T, configFile string, entry sshconfig.Entry) {
	t.Helper()
	entry.Command, entry.User, entry.Windows = kwCommand, "jovyan", runtime.GOOS == goosWindows
	file, err := sshconfig.Load(configFile, runtime.GOOS == goosWindows, kube.DefaultKubeconfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Install(entry); err != nil {
		t.Fatal(err)
	}
	if err := file.Save(); err != nil {
		t.Fatal(err)
	}
}

// renderedEntry returns the entry for context dev of the kubeconfig file as ssh-config
// --write writes it, for a fixture Install would refuse: a name that is no label, or a
// block about to be damaged.
func renderedEntry(cluster, kubeconfig string) string {
	return sshconfig.Entry{Cluster: cluster, Context: "dev", Kubeconfig: kubeconfig, Command: kwCommand, User: "jovyan",
		Windows: runtime.GOOS == goosWindows}.Render()
}

// writeConfig writes an SSH configuration file with the given content.
func writeConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// recordLaunches replaces the editor launcher for the test and returns the links it received.
func recordLaunches(t *testing.T) *[]string {
	t.Helper()
	var launched []string
	previous := launchURL
	launchURL = func(_ context.Context, link string) error {
		launched = append(launched, link)
		return nil
	}
	t.Cleanup(func() { launchURL = previous })
	return &launched
}

func TestOpenChecksTheConnectionThenLaunchesTheEditor(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantName string
		wantLink string
	}{
		{
			name: "VS Code by default", wantName: "Opening VS Code on",
			wantLink: "vscode://vscode-remote/ssh-remote+" + alias + "/home/jovyan?windowId=_blank",
		},
		{
			name: "Cursor when asked", args: []string{"--editor", "cursor"}, wantName: "Opening Cursor on",
			wantLink: "cursor://vscode-remote/ssh-remote+" + alias + "/home/jovyan?windowId=_blank",
		},
		{
			name: "Kiro when asked", args: []string{"--editor", "kiro"}, wantName: "Opening Kiro on",
			wantLink: "kiro://vscode-remote/ssh-remote+" + alias + "/home/jovyan?windowId=_blank",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			op := testutil.NewFakeOperator(t, http.StatusCreated,
				testutil.ConnectionBody("wss://jk8s.example.com"+sshPath+"?token=abc"))
			kubeconfig := operatorKubeconfig(t, op)
			launched := recordLaunches(t)

			args := append([]string{"open", "--kubeconfig", kubeconfig, "--ssh-config-file", installedEntry(t, kubeconfig)},
				tt.args...)
			stdout, err := run(t, append(args, "ws1")...)
			if err != nil {
				t.Fatal(err)
			}
			if req := op.LastRequest(t); req.Path != wantAPIPath {
				t.Errorf("the operator saw %q, want %q", req.Path, wantAPIPath)
			}
			if len(*launched) != 1 || (*launched)[0] != tt.wantLink {
				t.Errorf("launched %q, want %q", *launched, tt.wantLink)
			}
			for _, want := range []string{tt.wantName, "ssh " + alias} {
				if !strings.Contains(stdout, want) {
					t.Errorf("stdout = %q, want it to mention %q", stdout, want)
				}
			}
		})
	}
}

func TestOpenDoesNotLaunchWhenTheOperatorRefuses(t *testing.T) {
	op := testutil.NewFakeOperator(t, http.StatusBadRequest,
		testutil.StatusBody(400, "workspace is not available. Check workspace status for details"))
	kubeconfig := operatorKubeconfig(t, op)
	launched := recordLaunches(t)

	_, err := run(t, "open", "--kubeconfig", kubeconfig, "--ssh-config-file", installedEntry(t, kubeconfig), "ws1")
	if err == nil || !strings.Contains(err.Error(), "workspace is not available") {
		t.Errorf("error = %v, want the operator's message", err)
	}
	if len(*launched) != 0 {
		t.Error("the editor was launched although the workspace is not reachable")
	}
}

// The entry carries the context, so open finds the cluster name a user chose for an
// EKS context without being told again.
func TestOpenReadsTheClusterNameFromTheInstalledEntry(t *testing.T) {
	kubeconfig := staticKubeconfig(t)
	configFile := filepath.Join(t.TempDir(), "config")
	install := func(cluster string) {
		installEntry(t, configFile, sshconfig.Entry{Cluster: cluster, Context: testutil.ARNContext, Kubeconfig: kubeconfig})
	}
	open := func() (string, error) {
		return run(t, "open", "--kubeconfig", kubeconfig, "--context", testutil.ARNContext,
			"--ssh-config-file", configFile, "--print-only", "ws1")
	}

	stdout, err := open()
	if err != nil || !strings.Contains(stdout, "ssh ws1.default.dev.jk8s\n") {
		t.Errorf("without an entry: stdout = %q, %v; want the ARN's cluster name dev", stdout, err)
	}

	install("jd")
	stdout, err = open()
	if err != nil || !strings.Contains(stdout, "ssh ws1.default.jd.jk8s\n") {
		t.Errorf("with the entry: stdout = %q, %v; want the entry's cluster name jd", stdout, err)
	}

	install("jd-two")
	_, err = open()
	if err == nil || !strings.Contains(err.Error(), "jd, jd-two") || !strings.Contains(err.Error(), "--cluster-name") {
		t.Errorf("with two entries: error = %v, want both names and the flag to disambiguate", err)
	}
}

// A marker edited by hand can name a cluster the alias cannot carry; the lookup
// says so and names the flag, as every other failure to pick a cluster name does.
func TestOpenRefusesAnEditedEntryWhoseNameIsNoLabel(t *testing.T) {
	kubeconfig := staticKubeconfig(t)
	configFile := filepath.Join(t.TempDir(), "config")
	writeConfig(t, configFile, renderedEntry("My Cluster", kubeconfig))
	_, err := run(t, "open", "--kubeconfig", kubeconfig, "--ssh-config-file", configFile, "--print-only", "ws1")
	if err == nil || !strings.Contains(err.Error(), "cannot be the cluster name in the alias") || !strings.Contains(err.Error(), "--cluster-name") {
		t.Errorf("error = %v, want the edited name refused with the flag named", err)
	}
}

// Two kubeconfig files may name a context alike; open finds the entry that routes the
// context in the file in use, named by --kubeconfig or KUBECONFIG, and an entry for
// another file does not count.
func TestOpenFindsTheEntryOfTheKubeconfigFileInUse(t *testing.T) {
	first, second := staticKubeconfig(t), staticKubeconfig(t)
	configFile := filepath.Join(t.TempDir(), "config")
	for cluster, kubeconfig := range map[string]string{"first": first, "second": second} {
		installEntry(t, configFile, sshconfig.Entry{Cluster: cluster, Context: "dev", Kubeconfig: kubeconfig})
	}
	for cluster, kubeconfig := range map[string]string{"first": first, "second": second} {
		stdout, err := run(t, "open", "--kubeconfig", kubeconfig, "--ssh-config-file", configFile, "--print-only", "ws1")
		if err != nil || !strings.Contains(stdout, "ssh ws1.team-a."+cluster+".jk8s\n") {
			t.Errorf("with %s: stdout = %q, %v; want the alias under %s", kubeconfig, stdout, err, cluster)
		}
	}
	// An entry for another kubeconfig file does not count, so a third file gets the name
	// derived from its context.
	stdout, err := run(t, "open", "--kubeconfig", staticKubeconfig(t), "--ssh-config-file", configFile, "--print-only", "ws1")
	if err != nil || !strings.Contains(stdout, "ssh ws1.team-a.dev.jk8s\n") {
		t.Errorf("with a third kubeconfig: stdout = %q, %v; want the derived name", stdout, err)
	}

	// The entry names the file KUBECONFIG named at --write, so KUBECONFIG finds it again.
	t.Setenv("KUBECONFIG", second)
	stdout, err = run(t, "open", "--ssh-config-file", configFile, "--print-only", "ws1")
	if err != nil || !strings.Contains(stdout, "ssh ws1.team-a.second.jk8s\n") {
		t.Errorf("with KUBECONFIG=%s: stdout = %q, %v; want the alias under second", second, stdout, err)
	}
}

// The entry under the derived name may route another cluster: another context deriving
// the same name, or the same context name in another kubeconfig file, whichever side
// names the file. open refuses it rather than letting the editor connect through it.
func TestOpenRefusesAnEntryRoutingAnotherCluster(t *testing.T) {
	kubeconfig, other := staticKubeconfig(t), staticKubeconfig(t)
	home := kube.DefaultKubeconfig()
	useAsDefaultKubeconfig(t, kubeconfig)
	tests := []struct {
		name      string
		installed sshconfig.Entry
		// args selects the kubeconfig open runs with; none is the default lookup.
		args      []string
		wantRoute string
	}{
		{
			name:      "another context deriving the same name",
			installed: sshconfig.Entry{Cluster: "dev", Context: testutil.ARNContext, Kubeconfig: kubeconfig},
			args:      []string{"--kubeconfig", kubeconfig},
			wantRoute: `routes context "` + testutil.ARNContext + `" under the cluster name "dev", not context "dev"`,
		},
		{
			name:      "the same context name in another file",
			installed: sshconfig.Entry{Cluster: "dev", Context: "dev", Kubeconfig: other},
			args:      []string{"--kubeconfig", kubeconfig},
			wantRoute: `routes context "dev" of kubeconfig ` + other + ` under the cluster name "dev", ` +
				`not context "dev" of kubeconfig ` + kubeconfig,
		},
		{
			name:      "an entry naming a file while open uses the default lookup",
			installed: sshconfig.Entry{Cluster: "dev", Context: "dev", Kubeconfig: other},
			wantRoute: `routes context "dev" of kubeconfig ` + other + ` under the cluster name "dev", ` +
				`not context "dev" of kubeconfig ` + home,
		},
		{
			name:      "an entry naming no file, read as the default file, while open names one",
			installed: sshconfig.Entry{Cluster: "dev", Context: "dev"},
			args:      []string{"--kubeconfig", kubeconfig},
			wantRoute: `routes context "dev" of kubeconfig ` + home + ` under the cluster name "dev", ` +
				`not context "dev" of kubeconfig ` + kubeconfig,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configFile := filepath.Join(t.TempDir(), "config")
			installEntry(t, configFile, tt.installed)
			// The printed alias would land in the other cluster too, so --print-only refuses alike.
			var hint string
			for _, extra := range [][]string{nil, {"--print-only"}} {
				args := append([]string{"open", "--ssh-config-file", configFile, "--context", "dev"}, tt.args...)
				args = append(args, extra...)
				_, err := run(t, append(args, "ws1")...)
				if err == nil || !strings.Contains(err.Error(), tt.wantRoute) || !strings.Contains(err.Error(), "--cluster-name") {
					t.Fatalf("%v: error = %v, want the entry's route named and the flag", extra, err)
				}
				_, hint, _ = strings.Cut(err.Error(), "replace that entry with: ")
			}
			// The printed command, run as printed, replaces the entry on purpose; open then goes through.
			if _, err := run(t, sshconfig.SplitArgs(hint, runtime.GOOS == goosWindows)[2:]...); err != nil {
				t.Fatalf("the printed command %q failed: %v", hint, err)
			}
			args := append([]string{"open", "--ssh-config-file", configFile, "--context", "dev", "--print-only"}, tt.args...)
			if stdout, err := run(t, append(args, "ws1")...); err != nil || !strings.Contains(stdout, "ssh ws1.team-a.dev.jk8s\n") {
				t.Errorf("after the printed command: stdout = %q, %v; want the alias under the replaced entry", stdout, err)
			}
		})
	}
}

// An entry whose ProxyCommand names no kubeconfig file, written by hand or by an earlier
// build, leaves the file to ssh's environment; open reads it as the default file, so it
// serves the default lookup.
func TestOpenReadsAnEntryWithoutAFileAsTheDefaultFile(t *testing.T) {
	useAsDefaultKubeconfig(t, staticKubeconfig(t))
	configFile := filepath.Join(t.TempDir(), "config")
	installEntry(t, configFile, sshconfig.Entry{Cluster: "no-file", Context: "dev"})

	stdout, err := run(t, "open", "--ssh-config-file", configFile, "--print-only", "ws1")
	if err != nil || !strings.Contains(stdout, "ssh ws1.team-a.no-file.jk8s\n") {
		t.Errorf("stdout = %q, %v; want the alias under the entry without a file", stdout, err)
	}
}

// A damaged entry routing the context stops open before it derives a name and sends the
// user to write a second entry; printing the link and command still works.
func TestOpenStopsOnADamagedEntryForTheContext(t *testing.T) {
	kubeconfig := staticKubeconfig(t)
	configFile := filepath.Join(t.TempDir(), "config")
	writeConfig(t, configFile, brokenEntry)
	_, err := run(t, "open", "--kubeconfig", kubeconfig, "--ssh-config-file", configFile, "ws1")
	if err == nil || !strings.Contains(err.Error(), `the entry for cluster "jd" has no end marker`) {
		t.Errorf("error = %v, want the damaged entry named", err)
	}
	stdout, err := run(t, "open", "--kubeconfig", kubeconfig, "--ssh-config-file", configFile, "--print-only", "ws1")
	if err != nil || !strings.Contains(stdout, "ssh ws1.team-a.dev.jk8s\n") {
		t.Errorf("--print-only: stdout = %q, %v; want the derived alias printed", stdout, err)
	}
}

// The entry's kubeconfig file is matched as a file: an entry written through a dotfiles
// link is found from the path the link points at.
func TestOpenMatchesTheKubeconfigFileThroughALink(t *testing.T) {
	kubeconfig := staticKubeconfig(t)
	link := filepath.Join(t.TempDir(), "kubeconfig")
	testutil.Symlink(t, kubeconfig, link)
	configFile := filepath.Join(t.TempDir(), "config")
	installEntry(t, configFile, sshconfig.Entry{Cluster: "linked", Context: "dev", Kubeconfig: link})

	stdout, err := run(t, "open", "--kubeconfig", kubeconfig, "--ssh-config-file", configFile, "--print-only", "ws1")
	if err != nil || !strings.Contains(stdout, "ssh ws1.team-a.linked.jk8s\n") {
		t.Errorf("stdout = %q, %v; want the alias under the entry written through the link", stdout, err)
	}
}

// An entry under the cluster name whose ProxyCommand the plugin cannot read, one a user
// rewrote by hand, may be another cluster's: open refuses it until --cluster-name names
// it on purpose, and then goes on to the operator through it.
func TestOpenRefusesAnEntryItCannotRead(t *testing.T) {
	kubeconfig := staticKubeconfig(t)
	configFile := filepath.Join(t.TempDir(), "config")
	writeConfig(t, configFile, rewrittenEntry)
	_, err := run(t, "open", "--kubeconfig", kubeconfig, "--ssh-config-file", configFile, "ws1")
	if err == nil || !strings.Contains(err.Error(), "cannot read") || !strings.Contains(err.Error(), "--cluster-name dev") {
		t.Errorf("error = %v; want the unreadable entry refused with the flag named", err)
	}
	_, err = run(t, "open", "--kubeconfig", kubeconfig, "--ssh-config-file", configFile, "--cluster-name", "dev", "ws1")
	if err == nil || strings.Contains(err.Error(), "cannot read") || strings.Contains(err.Error(), "routes") {
		t.Errorf("with the flag: error = %v; want open past the entry check, failing only at the unreachable operator", err)
	}
}

// Printing needs no file: open --print-only prints the derived alias when the SSH
// configuration cannot be read, and only a launch, which goes through the file, reports it.
func TestOpenPrintsWithoutReadingTheFile(t *testing.T) {
	kubeconfig := staticKubeconfig(t)
	dir := t.TempDir()
	link := filepath.Join(dir, "config")
	testutil.Symlink(t, filepath.Join(dir, "dotfiles", "ssh_config"), link)

	stdout, err := run(t, "open", "--kubeconfig", kubeconfig, "--ssh-config-file", link, "--print-only", "ws1")
	if err != nil || !strings.Contains(stdout, "ssh ws1.team-a.dev.jk8s\n") {
		t.Errorf("--print-only: stdout = %q, %v; want the derived alias", stdout, err)
	}
	_, err = run(t, "open", "--kubeconfig", kubeconfig, "--ssh-config-file", link, "ws1")
	if err == nil || !strings.Contains(err.Error(), "link to a missing file") {
		t.Errorf("error = %v, want the link named", err)
	}
}

// Under a KUBECONFIG list an earlier file can define the cluster a later file's context
// names; open would check the workspace in that cluster while the editor's ssh, reading the
// entry's file alone, reaches the context's own. open stops with the way to merge the files.
func TestOpenRefusesAContextSplitAcrossKubeconfigFiles(t *testing.T) {
	own, other := clientcmdapi.NewConfig(), clientcmdapi.NewConfig()
	own.Contexts["dev"] = &clientcmdapi.Context{Cluster: "dev", AuthInfo: "dev", Namespace: "team-a"}
	own.Clusters["dev"] = &clientcmdapi.Cluster{Server: "https://127.0.0.1:1"}
	own.AuthInfos["dev"] = &clientcmdapi.AuthInfo{Token: "t"}
	own.CurrentContext = "dev"
	other.Clusters["dev"] = &clientcmdapi.Cluster{Server: "https://127.0.0.1:2"}
	paths := writeKubeconfigs(t, other, own)
	t.Setenv("KUBECONFIG", strings.Join(paths, string(os.PathListSeparator)))
	configFile := filepath.Join(t.TempDir(), "config")
	installEntry(t, configFile, sshconfig.Entry{Cluster: "dev", Context: "dev", Kubeconfig: paths[1]})

	for _, extra := range [][]string{nil, {"--print-only"}} {
		args := append([]string{"open", "--ssh-config-file", configFile, "--context", "dev"}, extra...)
		_, err := run(t, append(args, "ws1")...)
		want := "the cluster it names is taken from " + paths[0]
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "--flatten") {
			t.Errorf("%v: error = %v; want %q and the merge hint", extra, err, want)
		}
	}
}
