/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package cli

import (
	"fmt"
	"os"
	"path/filepath"
)

// absPath makes a path absolute, naming what it is in the error.
func absPath(what, path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("failed to resolve the %s path %q: %w", what, path, err)
	}
	return abs, nil
}

// sameFile reports whether both paths name the same file.
func sameFile(a, b string) bool {
	infoA, err := os.Stat(a)
	if err != nil {
		return false
	}
	infoB, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(infoA, infoB)
}
