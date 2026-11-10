/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

// Package main provides the entry point for kubectl-workspace, the kubectl plugin
// that connects a local machine to a workspace over SSH.
package main

import (
	"os"

	// Registers client-go's auth providers, so a kubeconfig whose user carries a
	// legacy auth-provider block instead of an exec plugin still authenticates.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"github.com/jupyter-infra/kubectl-workspace/internal/cli"
)

// version is set at build time with -ldflags "-X main.version=<tag>".
var version = "dev"

func main() {
	streams := cli.Streams{In: os.Stdin, Out: os.Stdout, Err: os.Stderr}
	os.Exit(cli.Main(os.Args[1:], streams, version))
}
