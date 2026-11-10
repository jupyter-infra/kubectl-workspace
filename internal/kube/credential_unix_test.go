//go:build !windows

/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package kube

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestResolveCredentialTerminatesThePluginsChildrenAtTheDeadline(t *testing.T) {
	target := fakeTarget("sleep-with-child", false)
	var stderr bytes.Buffer

	err := ResolveCredential(context.Background(), target, CredentialOptions{Stderr: &stderr, Timeout: time.Second})
	var signIn *SignInRequiredError
	if !errors.As(err, &signIn) {
		t.Fatalf("error = %v, want *SignInRequiredError", err)
	}

	var childPID int
	for _, line := range strings.Split(stderr.String(), "\n") {
		if raw, ok := strings.CutPrefix(line, "child-pid:"); ok {
			childPID, _ = strconv.Atoi(raw)
		}
	}
	if childPID == 0 {
		t.Fatalf("the plugin did not report its child; stderr:\n%s", stderr.String())
	}
	// A zombie still answers signal 0 until reaped; the child is reaped by init
	// once the plugin is gone, so allow a moment.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(childPID, 0); err != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = syscall.Kill(childPID, syscall.SIGKILL)
	t.Errorf("the plugin's child %d survived the deadline", childPID)
}
