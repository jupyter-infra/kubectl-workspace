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
	// Config reaches the cluster. Until ResolveCredential runs it may still name
	// an exec credential plugin.
	Config *rest.Config
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
	if _, ok := raw.Contexts[name]; !ok {
		return nil, fmt.Errorf("context %q not found in the kubeconfig", name)
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

	return &Target{Context: name, Namespace: namespace, Config: config}, nil
}
