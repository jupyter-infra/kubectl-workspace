/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package cli

import "time"

// programName prefixes every diagnostic, so a line in the editor's SSH log can be
// traced back to this program.
const programName = "kubectl-workspace"

const goosWindows = "windows"

// allowPlaintextFlag is the opt-in connect takes and ssh-config writes into the entry.
const allowPlaintextFlag = "insecure-allow-plaintext"

// writeFlag and removeFlag are ssh-config's install and removal flags, registered and
// spelled in the hints from here.
const (
	writeFlag  = "write"
	removeFlag = "remove"
)

// Help texts of the flags that connect, ssh-config and open share.
const (
	caFileHelp = "PEM bundle with the certificate authority of the cluster's ingress, " +
		"trusted instead of the system roots"
	credentialTimeoutHelp = "how long connect waits for the kubeconfig's credential plugin"
	allowPlaintextHelp    = "accept a connection URL without TLS from the operator, for a local cluster; " +
		"the token then travels in plaintext"
	clusterNameHelp = "name of the cluster in the alias (default from the installed entry, else the context name " +
		"or the cluster name in an EKS ARN)"
	sshConfigFileHelp = "SSH configuration file (default ~/.ssh/config)"
)

// defaultCredentialTimeout leaves room inside Remote-SSH's 15-second connection
// timeout for the connection request and the WebSocket handshake.
const defaultCredentialTimeout = 5 * time.Second

// slowPluginFactor scales the active deadline into the one the hint suggests.
const slowPluginFactor = 3

// defaultRemotePath is the workspace user's home, where the persistent volume is
// mounted.
const defaultRemotePath = "/home/jovyan"

// The --editor values, each also the editor's URL scheme.
const (
	editorVSCode  = "vscode"
	editorCursor  = "cursor"
	defaultEditor = editorVSCode
)
