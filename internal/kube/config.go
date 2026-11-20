/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

// Package kube reads the user's kubeconfig, resolves its credential without
// blocking on a sign-in, and asks the operator for a workspace connection.
package kube

import (
	"errors"
	"fmt"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/jupyter-infra/kubectl-workspace/internal/fsutil"
)

// requestTimeout bounds one request to the API server. Remote-SSH gives the whole
// connection 15 seconds by default.
const requestTimeout = 10 * time.Second

// Target is the kubeconfig context a command works against.
type Target struct {
	// Context is the resolved context name.
	Context string
	// Namespace is the context's namespace, "default" when it sets none.
	Namespace string
	// Kubeconfig is the file that defines the context, absolute: the --kubeconfig file, or
	// whichever of KUBECONFIG's files or the default location holds the context.
	Kubeconfig string
	// Config reaches the cluster. Until ResolveCredential runs it may still name
	// an exec credential plugin.
	Config *rest.Config
	// clusterFile and userFile are the files that define the cluster and the user the
	// context names, for StandsAlone; the user's is empty when none defines it.
	clusterFile, userFile string
}

// Load reads the kubeconfig and selects contextName, or the current context when
// empty. kubeconfigPath overrides KUBECONFIG and the default location.
func Load(kubeconfigPath, contextName, userAgent string) (*Target, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfigPath != "" {
		rules.ExplicitPath = kubeconfigPath
	}
	overrides := &clientcmd.ConfigOverrides{CurrentContext: contextName}
	loader := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides)

	raw, err := loader.RawConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to read the kubeconfig: %w", err)
	}
	name := contextName
	if name == "" {
		name = raw.CurrentContext
	}
	if name == "" {
		return nil, errors.New("no kubeconfig context selected; pass --context or set a current context")
	}
	context, ok := raw.Contexts[name]
	if !ok {
		return nil, fmt.Errorf("context %q not found in the kubeconfig", name)
	}
	// client-go records the file each context came from when it merges KUBECONFIG's files.
	kubeconfig, err := fsutil.Abs("kubeconfig", context.LocationOfOrigin)
	if err != nil {
		return nil, err
	}
	namespace, _, err := loader.Namespace()
	if err != nil {
		return nil, fmt.Errorf("failed to resolve the namespace of context %q: %w", name, err)
	}
	config, err := loader.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to build the client configuration for context %q: %w", name, err)
	}
	config.Timeout = requestTimeout
	config.UserAgent = userAgent

	target := &Target{Context: name, Namespace: namespace, Kubeconfig: kubeconfig, Config: config}
	if cluster, ok := raw.Clusters[context.Cluster]; ok {
		if target.clusterFile, err = fsutil.Abs("kubeconfig", cluster.LocationOfOrigin); err != nil {
			return nil, err
		}
	}
	if user, ok := raw.AuthInfos[context.AuthInfo]; ok {
		if target.userFile, err = fsutil.Abs("kubeconfig", user.LocationOfOrigin); err != nil {
			return nil, err
		}
	}
	return target, nil
}

// StandsAlone checks that the file defining the context also defines the cluster and the
// user the context names, so that a process reading that file alone, as connect does,
// resolves the context as this load did. client-go merges KUBECONFIG's files with the
// first definition of a name winning, so a context's cluster or user can come from
// another file, also when the context's own file defines them too.
func (t *Target) StandsAlone() error {
	for _, part := range []struct{ what, file string }{{"cluster", t.clusterFile}, {"user", t.userFile}} {
		if part.file != "" && !fsutil.SameFile(part.file, t.Kubeconfig) {
			return fmt.Errorf("context %q is defined in %s but the %s it names is taken from %s", t.Context, t.Kubeconfig,
				part.what, part.file)
		}
	}
	return nil
}

// DefaultKubeconfig returns the kubeconfig file client-go reads when neither --kubeconfig
// nor KUBECONFIG names one, ~/.kube/config.
func DefaultKubeconfig() string {
	return clientcmd.RecommendedHomeFile
}
