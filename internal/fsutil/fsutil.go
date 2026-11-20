/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

// Package fsutil holds the path helpers the other packages share.
package fsutil

import (
	"fmt"
	"os"
	"path/filepath"
)

// Abs makes a path absolute, naming what it is in the error.
func Abs(what, path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("failed to resolve the %s path %q: %w", what, path, err)
	}
	return abs, nil
}

// SameFile reports whether both paths name one file: the same spelling, or two spellings
// of one file, as a dotfiles symlink or a case difference on a case-insensitive filesystem
// gives.
func SameFile(a, b string) bool {
	if a == b {
		return true
	}
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
