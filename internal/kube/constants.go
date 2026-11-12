/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package kube

import "time"

const (
	// connectionAPIPath is where the operator's extension API serves connections.
	connectionAPIPath    = "apis/connection.workspace.jupyter.org/v1alpha1"
	connectionAPIVersion = "connection.workspace.jupyter.org/v1alpha1"
	connectionKind       = "WorkspaceConnection"

	// connectionTypeSSH names the connection whose URL is the sidecar's WebSocket route.
	connectionTypeSSH = "ssh-over-websocket"

	// maxResponseBodyBytes bounds how much of the operator's response is read.
	maxResponseBodyBytes = 64 * 1024
	// maxErrorDetailBytes bounds how much of a body that is not a Status object, an
	// ingress error page for example, ends up in the error line.
	maxErrorDetailBytes = 512
)

// execInfoEnv is the variable through which client-go describes the request to
// an exec credential plugin.
const execInfoEnv = "KUBERNETES_EXEC_INFO"

// killDelay is how long a plugin gets to exit after SIGTERM before SIGKILL.
const killDelay = 2 * time.Second

// The names kubelogin runs by: kubectl's oidc-login plugin, the krew binary, its own binary.
const (
	kubectlCommand    = "kubectl"
	kubeloginPlugin   = "oidc-login"
	kubeloginKrewName = "kubectl-oidc_login"
	kubeloginName     = "kubelogin"
)
