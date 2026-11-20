/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package cli

import (
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

func TestSSHConfigWritesANonDefaultCredentialTimeout(t *testing.T) {
	kubeconfig := staticKubeconfig(t)
	stdout, err := run(t, "ssh-config", "--kubeconfig", kubeconfig, "--command", kwCommand, "--credential-timeout", "15s")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "--context dev --credential-timeout 15s %h") {
		t.Errorf("entry =\n%s\nwant the timeout in the ProxyCommand", stdout)
	}

	stdout, err = run(t, "ssh-config", "--kubeconfig", kubeconfig, "--command", kwCommand)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout, "--credential-timeout") {
		t.Errorf("entry =\n%s\nwant connect's default left implicit", stdout)
	}

	_, err = run(t, "ssh-config", "--kubeconfig", kubeconfig, "--command", kwCommand, "--credential-timeout", "0")
	if err == nil || !strings.Contains(err.Error(), "--credential-timeout must be positive") {
		t.Errorf("error = %v", err)
	}
}

func TestSSHConfigRendersTheEntryForTheContext(t *testing.T) {
	kubeconfig := staticKubeconfig(t)
	stdout, err := run(t, "ssh-config", "--kubeconfig", kubeconfig, "--command", kwCommand)
	if err != nil {
		t.Fatal(err)
	}
	want := sshconfig.Entry{
		Cluster: "dev", Context: "dev", Kubeconfig: kubeconfig, Command: kwCommand, User: "jovyan",
		Windows: runtime.GOOS == goosWindows,
	}.Render()
	if stdout != want {
		t.Errorf("stdout =\n%s\nwant\n%s", stdout, want)
	}
}

func TestSSHConfigWritesThePlaintextOptInIntoTheEntry(t *testing.T) {
	kubeconfig := staticKubeconfig(t)
	stdout, err := run(t, "ssh-config", "--kubeconfig", kubeconfig, "--command", kwCommand, "--insecure-allow-plaintext")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "--insecure-allow-plaintext %h") {
		t.Errorf("entry =\n%s\nwant connect's --insecure-allow-plaintext in the ProxyCommand", stdout)
	}
}

// An EKS context is named by its ARN, which cannot be a host-name label, so the
// cluster name inside the ARN serves unless --cluster-name says otherwise.
func TestSSHConfigNamesTheClusterAfterTheEKSContext(t *testing.T) {
	kubeconfig := staticKubeconfig(t)
	wantLine := "ProxyCommand " + kwCommand + " connect --stdio --kubeconfig " + kubeconfig +
		" --context " + testutil.ARNContext + " %h"
	for _, tt := range []struct {
		name  string
		extra []string
		host  string
	}{
		{name: "the ARN's cluster name by default", host: "Host *.dev.jk8s\n"},
		{name: "--cluster-name overrides", extra: []string{"--cluster-name", "jd"}, host: "Host *.jd.jk8s\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]string{"ssh-config", "--kubeconfig", kubeconfig, "--context", testutil.ARNContext,
				"--command", kwCommand}, tt.extra...)
			stdout, err := run(t, args...)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(stdout, tt.host) || !strings.Contains(stdout, wantLine) {
				t.Errorf("stdout =\n%s\nwant %q and %q", stdout, tt.host, wantLine)
			}
		})
	}
}

// Running ssh-config --write again replaces the entry, which holds when the cluster name
// chosen the first time is found again without the flag.
func TestSSHConfigReusesTheClusterNameOfTheInstalledEntry(t *testing.T) {
	kubeconfig := staticKubeconfig(t)
	file := filepath.Join(t.TempDir(), "config")
	write := func(extra ...string) {
		args := append([]string{"ssh-config", "--write", "--ssh-config-file", file, "--kubeconfig", kubeconfig,
			"--context", testutil.ARNContext, "--command", kwCommand}, extra...)
		if _, err := run(t, args...); err != nil {
			t.Fatal(err)
		}
	}
	write("--cluster-name", "jd")
	write("--credential-timeout", "15s")
	content, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(content), "kubectl-workspace: start") != 1 || !strings.Contains(string(content), "Host *.jd.jk8s") ||
		!strings.Contains(string(content), "--credential-timeout 15s") {
		t.Errorf("file after the second write:\n%s\nwant one entry, still for jd, with the new timeout", content)
	}

	stdout, err := run(t, "ssh-config", "--remove", "--ssh-config-file", file, "--kubeconfig", kubeconfig,
		"--context", testutil.ARNContext)
	if err != nil || !strings.HasPrefix(stdout, "Removed the entry for Host *.jd.jk8s") {
		t.Errorf("--remove without the flag: %q, %v; want the jd entry removed", stdout, err)
	}
}

// The cluster name in the alias comes from the context: lowercased, as OpenSSH matches
// hosts, or for an EKS cluster ARN the cluster name inside it; a context that is no
// label even then needs the flag.
func TestSSHConfigDerivesTheClusterNameFromTheContext(t *testing.T) {
	tests := []struct {
		name, context, wantHost, wantErr string
	}{
		{name: "a plain context", context: "dev", wantHost: "Host *.dev.jk8s\n"},
		{name: "a mixed-case context", context: "Dev", wantHost: "Host *.dev.jk8s\n"},
		{name: "an EKS cluster ARN", context: "arn:aws:eks:us-west-2:123456789012:cluster/MyCluster",
			wantHost: "Host *.mycluster.jk8s\n"},
		{name: "a context that is no label", context: "Dev_Cluster", wantErr: "--cluster-name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kubeconfig := testutil.Kubeconfig{Name: tt.context, Server: "https://127.0.0.1:1", Namespace: "team-a"}.Write(t)
			stdout, err := run(t, "ssh-config", "--kubeconfig", kubeconfig, "--command", kwCommand)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want a hint to pass %s", err, tt.wantErr)
				}
				return
			}
			// The context keeps its case in the ProxyCommand; only the cluster name in the alias is lowercased.
			if err != nil || !strings.Contains(stdout, tt.wantHost) || !strings.Contains(stdout, "--context "+tt.context+" ") {
				t.Errorf("stdout = %q, %v; want %q and the context as named", stdout, err, tt.wantHost)
			}
		})
	}
}

// Installing the same context under a new name leaves the old entry in place; the
// warning names it and the printed command, run as printed, removes it.
func TestSSHConfigWarnsAboutAnEntryLeftUnderAnotherName(t *testing.T) {
	kubeconfig := staticKubeconfig(t)
	file := filepath.Join(t.TempDir(), "config")
	base := []string{"ssh-config", "--write", "--ssh-config-file", file, "--kubeconfig", kubeconfig, "--command", kwCommand}
	if _, err := run(t, base...); err != nil {
		t.Fatal(err)
	}
	_, stderr, err := runWithStderr(t, append(base, "--cluster-name", "jd")...)
	if err != nil {
		t.Fatal(err)
	}
	hint := "kubectl workspace ssh-config --remove --ssh-config-file " + file + " --kubeconfig " + kubeconfig +
		" --context dev --cluster-name dev"
	want := "kubectl-workspace: " + file + " also routes context \"dev\" under the cluster name \"dev\"; remove it with: " + hint + "\n"
	if stderr != want {
		t.Fatalf("stderr = %q\nwant %q", stderr, want)
	}

	if _, err := run(t, sshconfig.SplitArgs(hint, runtime.GOOS == goosWindows)[2:]...); err != nil {
		t.Fatalf("the printed command failed: %v", err)
	}
	content, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(content), "kubectl-workspace: start") != 1 || !strings.Contains(string(content), "Host *.jd.jk8s") {
		t.Errorf("file after the printed command:\n%s\nwant the jd entry alone", content)
	}
}

// An entry written without a kubeconfig file, by hand or by an earlier build, is read as
// the default file's, so a write from the default lookup under a new name names it.
func TestSSHConfigWarnsAboutAnEntryWithoutAFileLeftUnderAnotherName(t *testing.T) {
	useAsDefaultKubeconfig(t, staticKubeconfig(t))
	file := filepath.Join(t.TempDir(), "config")
	installEntry(t, file, sshconfig.Entry{Cluster: "first", Context: "dev"})
	_, stderr, err := runWithStderr(t, "ssh-config", "--write", "--ssh-config-file", file, "--command", kwCommand,
		"--cluster-name", "second")
	want := `also routes context "dev" under the cluster name "first"`
	if err != nil || !strings.Contains(stderr, want) || !strings.Contains(stderr, "--cluster-name first") {
		t.Errorf("stderr = %q, %v; want the entry named with its removal command", stderr, err)
	}
}

// A rerun replaces the cluster's own entry, and an entry routing a context of the
// same name for another kubeconfig file belongs to another cluster; neither draws
// the warning, whose hint would remove an entry still in use.
func TestSSHConfigWarnsAboutNeitherItsOwnEntryNorAnotherKubeconfigs(t *testing.T) {
	first, second := staticKubeconfig(t), staticKubeconfig(t)
	file := filepath.Join(t.TempDir(), "config")
	for cluster, kubeconfig := range map[string]string{"first": first, "second": second} {
		installEntry(t, file, sshconfig.Entry{Cluster: cluster, Context: "dev", Kubeconfig: kubeconfig})
	}
	stdout, stderr, err := runWithStderr(t, "ssh-config", "--write", "--ssh-config-file", file, "--kubeconfig", first,
		"--command", kwCommand, "--credential-timeout", "15s")
	if err != nil || stderr != "" || !strings.HasPrefix(stdout, "Installed the entry for Host *.first.jk8s") {
		t.Errorf("stdout = %q, stderr = %q, %v; want the first entry rewritten and no warning", stdout, stderr, err)
	}
	content, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(content), "kubectl-workspace: start") != 2 ||
		!strings.Contains(string(content), "--credential-timeout 15s") {
		t.Errorf("file after the rerun:\n%s\nwant both entries, the first with the new timeout", content)
	}
}

// Printing needs no file: an absent or unreadable one yields the default cluster name, and
// only --write, which has to read the file, reports it.
func TestSSHConfigPrintsWithoutReadingTheFile(t *testing.T) {
	kubeconfig := staticKubeconfig(t)
	printEntry := func(file string) (string, error) {
		return run(t, "ssh-config", "--ssh-config-file", file, "--kubeconfig", kubeconfig, "--command", kwCommand)
	}
	dir := t.TempDir()
	stdout, err := printEntry(filepath.Join(dir, "missing", "config"))
	if err != nil || !strings.Contains(stdout, "Host *.dev.jk8s\n") {
		t.Errorf("print mode with an absent file: %q, %v; want the entry printed", stdout, err)
	}

	// A dotfiles link whose target is gone cannot be read, unlike an absent file.
	link := filepath.Join(dir, "config")
	testutil.Symlink(t, filepath.Join(dir, "dotfiles", "ssh_config"), link)
	stdout, err = printEntry(link)
	if err != nil || !strings.Contains(stdout, "Host *.dev.jk8s\n") {
		t.Errorf("print mode with a dangling link: %q, %v; want the entry printed", stdout, err)
	}
	for _, action := range []string{"--write", "--remove"} {
		_, err = run(t, "ssh-config", action, "--ssh-config-file", link, "--kubeconfig", kubeconfig, "--command", kwCommand)
		if err == nil || !strings.Contains(err.Error(), "link to a missing file") {
			t.Errorf("%s through the dangling link: error = %v, want the link named", action, err)
		}
	}
}

// A stray or lost marker makes the entry damage that --remove reports instead of
// claiming that nothing routes the context; the file is left for a hand to repair.
func TestSSHConfigRemoveReportsADamagedEntry(t *testing.T) {
	kubeconfig := staticKubeconfig(t)
	tests := []struct {
		name    string
		damage  func(content string) string
		wantErr string
	}{
		{name: "a stray start marker", wantErr: "two start markers",
			damage: func(content string) string { return content + "# kubectl-workspace: start dev\n" }},
		{name: "the start marker lost", wantErr: "precedes its start",
			damage: func(content string) string { return strings.TrimPrefix(content, "# kubectl-workspace: start dev\n") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "config")
			installEntry(t, file, sshconfig.Entry{Cluster: "dev", Context: "dev", Kubeconfig: kubeconfig})
			content, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			damaged := tt.damage(string(content))
			writeConfig(t, file, damaged)

			_, err = run(t, "ssh-config", "--remove", "--ssh-config-file", file, "--kubeconfig", kubeconfig)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) || !strings.Contains(err.Error(), "by hand") {
				t.Errorf("error = %v, want %q and a request to repair the file by hand", err, tt.wantErr)
			}
			if after, err := os.ReadFile(file); err != nil || string(after) != damaged {
				t.Errorf("file after the refused --remove:\n%s\n%v; want it untouched", after, err)
			}
		})
	}
}

// A marker name edited by hand into something that is no label keeps the entry out of
// the lookup, which asks for --cluster-name; the flag then names the marker as written.
func TestSSHConfigRemoveDropsAHandEditedName(t *testing.T) {
	kubeconfig := staticKubeconfig(t)
	file := filepath.Join(t.TempDir(), "config")
	writeConfig(t, file, renderedEntry("My Cluster", kubeconfig))

	_, err := run(t, "ssh-config", "--remove", "--ssh-config-file", file, "--kubeconfig", kubeconfig)
	if err == nil || !strings.Contains(err.Error(), "cannot be the cluster name in the alias") || !strings.Contains(err.Error(), "--cluster-name") {
		t.Errorf("error = %v, want the edited name refused as a label and the flag named", err)
	}
	stdout, err := run(t, "ssh-config", "--remove", "--ssh-config-file", file, "--cluster-name", "My Cluster")
	if err != nil || !strings.HasPrefix(stdout, "Removed the entry for Host *.My Cluster.jk8s") {
		t.Errorf("--cluster-name \"My Cluster\": %q, %v; want the entry removed", stdout, err)
	}
	if content, err := os.ReadFile(file); err != nil || len(content) != 0 {
		t.Errorf("file after --remove = %q, %v; want it empty", content, err)
	}
}

// A block pasted twice yields one damaged entry per start marker; the damage is said once.
func TestSSHConfigNamesDamageOnce(t *testing.T) {
	kubeconfig := staticKubeconfig(t)
	file := filepath.Join(t.TempDir(), "config")
	installEntry(t, file, sshconfig.Entry{Cluster: "dev", Context: "dev", Kubeconfig: kubeconfig})
	content, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	writeConfig(t, file, string(content)+string(content))

	_, stderr, err := runWithStderr(t, "ssh-config", "--write", "--ssh-config-file", file, "--kubeconfig", kubeconfig,
		"--command", kwCommand, "--cluster-name", "other")
	if err != nil || strings.Count(stderr, "two start markers") != 1 {
		t.Errorf("stderr = %q, %v; want the duplicated block named once", stderr, err)
	}
}

// rewrittenEntry is an entry for cluster dev whose ProxyCommand a user replaced by hand;
// the plugin cannot read a context out of it.
const rewrittenEntry = "# kubectl-workspace: start dev\nHost *.dev.jk8s\n  ProxyCommand ssh -W %h:%p jump\n# kubectl-workspace: end dev\n"

// brokenEntry is what is left of an entry for cluster jd routing context dev after its
// end marker was lost: OpenSSH still honours its Host line. Damage is attributed to the
// context whatever file the entry names, so it names none.
var brokenEntry = strings.TrimSuffix(renderedEntry("jd", ""), "# kubectl-workspace: end jd\n")

// --write under a free name and --remove of the intact entry, by context or by the name
// the pasted hint carries, all succeed next to a damaged entry routing the same context,
// and all say it is still there.
func TestSSHConfigSaysWhenADamagedEntryRoutesTheContext(t *testing.T) {
	kubeconfig := staticKubeconfig(t)
	wantDamage := func(t *testing.T, action, stderr string) {
		t.Helper()
		if !strings.Contains(stderr, `the entry for cluster "jd" has no end marker`) || !strings.Contains(stderr, "by hand") {
			t.Errorf("%s: stderr = %q; want the damaged jd entry named with a request to repair it by hand", action, stderr)
		}
	}
	for name, removeArgs := range map[string][]string{
		"by context":  {"--kubeconfig", kubeconfig},
		"by its name": {"--cluster-name", "dev"},
	} {
		t.Run(name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "config")
			writeConfig(t, file, brokenEntry)
			stdout, stderr, err := runWithStderr(t, "ssh-config", "--write", "--ssh-config-file", file, "--kubeconfig", kubeconfig,
				"--command", kwCommand)
			if err != nil || !strings.HasPrefix(stdout, "Installed the entry for Host *.dev.jk8s") {
				t.Fatalf("--write: stdout = %q, %v; want the dev entry installed", stdout, err)
			}
			wantDamage(t, "--write", stderr)

			args := append([]string{"ssh-config", "--remove", "--ssh-config-file", file}, removeArgs...)
			stdout, stderr, err = runWithStderr(t, args...)
			if err != nil || !strings.HasPrefix(stdout, "Removed the entry for Host *.dev.jk8s") {
				t.Fatalf("--remove: stdout = %q, %v; want the dev entry removed", stdout, err)
			}
			wantDamage(t, "--remove", stderr)
		})
	}
}

// Replacing an entry can change what its alias reaches: the context, or the kubeconfig
// file ssh resolves it in. Under the derived name a changed file is refused like a changed
// context; with --cluster-name the rerun says what changed.
func TestSSHConfigSaysWhenTheReplacementChangesTheKubeconfigFile(t *testing.T) {
	other := staticKubeconfig(t)
	useAsDefaultKubeconfig(t, staticKubeconfig(t))
	file := filepath.Join(t.TempDir(), "config")
	write := func(extra ...string) (string, error) {
		_, stderr, err := runWithStderr(t, append([]string{"ssh-config", "--write", "--ssh-config-file", file,
			"--command", kwCommand}, extra...)...)
		return stderr, err
	}

	if stderr, err := write(); err != nil || stderr != "" {
		t.Fatalf("first write: stderr = %q, %v; want a quiet install", stderr, err)
	}
	_, err := write("--kubeconfig", other)
	want := `already routes context "dev" of kubeconfig ` + kube.DefaultKubeconfig() + ` under the cluster name "dev"`
	if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "--cluster-name") {
		t.Errorf("rerun for another file: error = %v; want %q and the flag", err, want)
	}
	stderr, err := write("--kubeconfig", other, "--cluster-name", "dev")
	want = `routed context "dev" of kubeconfig ` + kube.DefaultKubeconfig() + ` and now routes context "dev" of kubeconfig ` + other
	if err != nil || !strings.Contains(stderr, want) {
		t.Errorf("deliberate rerun: stderr = %q, %v; want %q", stderr, err, want)
	}
	if stderr, err := write("--kubeconfig", other); err != nil || stderr != "" {
		t.Errorf("same rerun: stderr = %q, %v; want no warning", stderr, err)
	}
}

// An entry written without a kubeconfig file is the default file's; a rerun from the
// default lookup replaces it quietly, and the entry names the file from then on.
func TestSSHConfigReplacesAnEntryWithoutAFileQuietly(t *testing.T) {
	useAsDefaultKubeconfig(t, staticKubeconfig(t))
	file := filepath.Join(t.TempDir(), "config")
	installEntry(t, file, sshconfig.Entry{Cluster: "dev", Context: "dev"})

	stdout, stderr, err := runWithStderr(t, "ssh-config", "--write", "--ssh-config-file", file, "--command", kwCommand)
	if err != nil || stderr != "" || !strings.HasPrefix(stdout, "Installed the entry for Host *.dev.jk8s") {
		t.Errorf("stdout = %q, stderr = %q, %v; want the entry replaced without a warning", stdout, stderr, err)
	}
	content, err := os.ReadFile(file)
	if err != nil || strings.Count(string(content), "kubectl-workspace: start") != 1 ||
		!strings.Contains(string(content), "--kubeconfig "+kube.DefaultKubeconfig()+" --context dev ") {
		t.Errorf("file:\n%s\n%v; want one entry naming the default kubeconfig file", content, err)
	}
}

// Two contexts can derive the same cluster name, as two EKS clusters named alike in
// different regions do; a --write for the second must not take over the first's entry.
func TestSSHConfigRefusesToTakeOverAnotherContextsEntry(t *testing.T) {
	kubeconfig := staticKubeconfig(t) // its contexts dev and arn:...:cluster/dev both derive dev
	file := filepath.Join(t.TempDir(), "config")
	write := func(extra ...string) (string, error) {
		args := append([]string{"ssh-config", "--write", "--ssh-config-file", file, "--kubeconfig", kubeconfig,
			"--command", kwCommand}, extra...)
		return run(t, args...)
	}
	if _, err := write("--context", "dev"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}

	_, err = write("--context", testutil.ARNContext)
	if err == nil || !strings.Contains(err.Error(), `routes context "dev" under the cluster name "dev"`) ||
		!strings.Contains(err.Error(), "--cluster-name") {
		t.Errorf("error = %v, want the entry's context named and the flag", err)
	}
	if after, err := os.ReadFile(file); err != nil || string(after) != string(before) {
		t.Errorf("file after the refused --write:\n%s\n%v; want it untouched", after, err)
	}

	// With the name given, the replacement is deliberate and the changed route is said;
	// another name adds an entry.
	stdout, stderr, err := runWithStderr(t, "ssh-config", "--write", "--ssh-config-file", file, "--kubeconfig", kubeconfig,
		"--command", kwCommand, "--context", testutil.ARNContext, "--cluster-name", "dev")
	if err != nil || !strings.HasPrefix(stdout, "Installed the entry for Host *.dev.jk8s") ||
		!strings.Contains(stderr, `routed context "dev" and now routes context "`+testutil.ARNContext+`"`) {
		t.Errorf("--cluster-name dev: %q, stderr %q, %v; want the entry replaced with the route change on stderr",
			stdout, stderr, err)
	}
	stdout, err = write("--context", "dev", "--cluster-name", "dev-west")
	if err != nil || !strings.HasPrefix(stdout, "Installed the entry for Host *.dev-west.jk8s") {
		t.Errorf("--cluster-name dev-west: %q, %v; want a second entry", stdout, err)
	}
	content, err := os.ReadFile(file)
	if err != nil || strings.Count(string(content), "kubectl-workspace: start") != 2 ||
		!strings.Contains(string(content), "--context "+testutil.ARNContext+" ") {
		t.Errorf("file:\n%s\n%v; want the ARN entry under dev and a dev-west entry", content, err)
	}
}

// An entry whose ProxyCommand the plugin cannot read, one a user rewrote by hand, may be
// another cluster's: a --write under its derived name is refused, and --cluster-name
// replaces it on purpose.
func TestSSHConfigRefusesToReplaceAnEntryItCannotRead(t *testing.T) {
	kubeconfig := staticKubeconfig(t)
	file := filepath.Join(t.TempDir(), "config")
	writeConfig(t, file, rewrittenEntry)
	write := func(extra ...string) (string, error) {
		args := append([]string{"ssh-config", "--write", "--ssh-config-file", file, "--kubeconfig", kubeconfig,
			"--command", kwCommand}, extra...)
		return run(t, args...)
	}

	_, err := write()
	if err == nil || !strings.Contains(err.Error(), "cannot read") || !strings.Contains(err.Error(), "--cluster-name dev") {
		t.Errorf("error = %v; want the unreadable entry refused with the flag named", err)
	}
	if content, err := os.ReadFile(file); err != nil || string(content) != rewrittenEntry {
		t.Errorf("file after the refused --write:\n%s\n%v; want it untouched", content, err)
	}
	stdout, err := write("--cluster-name", "dev")
	if err != nil || !strings.HasPrefix(stdout, "Installed the entry for Host *.dev.jk8s") {
		t.Errorf("--cluster-name dev: %q, %v; want the entry replaced", stdout, err)
	}
	if content, err := os.ReadFile(file); err != nil || strings.Contains(string(content), "jump") ||
		!strings.Contains(string(content), "--context dev ") {
		t.Errorf("file:\n%s\n%v; want the rewritten entry gone and the plugin's in its place", content, err)
	}
}

// A marker name edited by hand may not be safe on a command line: the warning names the
// entry and leaves the removal to a hand instead of printing a command to paste.
func TestSSHConfigNamesAnEntryUnderAnUnsafeNameWithoutAHint(t *testing.T) {
	kubeconfig := staticKubeconfig(t)
	file := filepath.Join(t.TempDir(), "config")
	writeConfig(t, file, renderedEntry("x&calc!", kubeconfig))

	_, stderr, err := runWithStderr(t, "ssh-config", "--write", "--ssh-config-file", file, "--kubeconfig", kubeconfig,
		"--command", kwCommand, "--cluster-name", "new")
	if err != nil || !strings.Contains(stderr, `"x&calc!"`) || !strings.Contains(stderr, "remove it by hand") ||
		strings.Contains(stderr, "--cluster-name") {
		t.Errorf("stderr = %q, %v; want the entry named for a hand removal and no command to paste", stderr, err)
	}
}

// --remove without --cluster-name acts only on an entry that routes the context in the
// kubeconfig file in use; the derived name could be another cluster's entry, for another
// context or for the same context name in another file. The flag names that entry.
func TestSSHConfigRemoveLeavesAnotherClustersEntryAlone(t *testing.T) {
	kubeconfig, other := staticKubeconfig(t), staticKubeconfig(t)
	for name, installed := range map[string]sshconfig.Entry{
		"another context":                       {Cluster: "dev", Context: testutil.ARNContext, Kubeconfig: kubeconfig},
		"the same context name in another file": {Cluster: "dev", Context: "dev", Kubeconfig: other},
	} {
		t.Run(name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "config")
			installEntry(t, file, installed)
			before, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}

			stdout, err := run(t, "ssh-config", "--remove", "--ssh-config-file", file, "--kubeconfig", kubeconfig, "--context", "dev")
			want := "No entry in " + file + " routes context \"dev\" of kubeconfig " + kubeconfig +
				"; pass --cluster-name to remove an entry by its name.\n"
			if err != nil || stdout != want {
				t.Errorf("stdout = %q, %v; want %q", stdout, err, want)
			}
			if after, err := os.ReadFile(file); err != nil || string(after) != string(before) {
				t.Errorf("file:\n%s\n%v; want the other cluster's entry kept", after, err)
			}
			stdout, err = run(t, "ssh-config", "--remove", "--ssh-config-file", file, "--cluster-name", "dev")
			if err != nil || !strings.HasPrefix(stdout, "Removed the entry for Host *.dev.jk8s") {
				t.Errorf("--cluster-name dev: %q, %v; want the entry removed by its name", stdout, err)
			}
		})
	}
}

func TestSSHConfigRemovesTheEntry(t *testing.T) {
	kubeconfig := staticKubeconfig(t)
	file := filepath.Join(t.TempDir(), "config")
	writeConfig(t, file, "Host github.com\n  User git\n")
	if _, err := run(t, "ssh-config", "--write", "--ssh-config-file", file, "--kubeconfig", kubeconfig,
		"--command", kwCommand); err != nil {
		t.Fatal(err)
	}

	stdout, err := run(t, "ssh-config", "--remove", "--ssh-config-file", file, "--kubeconfig", kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	if want := "Removed the entry for Host *.dev.jk8s from " + file + ".\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	if got, err := os.ReadFile(file); err != nil || string(got) != "Host github.com\n  User git\n" {
		t.Errorf("file after --remove = %q, %v; want the original content", got, err)
	}

	stdout, err = run(t, "ssh-config", "--remove", "--ssh-config-file", file, "--kubeconfig", kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	want := "No entry in " + file + " routes context \"dev\" of kubeconfig " + kubeconfig +
		"; pass --cluster-name to remove an entry by its name.\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	stdout, err = run(t, "ssh-config", "--remove", "--ssh-config-file", file, "--cluster-name", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if want := "No entry for cluster \"dev\" in " + file + ".\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

func TestSSHConfigRefusesWriteWithRemove(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config")
	_, err := run(t, "ssh-config", "--remove", "--write", "--ssh-config-file", file, "--kubeconfig", staticKubeconfig(t))
	if err == nil || !strings.Contains(err.Error(), "none of the others can be") {
		t.Errorf("error = %v, want --write and --remove refused together", err)
	}
}

// The context is often gone by the time its entry is removed; the flag is the way around it.
func TestSSHConfigRemoveNamesTheFlagWhenTheContextIsGone(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config")
	_, err := run(t, "ssh-config", "--remove", "--ssh-config-file", file, "--kubeconfig", staticKubeconfig(t), "--context", "gone")
	if err == nil || !strings.Contains(err.Error(), "pass --cluster-name to remove the entry without reading the kubeconfig") {
		t.Errorf("error = %v, want the flag named as the way around a missing context", err)
	}
}

// With two entries routing the context, --remove without --cluster-name cannot tell
// which one to drop; the flag names it and has to be a label itself.
func TestSSHConfigRemoveNeedsTheClusterNameToTellTwoEntriesApart(t *testing.T) {
	kubeconfig := staticKubeconfig(t)
	file := filepath.Join(t.TempDir(), "config")
	for _, cluster := range []string{"dev", "jd"} {
		installEntry(t, file, sshconfig.Entry{Cluster: cluster, Context: "dev", Kubeconfig: kubeconfig})
	}
	remove := func(extra ...string) (string, error) {
		args := append([]string{"ssh-config", "--remove", "--ssh-config-file", file, "--kubeconfig", kubeconfig},
			extra...)
		return run(t, args...)
	}

	_, err := remove()
	if err == nil || !strings.Contains(err.Error(), "dev, jd") || !strings.Contains(err.Error(), "--cluster-name") {
		t.Errorf("error = %v, want both names and the flag to disambiguate", err)
	}
	if content, err := os.ReadFile(file); err != nil || strings.Count(string(content), "kubectl-workspace: start") != 2 {
		t.Errorf("file after the refused --remove:\n%s\n%v; want both entries kept", content, err)
	}

	stdout, err := remove("--cluster-name", "My Cluster")
	if want := "No entry for cluster \"My Cluster\" in " + file + ".\n"; err != nil || stdout != want {
		t.Errorf("--cluster-name \"My Cluster\": %q, %v; want %q", stdout, err, want)
	}

	stdout, err = remove("--cluster-name", "jd")
	if err != nil || !strings.HasPrefix(stdout, "Removed the entry for Host *.jd.jk8s") {
		t.Errorf("--cluster-name jd: %q, %v; want the jd entry removed", stdout, err)
	}
	stdout, err = remove()
	if err != nil || !strings.HasPrefix(stdout, "Removed the entry for Host *.dev.jk8s") {
		t.Errorf("with one entry left: %q, %v; want it found without the flag", stdout, err)
	}
}

// The entry names the kubeconfig file the context was found in, whichever way it was
// found, so ssh resolves the context in that file whatever its own environment says.
func TestSSHConfigWritesTheKubeconfigFileIntoTheEntry(t *testing.T) {
	first := staticKubeconfig(t)
	second := testutil.Kubeconfig{Name: "other", Server: "https://127.0.0.1:1"}.Write(t)
	tests := []struct {
		name     string
		setup    func(t *testing.T)
		args     []string
		wantFile string
	}{
		{name: "the --kubeconfig flag", args: []string{"--kubeconfig", first}, wantFile: first},
		{name: "a single-file KUBECONFIG", setup: func(t *testing.T) { t.Setenv("KUBECONFIG", first) }, wantFile: first},
		{
			name:     "the file of a KUBECONFIG list that defines the context",
			setup:    func(t *testing.T) { t.Setenv("KUBECONFIG", second+string(os.PathListSeparator)+first) },
			args:     []string{"--context", "dev"},
			wantFile: first,
		},
		{
			name:     "the default lookup",
			setup:    func(t *testing.T) { useAsDefaultKubeconfig(t, first) },
			wantFile: kube.DefaultKubeconfig(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setup != nil {
				tt.setup(t)
			}
			stdout, stderr, err := runWithStderr(t, append([]string{"ssh-config", "--command", kwCommand}, tt.args...)...)
			if err != nil || stderr != "" {
				t.Fatalf("stderr = %q, %v", stderr, err)
			}
			if want := "--kubeconfig " + tt.wantFile + " --context dev "; !strings.Contains(stdout, want) {
				t.Errorf("entry =\n%s\nwant %q", stdout, want)
			}
		})
	}
}

// Under a KUBECONFIG list the context's cluster or user can come from another file than
// the context; the entry names one file, which connect reads alone, so the write stops
// with the way to merge the files. The ways a context can be split are pinned in kube.
func TestSSHConfigRefusesAContextSplitAcrossKubeconfigFiles(t *testing.T) {
	own, other := clientcmdapi.NewConfig(), clientcmdapi.NewConfig()
	own.Contexts["dev"] = &clientcmdapi.Context{Cluster: "dev", AuthInfo: "dev"}
	own.Clusters["dev"] = &clientcmdapi.Cluster{Server: "https://127.0.0.1:1"}
	own.CurrentContext = "dev"
	other.AuthInfos["dev"] = &clientcmdapi.AuthInfo{Token: "t"}
	paths := writeKubeconfigs(t, own, other)
	t.Setenv("KUBECONFIG", strings.Join(paths, string(os.PathListSeparator)))

	_, err := run(t, "ssh-config", "--command", kwCommand)
	want := "the user it names is taken from " + paths[1]
	if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "--flatten") {
		t.Errorf("error = %v; want %q and the merge hint", err, want)
	}
}

// The installed entry is a command line OpenSSH runs verbatim, so it is run here
// the same way, with the alias in place of %h.
func TestInstalledEntryRunsConnect(t *testing.T) {
	echo := testutil.NewTLSEchoServer(t)
	caFile := writeCAFile(t, echo.CAPEM())
	op := grantingOperator(t, echo)
	kubeconfig := operatorKubeconfig(t, op)
	configFile := filepath.Join(t.TempDir(), ".ssh", "config")
	stdout, err := run(t, "ssh-config", "--write", "--kubeconfig", kubeconfig, "--context", testutil.ARNContext,
		"--cluster-name", "dev", "--command", kwCommand, "--ssh-config-file", configFile, "--ca-file", caFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "Connect with: ssh <workspace>.<namespace>.dev.jk8s") {
		t.Errorf("stdout = %q, want the alias form to connect with", stdout)
	}

	content, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatal(err)
	}
	var proxyCommand []string
	for _, line := range strings.Split(string(content), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "ProxyCommand "); ok {
			proxyCommand = sshconfig.SplitArgs(rest, runtime.GOOS == goosWindows)
		}
	}
	if len(proxyCommand) == 0 || proxyCommand[0] != kwCommand {
		t.Fatalf("no ProxyCommand line in\n%s", content)
	}
	args := proxyCommand[1:]
	args[len(args)-1] = strings.ReplaceAll(args[len(args)-1], "%h", alias)

	r := startConnect(t, args...)
	if got := r.echo(t, "through the installed entry"); got != "through the installed entry" {
		t.Errorf("stdout = %q", got)
	}
	if err := r.finish(t); err != nil {
		t.Errorf("connect returned %v (args %q)", err, args)
	}
	if upgrade := echo.LastHandshake(t); upgrade.Authorization != "Bearer "+grantedToken {
		t.Errorf("WebSocket Authorization = %q", upgrade.Authorization)
	}
}

func TestSSHConfigChecksTheCAFileAndWritesItsAbsolutePath(t *testing.T) {
	echo := testutil.NewTLSEchoServer(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ca.pem"), echo.CAPEM(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "junk.pem"), []byte("not a certificate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	kubeconfig := staticKubeconfig(t)
	file := filepath.Join(dir, "ssh_config")

	_, err := run(t, "ssh-config", "--write", "--ssh-config-file", file, "--kubeconfig", kubeconfig,
		"--command", kwCommand, "--ca-file", "junk.pem")
	if err == nil || !strings.Contains(err.Error(), "no certificates") {
		t.Errorf("error = %v, want the empty bundle refused", err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("the entry was written although the CA file was refused (stat: %v)", err)
	}

	if _, err := run(t, "ssh-config", "--write", "--ssh-config-file", file, "--kubeconfig", kubeconfig,
		"--command", kwCommand, "--ca-file", "ca.pem"); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if want := "--ca-file " + filepath.Join(dir, "ca.pem") + " %h"; !strings.Contains(string(written), want) {
		t.Errorf("entry =\n%s\nwant the relative CA file path made absolute: %s", written, want)
	}
}

func TestSSHConfigResolvesTheCommandPath(t *testing.T) {
	kubeconfig := staticKubeconfig(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	stdout, err := run(t, "ssh-config", "--kubeconfig", kubeconfig, "--command", "bin/kubectl-workspace")
	if err != nil {
		t.Fatal(err)
	}
	want := "ProxyCommand " + filepath.Join(cwd, "bin", "kubectl-workspace") + " connect"
	if !strings.Contains(stdout, want) {
		t.Errorf("entry =\n%s\nwant the relative path made absolute: %s", stdout, want)
	}

	stdout, err = run(t, "ssh-config", "--kubeconfig", kubeconfig, "--command", "kubectl-workspace")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "ProxyCommand kubectl-workspace connect") {
		t.Errorf("entry =\n%s\nwant a bare name left to the PATH lookup", stdout)
	}
}

// The entry's kubeconfig file is matched as a file, not a spelling: an entry written
// through a dotfiles link is the entry for the file the link points at, so a rerun through
// the other spelling neither takes it for another cluster nor reports a changed route.
func TestSSHConfigTreatsTwoSpellingsOfTheKubeconfigAsOneFile(t *testing.T) {
	kubeconfig := staticKubeconfig(t)
	link := filepath.Join(t.TempDir(), "kubeconfig")
	testutil.Symlink(t, kubeconfig, link)
	file := filepath.Join(t.TempDir(), "config")
	if _, err := run(t, "ssh-config", "--write", "--ssh-config-file", file, "--kubeconfig", kubeconfig, "--command", kwCommand,
		"--cluster-name", "linked"); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := runWithStderr(t, "ssh-config", "--write", "--ssh-config-file", file, "--kubeconfig", link,
		"--command", kwCommand)
	if err != nil || !strings.HasPrefix(stdout, "Installed the entry for Host *.linked.jk8s") || stderr != "" {
		t.Errorf("rerun through the link: %q, stderr %q, %v; want the linked entry found and replaced quietly", stdout, stderr, err)
	}
}

// useAsDefaultKubeconfig installs the kubeconfig as ~/.kube/config in the test package's
// own home directory, for a command run without --kubeconfig and KUBECONFIG.
func useAsDefaultKubeconfig(t *testing.T, kubeconfig string) {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	defaultKubeconfig := filepath.Join(home, ".kube", "config")
	if err := os.MkdirAll(filepath.Dir(defaultKubeconfig), 0o700); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(defaultKubeconfig, content, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(defaultKubeconfig)) })
}
