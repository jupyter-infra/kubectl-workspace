/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package fsutil

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jupyter-infra/kubectl-workspace/internal/testutil"
)

func TestAbsResolvesAgainstTheWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	got, err := Abs("kubeconfig", "kc")
	if err != nil || got != filepath.Join(dir, "kc") {
		t.Errorf("Abs = %q, %v; want the path under the working directory", got, err)
	}
}

func TestSameFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	testutil.Symlink(t, file, link)
	other := filepath.Join(filepath.Dir(file), "other")
	if err := os.WriteFile(other, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	for _, tt := range []struct {
		name, a, b string
		want       bool
	}{
		{name: "one spelling", a: file, b: file, want: true},
		{name: "a link and its target", a: link, b: file, want: true},
		{name: "two files", a: file, b: other, want: false},
		{name: "a missing file under one spelling", a: missing, b: missing, want: true},
		{name: "a missing file and an existing one", a: missing, b: file, want: false},
	} {
		if got := SameFile(tt.a, tt.b); got != tt.want {
			t.Errorf("%s: SameFile = %v, want %v", tt.name, got, tt.want)
		}
	}
}
