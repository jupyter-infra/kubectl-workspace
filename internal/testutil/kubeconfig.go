/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package testutil

import (
	"path/filepath"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

const (
	// Context is the current context in every kubeconfig Write renders.
	Context = "dev"
	// ARNContext is a second context named the way `aws eks update-kubeconfig`
	// names them, which no alias label can carry.
	ARNContext = "arn:aws:eks:us-west-2:123456789012:cluster/dev"
	// Token is the static credential unless an exec plugin is configured.
	Token = "kube-token"
)

// ExecPlugin is a kubeconfig exec credential plugin.
type ExecPlugin struct {
	Command     string
	Env         map[string]string
	InstallHint string
}

// Kubeconfig describes the file Write renders: one cluster, the contexts Name
// (default Context) and ARNContext pointing at it, and one user with Token or the
// exec plugin.
type Kubeconfig struct {
	// Name is the context, cluster and user name; empty means Context.
	Name   string
	Server string
	CAPEM  string
	// Namespace is the context's namespace; empty leaves it unset.
	Namespace string
	// Token is the static credential; empty means the Token constant.
	Token string
	Exec  *ExecPlugin
	// NoCurrentContext leaves current-context unset.
	NoCurrentContext bool
}

// Write renders the kubeconfig into a temporary directory and returns its path.
func (k Kubeconfig) Write(t *testing.T) string {
	t.Helper()
	name := k.Name
	if name == "" {
		name = Context
	}
	config := clientcmdapi.NewConfig()
	config.Clusters[name] = &clientcmdapi.Cluster{
		Server:                   k.Server,
		CertificateAuthorityData: []byte(k.CAPEM),
	}
	config.Contexts[name] = &clientcmdapi.Context{Cluster: name, AuthInfo: name, Namespace: k.Namespace}
	config.Contexts[ARNContext] = &clientcmdapi.Context{Cluster: name, AuthInfo: name}
	if !k.NoCurrentContext {
		config.CurrentContext = name
	}

	auth := &clientcmdapi.AuthInfo{}
	if k.Exec != nil {
		auth.Exec = &clientcmdapi.ExecConfig{
			Command:         k.Exec.Command,
			APIVersion:      "client.authentication.k8s.io/v1",
			InteractiveMode: clientcmdapi.IfAvailableExecInteractiveMode,
			InstallHint:     k.Exec.InstallHint,
		}
		for name, value := range k.Exec.Env {
			auth.Exec.Env = append(auth.Exec.Env, clientcmdapi.ExecEnvVar{Name: name, Value: value})
		}
	} else if k.Token != "" {
		auth.Token = k.Token
	} else {
		auth.Token = Token
	}
	config.AuthInfos[name] = auth

	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := clientcmd.WriteToFile(*config, path); err != nil {
		t.Fatal(err)
	}
	return path
}
