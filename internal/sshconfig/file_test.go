/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package sshconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jupyter-infra/kubectl-workspace/internal/testutil"
)

func TestInstallCreatesThePrivateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".ssh", "config")
	if err := install(path, testEntry()); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != testEntry().Render() {
		t.Errorf("new file =\n%s\nwant the rendered entry alone", got)
	}
	if runtime.GOOS == "windows" {
		return
	}
	// OpenSSH refuses a configuration file others can write.
	assertMode(t, path, 0o600)
	assertMode(t, filepath.Dir(path), 0o700)
}

func TestInstallWritesThroughASymlinkedFile(t *testing.T) {
	target := writeFile(t, "Host github.com\n  User git\n")
	link := filepath.Join(t.TempDir(), "config")
	testutil.Symlink(t, target, link)

	if err := install(link, testEntry()); err != nil {
		t.Fatal(err)
	}

	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("the symlink was replaced by a regular file")
	}
	want := testEntry().Render() + "\nHost github.com\n  User git\n"
	if got := readFile(t, target); got != want {
		t.Errorf("target file =\n%s\nwant\n%s", got, want)
	}
	if _, ok, err := entryFor(link, "prod"); err != nil || !ok {
		t.Errorf("EntryFor through the link = %v, %v", ok, err)
	}
}

func TestInstallPlacesTheEntry(t *testing.T) {
	rendered := testEntry().Render()
	tests := []struct {
		name     string
		existing string
		want     string
	}{
		{
			name:     "appended after the options when there is no Host block",
			existing: "ServerAliveInterval 60\n",
			want:     "ServerAliveInterval 60\n\n" + rendered,
		},
		{
			name:     "before the first Host block, after an Include",
			existing: "Include ~/.ssh/conf.d/*\n\nHost github.com\n  User git\n\nHost *\n  ServerAliveInterval 60\n",
			want: "Include ~/.ssh/conf.d/*\n\n" + rendered +
				"\nHost github.com\n  User git\n\nHost *\n  ServerAliveInterval 60\n",
		},
		{
			name:     "before a Match block",
			existing: "Match host bastion\n  User ops\n",
			want:     rendered + "\nMatch host bastion\n  User ops\n",
		},
		{
			name:     "before a lowercase keyword=value block",
			existing: "host=github.com\n  User git\n",
			want:     rendered + "\nhost=github.com\n  User git\n",
		},
		{
			name:     "separated from the preceding line by a blank line",
			existing: "Include ~/.ssh/conf.d/*\nHost github.com\n  User git\n",
			want:     "Include ~/.ssh/conf.d/*\n\n" + rendered + "\nHost github.com\n  User git\n",
		},
		{
			name:     "above the comment that belongs to the first block",
			existing: "Include ~/.ssh/conf.d/*\n# GitHub\nHost github.com\n  User git\n",
			want:     "Include ~/.ssh/conf.d/*\n\n" + rendered + "\n# GitHub\nHost github.com\n  User git\n",
		},
		{
			name:     "a file without a trailing newline",
			existing: "Host github.com\n  User git",
			want:     rendered + "\nHost github.com\n  User git\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeFile(t, tt.existing)
			if err := install(path, testEntry()); err != nil {
				t.Fatal(err)
			}
			if got := readFile(t, path); got != tt.want {
				t.Errorf("file =\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}

func TestInstallRefusesALinkToAMissingFile(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "config")
	testutil.Symlink(t, filepath.Join(dir, "dotfiles", "ssh_config"), link)

	err := install(link, testEntry())
	if err == nil || !strings.Contains(err.Error(), "link to a missing file") {
		t.Errorf("Install error = %v, want the dangling link named", err)
	}
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link was replaced: %v, %v", info, err)
	}
}

func TestInstallKeepsEachClusterInsideItsOwnMarkers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	staging, prod := testEntry(), testEntry()
	staging.Cluster, prod.Cluster = "staging", "prod"
	for _, e := range []Entry{staging, prod, staging} {
		if err := install(path, e); err != nil {
			t.Fatal(err)
		}
	}

	// A new cluster lands before the existing entry; the reinstall replaces staging in place.
	want := prod.Render() + "\n" + staging.Render()
	if got := readFile(t, path); got != want {
		t.Errorf("file =\n%s\nwant\n%s", got, want)
	}
}

func TestInstallKeepsTheFilesMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are Unix")
	}
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte("Host github.com\n  User git\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := install(path, testEntry()); err != nil {
		t.Fatal(err)
	}
	assertMode(t, path, 0o644)
}

func TestInstallLeavesTheFileAloneWhenTheDirectoryIsReadOnly(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("needs Unix permissions enforced for a non-root user")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte("Host github.com\n  User git\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err := install(path, testEntry())
	if err == nil || !strings.Contains(err.Error(), dir) {
		t.Errorf("Install error = %v, want it to name the directory %s", err, dir)
	}
	if got := readFile(t, path); got != "Host github.com\n  User git\n" {
		t.Errorf("a failed install changed the file:\n%s", got)
	}
}

func TestInstallReplacesTheEntryForTheSameCluster(t *testing.T) {
	old := testEntry()
	old.Command = "/old/path/kubectl-workspace"
	other := testEntry()
	other.Cluster = "staging"
	other.Context = "staging"
	existing := "Host github.com\n  User git\n\n" + old.Render() + "\n" + other.Render() + "\nHost *\n  ServerAliveInterval 60\n"
	path := writeFile(t, existing)

	if err := install(path, testEntry()); err != nil {
		t.Fatal(err)
	}

	got := readFile(t, path)
	want := "Host github.com\n  User git\n\n" + testEntry().Render() + "\n" + other.Render() + "\nHost *\n  ServerAliveInterval 60\n"
	if got != want {
		t.Errorf("file =\n%s\nwant\n%s", got, want)
	}
	if strings.Count(got, startMarker("prod")) != 1 {
		t.Errorf("expected exactly one entry for prod, got %d", strings.Count(got, startMarker("prod")))
	}
}

func TestInstallKeepsCRLF(t *testing.T) {
	path := writeFile(t, "Host github.com\r\n  User git\r\n")
	if err := install(path, testEntry()); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, path)
	if strings.Contains(strings.ReplaceAll(got, "\r\n", ""), "\n") {
		t.Errorf("expected every line ending to be CRLF, got\n%q", got)
	}
	if !strings.HasPrefix(got, startMarker("prod")+"\r\nHost *.prod.jk8s\r\n") {
		t.Errorf("entry not written with CRLF:\n%q", got)
	}
}

func TestInstallRejectsAPartialEntry(t *testing.T) {
	path := writeFile(t, startMarker("prod")+"\nHost *.prod.jk8s\n  User jovyan\n")
	err := install(path, testEntry())
	if err == nil || !strings.Contains(err.Error(), "remove it by hand") {
		t.Fatalf("Install error = %v, want a request to repair the file by hand", err)
	}
	if got := readFile(t, path); !strings.HasPrefix(got, startMarker("prod")) {
		t.Errorf("a failed install must leave the file untouched, got\n%s", got)
	}
}

func TestEntryFor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if _, ok, err := entryFor(path, "prod"); err != nil || ok {
		t.Fatalf("EntryFor on a missing file = %v, %v; want false, nil", ok, err)
	}
	if err := install(path, testEntry()); err != nil {
		t.Fatal(err)
	}
	entry, ok, err := entryFor(path, "prod")
	if err != nil || !ok || entry.Context != testEntry().Context || entry.Kubeconfig != testEntry().Kubeconfig {
		t.Errorf("EntryFor after Install = %+v, %v, %v; want the installed entry's context and kubeconfig", entry, ok, err)
	}
	if _, ok, err := entryFor(path, "staging"); err != nil || ok {
		t.Errorf("EntryFor for another cluster = %v, %v; want false, nil", ok, err)
	}
}

// A hand edit may spell the keyword or the flags the other ways ssh accepts; the entry
// reads back the same.
func TestEntryForReadsHandEditedSpellings(t *testing.T) {
	for _, line := range []string{
		"ProxyCommand kw connect --stdio --kubeconfig=/k --context=prod %h",
		"proxycommand kw connect --stdio --kubeconfig /k --context prod %h",
		"ProxyCommand=kw connect --stdio --kubeconfig /k --context prod %h",
		"ProxyCommand = kw connect --stdio --kubeconfig /k --context prod %h",
	} {
		path := writeFile(t, startMarker("prod")+"\nHost *.prod.jk8s\n  "+line+"\n"+endMarker("prod")+"\n")
		entry, ok, err := entryFor(path, "prod")
		if err != nil || !ok || entry.Context != "prod" || entry.Kubeconfig != "/k" {
			t.Errorf("%q: EntryFor = %+v, %v, %v; want context prod and kubeconfig /k", line, entry, ok, err)
		}
	}
}

func TestEntryForRejectsAPartialEntry(t *testing.T) {
	path := writeFile(t, endMarker("prod")+"\n")
	if _, _, err := entryFor(path, "prod"); err == nil || !strings.Contains(err.Error(), "remove it by hand") {
		t.Errorf("EntryFor error = %v, want a request to repair the file by hand", err)
	}
}

// A block pasted twice, or a stray marker after a complete block, is a damaged file:
// the lookup marks it and the writes refuse it, instead of acting on the first copy.
func TestDuplicatedMarkersAreDamage(t *testing.T) {
	rendered := testEntry().Render()
	lines := strings.Split(strings.TrimSuffix(rendered, "\n"), "\n")
	swapped := append([]string{}, lines...)
	swapped[0], swapped[len(swapped)-1] = swapped[len(swapped)-1], swapped[0]
	other := testEntry()
	other.Cluster, other.Context = "other", "staging"
	otherLines := strings.Split(strings.TrimSuffix(other.Render(), "\n"), "\n")
	nested := append(append(append([]string{}, lines[:len(lines)-1]...), otherLines[:len(otherLines)-1]...),
		lines[len(lines)-1], otherLines[len(otherLines)-1])
	tests := map[string]string{
		"the block twice":            rendered + rendered,
		"a stray start marker after": rendered + startMarker("prod") + "\n",
		"a stray end marker after":   rendered + endMarker("prod") + "\n",
		"the start marker lost":      strings.TrimPrefix(rendered, startMarker("prod")+"\n"),
		"the markers swapped":        strings.Join(swapped, "\n") + "\n",
		"two blocks nested":          strings.Join(nested, "\n") + "\n",
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			path := writeFile(t, content)
			entries, err := entriesForContext(path, "prod")
			if err != nil || len(entries) == 0 {
				t.Fatalf("entries = %v, %v; want the entry returned, marked damaged", entries, err)
			}
			for _, e := range entries {
				if e.Damaged == nil || e.Cluster != "prod" {
					t.Errorf("entry %+v; want cluster prod marked damaged", e)
				}
			}
			if _, err := remove(path, "prod"); err == nil || !strings.Contains(err.Error(), "by hand") {
				t.Errorf("Remove error = %v, want a request to repair the file by hand", err)
			}
			if err := install(path, testEntry()); err == nil || !strings.Contains(err.Error(), "by hand") {
				t.Errorf("Install error = %v, want a request to repair the file by hand", err)
			}
			if got := readFile(t, path); got != content {
				t.Errorf("file changed to\n%s\nwant it untouched", got)
			}
		})
	}
}

func TestDefaultPathIsTheUsersSSHConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	got, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".ssh", "config"); got != want {
		t.Errorf("DefaultPath() = %q, want %q", got, want)
	}
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s has mode %o, want %o", path, got, want)
	}
}

func TestEntriesForContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if entries, err := entriesForContext(path, "prod"); err != nil || len(entries) != 0 {
		t.Fatalf("missing file: entries = %v, %v; want none, nil", entries, err)
	}
	arn := "arn:aws:eks:us-west-2:123456789012:cluster/dev"
	for _, e := range []Entry{
		testEntry(),
		{Cluster: "dev", Context: arn, Kubeconfig: "/My Files/kubeconfig", Command: "/My Tools/kubectl-workspace", User: DefaultUser},
		{Cluster: "dev-two", Context: arn, Command: "/opt/kw", User: DefaultUser},
	} {
		if err := install(path, e); err != nil {
			t.Fatal(err)
		}
	}
	for _, tt := range []struct {
		context string
		want    []InstalledEntry
	}{
		{"prod", []InstalledEntry{{Cluster: "prod", Context: "prod"}}},
		// Install puts a newer entry before the older one.
		{arn, []InstalledEntry{{Cluster: "dev-two", Context: arn}, {Cluster: "dev", Context: arn, Kubeconfig: "/My Files/kubeconfig"}}},
		{"staging", nil},
	} {
		entries, err := entriesForContext(path, tt.context)
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(entries) != fmt.Sprint(tt.want) {
			t.Errorf("entries for %q = %v, want %v", tt.context, entries, tt.want)
		}
	}
}

// A damaged entry comes back marked, with the flags read as far as the next marker, so
// a lookup can leave it out and a command about to act on the file can report it.
func TestEntriesForContextMarksADamagedEntry(t *testing.T) {
	path := writeFile(t, startMarker("broken")+"\n  ProxyCommand kw connect --stdio --context prod %h\n")
	if err := install(path, testEntry()); err != nil {
		t.Fatal(err)
	}
	entries, err := entriesForContext(path, "prod")
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries = %v, %v; want the damaged and the intact entry", entries, err)
	}
	byCluster := map[string]InstalledEntry{}
	for _, e := range entries {
		byCluster[e.Cluster] = e
	}
	if e := byCluster["broken"]; e.Damaged == nil || !strings.Contains(e.Damaged.Error(), "no end marker") {
		t.Errorf("broken entry = %+v; want it marked damaged for its missing end marker", e)
	}
	if e := byCluster["prod"]; e.Context != "prod" || e.Damaged != nil {
		t.Errorf("prod entry = %+v; want the intact entry", e)
	}
}

// Remove undoes Install: the file ends up as it was, except that one blank line may
// separate what the entry had split, since Install padded both sides of the entry.
func TestRemoveUndoesInstall(t *testing.T) {
	tests := []struct {
		name, before, after string
	}{
		{name: "empty file", before: "", after: ""},
		{name: "only a block", before: "Host github.com\n  User git\n", after: "Host github.com\n  User git\n"},
		{name: "options then a block", before: "AddKeysToAgent yes\nHost github.com\n", after: "AddKeysToAgent yes\n\nHost github.com\n"},
		{name: "options only", before: "AddKeysToAgent yes\n", after: "AddKeysToAgent yes\n"},
		{name: "crlf", before: "Host a\r\n  User x\r\n", after: "Host a\r\n  User x\r\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeFile(t, tt.before)
			if err := install(path, testEntry()); err != nil {
				t.Fatal(err)
			}
			removed, err := remove(path, "prod")
			if err != nil || !removed {
				t.Fatalf("Remove = %v, %v; want true, nil", removed, err)
			}
			if got := readFile(t, path); got != tt.after {
				t.Errorf("after Remove:\n%q\nwant\n%q", got, tt.after)
			}
		})
	}
}

func TestRemoveLeavesOtherClustersAndReportsAbsence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	staging := testEntry()
	staging.Cluster, staging.Context = "staging", "staging"
	for _, e := range []Entry{testEntry(), staging} {
		if err := install(path, e); err != nil {
			t.Fatal(err)
		}
	}
	if removed, err := remove(path, "prod"); err != nil || !removed {
		t.Fatalf("Remove prod = %v, %v", removed, err)
	}
	if got := readFile(t, path); got != staging.Render() {
		t.Errorf("after removing prod:\n%s\nwant the staging entry alone", got)
	}
	if removed, err := remove(path, "prod"); err != nil || removed {
		t.Errorf("second Remove = %v, %v; want false, nil", removed, err)
	}
	if removed, err := remove(filepath.Join(t.TempDir(), "none"), "staging"); err != nil || removed {
		t.Errorf("Remove on a missing file = %v, %v; want false, nil", removed, err)
	}
}

func TestRemoveRejectsAPartialEntry(t *testing.T) {
	path := writeFile(t, endMarker("prod")+"\n")
	if _, err := remove(path, "prod"); err == nil || !strings.Contains(err.Error(), "remove it by hand") {
		t.Errorf("error = %v, want a request to repair the file by hand", err)
	}
}

func TestRemoveWritesThroughASymlinkedFile(t *testing.T) {
	target := writeFile(t, "Host github.com\n  User git\n")
	link := filepath.Join(t.TempDir(), "config")
	testutil.Symlink(t, target, link)
	if err := install(link, testEntry()); err != nil {
		t.Fatal(err)
	}

	if removed, err := remove(link, "prod"); err != nil || !removed {
		t.Fatalf("Remove through the link = %v, %v; want true, nil", removed, err)
	}

	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("the symlink was replaced by a regular file")
	}
	if got := readFile(t, target); got != "Host github.com\n  User git\n" {
		t.Errorf("target file =\n%s\nwant the content before Install", got)
	}
}

// install, remove, entryFor and entriesForContext run one Load of the file around the
// call, as a command does.
func install(path string, e Entry) error {
	f, err := Load(path, false, "")
	if err != nil {
		return err
	}
	if err := f.Install(e); err != nil {
		return err
	}
	return f.Save()
}

func remove(path, cluster string) (bool, error) {
	f, err := Load(path, false, "")
	if err != nil {
		return false, err
	}
	removed, err := f.Remove(cluster)
	if err != nil || !removed {
		return removed, err
	}
	return true, f.Save()
}

func entryFor(path, cluster string) (InstalledEntry, bool, error) {
	f, err := Load(path, false, "")
	if err != nil {
		return InstalledEntry{}, false, err
	}
	return f.EntryFor(cluster)
}

func entriesForContext(path, contextName string) ([]InstalledEntry, error) {
	f, err := Load(path, false, "")
	if err != nil {
		return nil, err
	}
	return f.EntriesForContext(contextName), nil
}

// An entry routes the context of one kubeconfig file, whatever the file is called; an
// entry whose ProxyCommand names no file is read as naming the default one.
func TestRoutes(t *testing.T) {
	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(kubeconfig, []byte("apiVersion: v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	testutil.Symlink(t, kubeconfig, link)
	other := filepath.Join(t.TempDir(), "other")
	if err := os.WriteFile(other, []byte("apiVersion: v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	withFile, withoutFile := testEntry(), testEntry()
	withFile.Kubeconfig = kubeconfig
	withoutFile.Cluster = "no-file"
	path := writeFile(t, startMarker("edited")+"\nHost *.edited.jk8s\n  ProxyCommand kw connect --stdio --kubeconfig /k %h\n"+
		endMarker("edited")+"\n"+
		startMarker("relative")+"\nHost *.relative.jk8s\n  ProxyCommand kw connect --stdio --kubeconfig kubeconfig --context prod %h\n"+
		endMarker("relative")+"\n")
	for _, e := range []Entry{withFile, withoutFile} {
		if err := install(path, e); err != nil {
			t.Fatal(err)
		}
	}
	f, err := Load(path, false, other)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Dir(kubeconfig))
	for _, tt := range []struct {
		name, cluster, context, kubeconfig string
		want                               bool
	}{
		{name: "the file it names", cluster: "prod", context: "prod", kubeconfig: kubeconfig, want: true},
		{name: "the file it names, through a link", cluster: "prod", context: "prod", kubeconfig: link, want: true},
		{name: "another file", cluster: "prod", context: "prod", kubeconfig: other, want: false},
		{name: "another context", cluster: "prod", context: "staging", kubeconfig: kubeconfig, want: false},
		{name: "no file named, the default", cluster: "no-file", context: "prod", kubeconfig: other, want: true},
		{name: "no file named, not another", cluster: "no-file", context: "prod", kubeconfig: kubeconfig, want: false},
		{name: "a relative file, from its own directory", cluster: "relative", context: "prod", kubeconfig: kubeconfig, want: false},
	} {
		installed, ok, err := f.EntryFor(tt.cluster)
		if err != nil || !ok {
			t.Fatalf("EntryFor(%s) = %v, %v", tt.cluster, ok, err)
		}
		if got := installed.Routes(tt.context, tt.kubeconfig); got != tt.want {
			t.Errorf("%s: Routes(%q, %s) = %v, want %v", tt.name, tt.context, tt.kubeconfig, got, tt.want)
		}
	}
	e, ok, err := f.EntryFor("edited")
	if err != nil || !ok || e.Routes("prod", other) || e.Kubeconfig != "" {
		t.Errorf("an entry without a readable context = %+v, %v, %v; want it to route nothing and name no file", e, ok, err)
	}
}

// The lookups answer from the one read and the edits reach the file only through Save.
func TestEditsReachTheFileThroughSave(t *testing.T) {
	path := writeFile(t, "Host github.com\n  User git\n")
	f, err := Load(path, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Install(testEntry()); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := f.EntryFor("prod"); err != nil || !ok {
		t.Errorf("EntryFor after Install = %v, %v; want the entry found in memory", ok, err)
	}
	if got := readFile(t, path); got != "Host github.com\n  User git\n" {
		t.Errorf("file before Save =\n%s\nwant it untouched", got)
	}
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != testEntry().Render()+"\nHost github.com\n  User git\n" {
		t.Errorf("file after Save =\n%s\nwant the entry before the block", got)
	}
}
