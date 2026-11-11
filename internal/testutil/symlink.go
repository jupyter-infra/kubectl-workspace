/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package testutil

import (
	"os"
	"runtime"
	"testing"
)

// Symlink creates link pointing at target. On Windows, where creating a symbolic
// link needs a privilege the user may lack, a refused link skips the test.
func Symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symbolic links are not permitted here: %v", err)
		}
		t.Fatal(err)
	}
}
