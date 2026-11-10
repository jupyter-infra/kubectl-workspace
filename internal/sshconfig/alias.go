/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

// Package sshconfig turns workspace names into SSH host aliases and manages the
// per-cluster SSH configuration entry that routes those aliases through
// `kubectl workspace connect`.
package sshconfig

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

// AliasSuffix is the last label of every workspace alias. The configuration
// entry matches `*.<cluster>.jk8s`, so the suffix keeps it away from every other
// host the user connects to.
const AliasSuffix = "jk8s"

// AliasFormat is the alias shape as shown in messages and help text.
const AliasFormat = "<workspace>.<namespace>.<cluster>." + AliasSuffix

// Alias is the SSH host alias of one workspace: `<workspace>.<namespace>.<cluster>.jk8s`.
// Cluster is the deployer's name for the cluster, not the kubeconfig context:
// context names such as EKS ARNs contain `:` and `/`, which neither an SSH host
// argument nor the editor's remote URI can carry, so the context travels in the
// configuration entry and the alias stays lowercase DNS labels.
type Alias struct {
	Workspace string
	Namespace string
	Cluster   string
}

// NewAlias validates the three names and builds the alias.
func NewAlias(workspace, namespace, cluster string) (Alias, error) {
	a := Alias{Workspace: workspace, Namespace: namespace, Cluster: cluster}
	return a, a.validate()
}

// ParseAlias splits an alias from the right: the suffix, the cluster and the
// namespace are single labels, and whatever remains is the workspace name, which
// Kubernetes allows to contain dots. The input is lowercased first because
// OpenSSH lowercases the host before expanding %h.
func ParseAlias(host string) (Alias, error) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	labels := strings.Split(host, ".")
	if len(labels) < 4 || labels[len(labels)-1] != AliasSuffix {
		return Alias{}, fmt.Errorf("%q is not a workspace alias; expected %s", host, AliasFormat)
	}
	a := Alias{
		Workspace: strings.Join(labels[:len(labels)-3], "."),
		Namespace: labels[len(labels)-3],
		Cluster:   labels[len(labels)-2],
	}
	return a, a.validate()
}

// String renders the alias.
func (a Alias) String() string {
	return strings.Join([]string{a.Workspace, a.Namespace, a.Cluster, AliasSuffix}, ".")
}

func (a Alias) validate() error {
	if errs := validation.IsDNS1123Subdomain(a.Workspace); len(errs) > 0 {
		return fmt.Errorf("invalid workspace name %q: %s", a.Workspace, errs[0])
	}
	if errs := validation.IsDNS1123Label(a.Namespace); len(errs) > 0 {
		return fmt.Errorf("invalid namespace %q: %s", a.Namespace, errs[0])
	}
	return ValidateClusterName(a.Cluster)
}

// ValidateClusterName checks that a cluster name can be one label of an alias.
func ValidateClusterName(cluster string) error {
	if errs := validation.IsDNS1123Label(cluster); len(errs) > 0 {
		return fmt.Errorf("invalid cluster name %q: %s", cluster, errs[0])
	}
	return nil
}
