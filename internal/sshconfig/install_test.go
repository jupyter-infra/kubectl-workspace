/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package sshconfig

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallCreatesThePrivateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".ssh", "config")
	if err := Install(path, testEntry()); err != nil {
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
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	target := writeFile(t, "Host github.com\n  User git\n")
	link := filepath.Join(t.TempDir(), "config")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	if err := Install(link, testEntry()); err != nil {
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
	if installed, err := Installed(link, "prod"); err != nil || !installed {
		t.Errorf("Installed through the link = %v, %v", installed, err)
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
			if err := Install(path, testEntry()); err != nil {
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
	if err := os.Symlink(filepath.Join(dir, "dotfiles", "ssh_config"), link); err != nil {
		t.Fatal(err)
	}

	err := Install(link, testEntry())
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
		if err := Install(path, e); err != nil {
			t.Fatal(err)
		}
	}

	lines := splitLines(readFile(t, path))
	for _, e := range []Entry{staging, prod} {
		start, end, err := findEntry(lines, e.Cluster)
		if err != nil || start < 0 {
			t.Fatalf("entry for %s: start %d, %v", e.Cluster, start, err)
		}
		var hosts []string
		for _, line := range lines[start : end+1] {
			if blockKeyword.MatchString(line) {
				hosts = append(hosts, strings.TrimSpace(line))
			}
		}
		if len(hosts) != 1 || hosts[0] != "Host "+e.HostPattern() {
			t.Errorf("the markers for %s enclose %q, want only its own Host line", e.Cluster, hosts)
		}
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
	if err := Install(path, testEntry()); err != nil {
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

	err := Install(path, testEntry())
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

	if err := Install(path, testEntry()); err != nil {
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

func TestInstallIsIdempotent(t *testing.T) {
	path := writeFile(t, "Host github.com\n  User git\n")
	for i := 0; i < 3; i++ {
		if err := Install(path, testEntry()); err != nil {
			t.Fatal(err)
		}
	}
	want := testEntry().Render() + "\nHost github.com\n  User git\n"
	if got := readFile(t, path); got != want {
		t.Errorf("after three installs file =\n%s\nwant\n%s", got, want)
	}
}

func TestInstallKeepsCRLF(t *testing.T) {
	path := writeFile(t, "Host github.com\r\n  User git\r\n")
	if err := Install(path, testEntry()); err != nil {
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
	err := Install(path, testEntry())
	if err == nil || !strings.Contains(err.Error(), "remove it by hand") {
		t.Fatalf("Install error = %v, want a request to repair the file by hand", err)
	}
	if got := readFile(t, path); !strings.HasPrefix(got, startMarker("prod")) {
		t.Errorf("a failed install must leave the file untouched, got\n%s", got)
	}
}

func TestInstalled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	installed, err := Installed(path, "prod")
	if err != nil || installed {
		t.Fatalf("Installed on a missing file = %v, %v; want false, nil", installed, err)
	}
	if err := Install(path, testEntry()); err != nil {
		t.Fatal(err)
	}
	installed, err = Installed(path, "prod")
	if err != nil || !installed {
		t.Errorf("Installed after Install = %v, %v; want true, nil", installed, err)
	}
	installed, err = Installed(path, "staging")
	if err != nil || installed {
		t.Errorf("Installed for another cluster = %v, %v; want false, nil", installed, err)
	}
}

func TestInstalledRejectsAPartialEntry(t *testing.T) {
	path := writeFile(t, endMarker("prod")+"\n")
	if _, err := Installed(path, "prod"); err == nil || !strings.Contains(err.Error(), "remove it by hand") {
		t.Errorf("Installed error = %v, want a request to repair the file by hand", err)
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
