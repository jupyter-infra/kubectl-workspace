/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package sshconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// newFileMode keeps a new configuration file private, as OpenSSH requires.
const newFileMode = 0o600

// DefaultPath returns the user's SSH configuration file, ~/.ssh/config.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to find the home directory: %w", err)
	}
	return filepath.Join(home, ".ssh", "config"), nil
}

// Install writes the entry into the SSH configuration file at path, creating the
// file when needed. An existing entry for the same cluster is replaced in place; a
// new one goes where firstBlock says.
func Install(path string, e Entry) error {
	if err := e.Validate(); err != nil {
		return err
	}
	path, err := resolveTarget(path)
	if err != nil {
		return err
	}
	content, mode, err := read(path)
	if err != nil {
		return err
	}
	updated, err := merge(content, e)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return write(path, updated, mode)
}

// Installed reports whether the file at path holds an entry for the cluster.
func Installed(path, cluster string) (bool, error) {
	path, err := resolveTarget(path)
	if err != nil {
		return false, err
	}
	content, _, err := read(path)
	if err != nil {
		return false, err
	}
	start, _, err := findEntry(splitLines(content), cluster)
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	return start >= 0, nil
}

// resolveTarget follows a symlinked configuration file, which dotfiles setups
// have, so the rename in write replaces the file OpenSSH reads and not the link.
func resolveTarget(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if errors.Is(err, os.ErrNotExist) {
		// A link whose target is missing resolves the same way as no file; renaming
		// over it would replace the link with a regular file.
		if info, lerr := os.Lstat(path); lerr == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%s is a link to a missing file; create the target or remove the link", path)
		}
		return path, nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to resolve %s: %w", path, err)
	}
	return resolved, nil
}

func read(path string) (string, os.FileMode, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", newFileMode, nil
	}
	if err != nil {
		return "", 0, fmt.Errorf("failed to read %s: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", 0, fmt.Errorf("failed to stat %s: %w", path, err)
	}
	return string(data), info.Mode().Perm(), nil
}

// write replaces the file through a rename so a crash leaves either version whole.
func write(path, content string, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create a temporary file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("failed to write %s: %w", tmpPath, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("failed to set the mode of %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to write %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("failed to replace %s: %w", path, err)
	}
	return nil
}

// merge returns the configuration text with the entry in place.
func merge(content string, e Entry) (string, error) {
	newline := "\n"
	if strings.Contains(content, "\r\n") {
		newline = "\r\n"
	}
	lines := splitLines(content)
	entry := splitLines(e.Render())

	start, end, err := findEntry(lines, e.Cluster)
	if err != nil {
		return "", err
	}

	var out []string
	if start >= 0 {
		out = append(out, lines[:start]...)
		out = append(out, entry...)
		out = append(out, lines[end+1:]...)
		return strings.Join(out, newline) + newline, nil
	}
	at := firstBlock(lines)
	if at < 0 {
		at = len(lines)
	}
	before, after := lines[:at], lines[at:]
	out = append(out, before...)
	if len(before) > 0 && before[len(before)-1] != "" {
		out = append(out, "")
	}
	out = append(out, entry...)
	if len(after) > 0 {
		out = append(out, "")
	}
	out = append(out, after...)
	return strings.Join(out, newline) + newline, nil
}

// splitLines splits on either line ending and drops the empty element a trailing
// newline produces, so a rejoin does not grow the file by one line per run.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}

// findEntry returns the line range of the entry for cluster, or -1 when absent.
func findEntry(lines []string, cluster string) (start, end int, err error) {
	start = -1
	for i, line := range lines {
		switch strings.TrimSpace(line) {
		case startMarker(cluster):
			if start >= 0 {
				return -1, -1, fmt.Errorf("two start markers for cluster %q; remove the entries by hand", cluster)
			}
			start = i
		case endMarker(cluster):
			if start < 0 {
				return -1, -1, fmt.Errorf("an end marker for cluster %q precedes its start; remove it by hand", cluster)
			}
			return start, i, nil
		}
	}
	if start >= 0 {
		return -1, -1, fmt.Errorf("the entry for cluster %q has no end marker; remove it by hand", cluster)
	}
	return -1, -1, nil
}

// blockKeyword matches the first line of a Host or Match block. ssh_config
// accepts `Keyword value` and `Keyword=value`, in any letter case.
var blockKeyword = regexp.MustCompile(`(?i)^\s*(host|match)(\s|=)`)

// firstBlock returns the index where a new entry goes: the first Host or Match line
// with the comment lines directly above it, or -1 when the file has no block. The
// placement rule is in AGENT.md under Invariants; another cluster's entry counts
// from its start marker, so a new entry lands before it, not inside its markers.
func firstBlock(lines []string) int {
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), startMarkerPrefix) {
			return i
		}
		if blockKeyword.MatchString(line) {
			for i > 0 && strings.HasPrefix(strings.TrimSpace(lines[i-1]), "#") {
				i--
			}
			return i
		}
	}
	return -1
}
