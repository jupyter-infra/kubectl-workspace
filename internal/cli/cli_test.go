/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package cli

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jupyter-infra/kubectl-workspace/internal/testutil"
)

const (
	alias       = "ws1.team-a.dev.jk8s"
	sshPath     = "/workspaces/team-a/ws1/ssh-ws"
	wantAPIPath = "/apis/connection.workspace.jupyter.org/v1alpha1/namespaces/team-a/workspaceconnections"
)

// run executes the command tree with the given arguments and no stdin.
func run(t *testing.T, args ...string) (stdout string, err error) {
	t.Helper()
	stdout, _, err = runWithStderr(t, args...)
	return stdout, err
}

func runWithStderr(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	root := newRootCommand(Streams{In: strings.NewReader(""), Out: &out, Err: &errOut}, "test")
	root.SetArgs(args)
	err = root.ExecuteContext(context.Background())
	return out.String(), errOut.String(), err
}

// kwCommand is an absolute --command path that ssh-config keeps as it is; on
// Windows it carries the current drive, which is what filepath.Abs adds there.
var kwCommand = func() string {
	abs, err := filepath.Abs(filepath.FromSlash("/opt/kw"))
	if err != nil {
		panic(err)
	}
	return abs
}()

func staticKubeconfig(t *testing.T) string {
	t.Helper()
	return testutil.Kubeconfig{Server: "https://127.0.0.1:1", Namespace: "team-a"}.Write(t)
}

// connectRun is a connect command running with pipes for ssh's stdin and stdout.
type connectRun struct {
	stdin  *io.PipeWriter
	stdout *io.PipeReader
	stderr *bytes.Buffer
	done   chan error
	cancel context.CancelFunc
}

// startConnect runs the plugin with the full argument list, as ssh would.
func startConnect(t *testing.T, args ...string) *connectRun {
	t.Helper()
	inReader, inWriter := io.Pipe()
	outReader, outWriter := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r := &connectRun{stdin: inWriter, stdout: outReader, stderr: &bytes.Buffer{}, done: make(chan error, 1), cancel: cancel}
	root := newRootCommand(Streams{In: inReader, Out: outWriter, Err: r.stderr}, "test")
	root.SetArgs(args)
	go func() {
		err := root.ExecuteContext(ctx)
		_ = outWriter.Close()
		r.done <- err
	}()
	return r
}

// wait returns connect's result without touching stdin.
func (r *connectRun) wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-r.done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("connect did not return")
		return nil
	}
}

// echo sends payload through stdin and returns what came back on stdout.
func (r *connectRun) echo(t *testing.T, payload string) string {
	t.Helper()
	go func() { _, _ = io.WriteString(r.stdin, payload) }()
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(r.stdout, got); err != nil {
		t.Fatalf("reading the echoed bytes: %v (stderr: %s)", err, r.stderr.String())
	}
	return string(got)
}

// finish closes stdin, as ssh exiting does, and returns connect's result.
func (r *connectRun) finish(t *testing.T) error {
	t.Helper()
	_ = r.stdin.Close()
	return r.wait(t)
}
