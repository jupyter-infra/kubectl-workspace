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

	"github.com/jupyter-infra/kubectl-workspace/internal/fsutil"
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

// File is an SSH configuration file read once. The lookups answer from what was read,
// Install and Remove edit it in memory, and Save writes it back in one replace, so a
// command acts on the file it checked.
type File struct {
	// path is the file as named; target is the file OpenSSH reads, through a symlink.
	path, target      string
	lines             []string
	newline           string
	mode              os.FileMode
	windows           bool
	defaultKubeconfig string
}

// Load reads the configuration file at path; a missing file loads empty. windows selects
// the quoting the entries were written with. defaultKubeconfig is the file an entry whose
// ProxyCommand names no --kubeconfig is read as naming: the default location of the process
// ssh runs in.
func Load(path string, windows bool, defaultKubeconfig string) (*File, error) {
	target, err := resolveTarget(path)
	if err != nil {
		return nil, err
	}
	content, mode, err := read(target)
	if err != nil {
		return nil, err
	}
	return &File{
		path: path, target: target, lines: splitLines(content), newline: lineEnding(content), mode: mode,
		windows: windows, defaultKubeconfig: defaultKubeconfig,
	}, nil
}

// Path returns the file as it was named to Load.
func (f *File) Path() string {
	return f.path
}

// Install puts the entry in place of the one installed for its cluster, or where
// firstBlock says. Save writes the change.
func (f *File) Install(e Entry) error {
	if err := e.Validate(); err != nil {
		return err
	}
	lines, err := merge(f.lines, e)
	if err != nil {
		return fmt.Errorf("%s: %w", f.path, err)
	}
	f.lines = lines
	return nil
}

// Remove deletes the entry for cluster and reports whether there was one. Of the blank
// lines Install put around the entry, one goes with it, so the file reads as it did before
// Install. Save writes the change.
func (f *File) Remove(cluster string) (bool, error) {
	start, end, err := findEntry(f.lines, cluster)
	if err != nil {
		return false, fmt.Errorf("%s: %w", f.path, err)
	}
	if start < 0 {
		return false, nil
	}
	out := make([]string, 0, len(f.lines)-(end-start+1))
	out = append(out, f.lines[:start]...)
	out = append(out, f.lines[end+1:]...)
	switch {
	case start < len(out) && out[start] == "" && (start == 0 || out[start-1] == ""):
		out = append(out[:start], out[start+1:]...)
	case start == len(out) && start > 0 && out[start-1] == "":
		out = out[:start-1]
	}
	f.lines = out
	return true, nil
}

// Save writes the lines back through a rename, creating the file when needed.
func (f *File) Save() error {
	if len(f.lines) == 0 {
		return write(f.target, "", f.mode)
	}
	return write(f.target, strings.Join(f.lines, f.newline)+f.newline, f.mode)
}

// EntryFor returns the entry installed for cluster, and whether there is one.
func (f *File) EntryFor(cluster string) (InstalledEntry, bool, error) {
	start, end, err := findEntry(f.lines, cluster)
	if err != nil {
		return InstalledEntry{}, false, fmt.Errorf("%s: %w", f.path, err)
	}
	if start < 0 {
		return InstalledEntry{}, false, nil
	}
	return f.readEntry(cluster, f.lines[start:end+1]), true, nil
}

// InstalledEntry is what EntryFor and EntriesForContext read back from an entry: its
// cluster name, and the context and kubeconfig file its ProxyCommand runs connect with.
type InstalledEntry struct {
	Cluster string
	// Context is empty when the ProxyCommand names none, as after a hand edit replaced it.
	Context string
	// Kubeconfig is the file the ProxyCommand names, or the default file of Load when it
	// names none; empty with Context.
	Kubeconfig string
	// Damaged is the marker error of an entry whose markers do not pair up; its flags
	// were read as far as the next marker. Nil for an intact entry.
	Damaged error
}

// Routes reports whether the entry's ProxyCommand connects to contextName in the
// kubeconfig file: the same context name, and one file under both spellings. An entry
// whose ProxyCommand the plugin cannot read routes nothing, and so does one naming a
// relative file, which connect would resolve against OpenSSH's working directory.
func (e InstalledEntry) Routes(contextName, kubeconfig string) bool {
	return e.Context != "" && e.Context == contextName && filepath.IsAbs(e.Kubeconfig) &&
		fsutil.SameFile(e.Kubeconfig, kubeconfig)
}

// EntriesForContext returns the entries whose ProxyCommand runs connect with
// contextName, in file order. An entry with broken markers comes back with Damaged set,
// so a lookup can skip it and a command about to act on the file can report it.
func (f *File) EntriesForContext(contextName string) []InstalledEntry {
	lines := f.lines
	var entries []InstalledEntry
	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		var cluster string
		var block []string
		var damaged error
		switch {
		case strings.HasPrefix(trimmed, startMarkerPrefix):
			cluster = strings.TrimPrefix(trimmed, startMarkerPrefix)
			start, end, err := findEntry(lines, cluster)
			if err != nil {
				start, end, damaged = i, nextMarker(lines, i+1)-1, err
				// With the entry's own end marker as the nearest marker above, the lines between
				// the two are the entry's.
				if p := prevMarker(lines, i-1); p >= 0 && strings.TrimSpace(lines[p]) == endMarker(cluster) {
					start = p + 1
				}
			}
			block = lines[start : end+1]
			i = end
		case strings.HasPrefix(trimmed, endMarkerPrefix):
			// An end marker whose cluster also has a start marker is read from that start
			// marker; one without is the remains of an entry whose start was lost.
			cluster = strings.TrimPrefix(trimmed, endMarkerPrefix)
			if hasMarker(lines, startMarker(cluster)) {
				continue
			}
			_, _, damaged = findEntry(lines, cluster)
			block = lines[prevMarker(lines, i-1)+1 : i+1]
		default:
			continue
		}
		entry := f.readEntry(cluster, block)
		entry.Damaged = damaged
		if entry.Context == contextName {
			entries = append(entries, entry)
		}
	}
	return entries
}

// hasMarker reports whether one of the lines is the marker.
func hasMarker(lines []string, marker string) bool {
	for _, line := range lines {
		if strings.TrimSpace(line) == marker {
			return true
		}
	}
	return false
}

// isMarker reports whether the line is a start or an end marker of any cluster.
func isMarker(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, startMarkerPrefix) || strings.HasPrefix(trimmed, endMarkerPrefix)
}

// nextMarker returns the index of the first marker line at or after from, or the
// number of lines when there is none.
func nextMarker(lines []string, from int) int {
	for i := from; i < len(lines); i++ {
		if isMarker(lines[i]) {
			return i
		}
	}
	return len(lines)
}

// prevMarker returns the index of the last marker line at or before from, or -1 when
// there is none.
func prevMarker(lines []string, from int) int {
	for i := from; i >= 0; i-- {
		if isMarker(lines[i]) {
			return i
		}
	}
	return -1
}

// proxyCommandKeyword matches the ProxyCommand keyword as ssh_config reads it: in any
// letter case, followed by whitespace or `=`.
var proxyCommandKeyword = regexp.MustCompile(`(?i)^proxycommand(\s+|\s*=\s*)`)

// readEntry reads the connect flags out of an entry's ProxyCommand line, however the
// keyword is spelled. OpenSSH keeps the first value it meets for an option, so the first
// ProxyCommand line is the entry's.
func (f *File) readEntry(cluster string, block []string) InstalledEntry {
	entry := InstalledEntry{Cluster: cluster}
	for _, line := range block {
		trimmed := strings.TrimSpace(line)
		keyword := proxyCommandKeyword.FindStringIndex(trimmed)
		if keyword == nil {
			continue
		}
		args := SplitArgs(trimmed[keyword[1]:], f.windows)
		for i := 0; i < len(args); i++ {
			entry.Context = flagValue(args, i, "--context", entry.Context)
			entry.Kubeconfig = flagValue(args, i, "--kubeconfig", entry.Kubeconfig)
		}
		break
	}
	switch {
	case entry.Context == "":
		entry.Kubeconfig = ""
	case entry.Kubeconfig == "":
		entry.Kubeconfig = f.defaultKubeconfig
	}
	return entry
}

// flagValue returns the value of flag at position i of args, in the "--flag value" form
// the plugin writes or the "--flag=value" form a hand edit may use, else current.
func flagValue(args []string, i int, flag, current string) string {
	if args[i] == flag && i+1 < len(args) {
		return args[i+1]
	}
	if value, ok := strings.CutPrefix(args[i], flag+"="); ok {
		return value
	}
	return current
}

// lineEnding returns the line ending the content uses, so a rewrite keeps it.
func lineEnding(content string) string {
	if strings.Contains(content, "\r\n") {
		return "\r\n"
	}
	return "\n"
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

// merge returns the lines with the entry in place.
func merge(lines []string, e Entry) ([]string, error) {
	entry := splitLines(e.Render())
	start, end, err := findEntry(lines, e.Cluster)
	if err != nil {
		return nil, err
	}

	var out []string
	if start >= 0 {
		out = append(out, lines[:start]...)
		out = append(out, entry...)
		out = append(out, lines[end+1:]...)
		return out, nil
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
	return out, nil
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

// findEntry returns the line range of the entry for cluster, or -1 when absent. A
// start marker without its end, an end marker without its start, a second marker of
// either kind, or another entry's marker inside the block is an error: the file needs a
// hand before the tool touches it.
func findEntry(lines []string, cluster string) (start, end int, err error) {
	start, end = -1, -1
	for i, line := range lines {
		switch strings.TrimSpace(line) {
		case startMarker(cluster):
			if start >= 0 {
				return -1, -1, fmt.Errorf("two start markers for cluster %q; remove the entries by hand", cluster)
			}
			start = i
		case endMarker(cluster):
			switch {
			case start < 0:
				return -1, -1, fmt.Errorf("an end marker for cluster %q precedes its start; remove it by hand", cluster)
			case end >= 0:
				return -1, -1, fmt.Errorf("two end markers for cluster %q; remove the entries by hand", cluster)
			}
			end = i
		default:
			if start >= 0 && end < 0 && isMarker(line) {
				return -1, -1, fmt.Errorf("the entry for cluster %q holds another entry's markers; repair the file by hand",
					cluster)
			}
		}
	}
	if start >= 0 && end < 0 {
		return -1, -1, fmt.Errorf("the entry for cluster %q has no end marker; remove it by hand", cluster)
	}
	return start, end, nil
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
