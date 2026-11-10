/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package kube

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"

	"github.com/jupyter-infra/kubectl-workspace/internal/testutil"
)

const testServer = "https://dev.example.invalid:6443"

func TestLoadUsesTheCurrentContext(t *testing.T) {
	kubeconfig := testutil.Kubeconfig{Server: testServer, Namespace: "team-a"}.Write(t)

	target, err := Load(kubeconfig, "", "kubectl-workspace/test")
	if err != nil {
		t.Fatal(err)
	}
	if target.Context != testutil.Context || target.Namespace != "team-a" {
		t.Errorf("context/namespace = %s/%s, want %s/team-a", target.Context, target.Namespace, testutil.Context)
	}
	if target.Config.Host != testServer || target.Config.BearerToken != testutil.Token {
		t.Errorf("config host/token = %s/%s", target.Config.Host, target.Config.BearerToken)
	}
	if target.Config.UserAgent != "kubectl-workspace/test" || target.Config.Timeout != requestTimeout {
		t.Errorf("user agent/timeout = %s/%s", target.Config.UserAgent, target.Config.Timeout)
	}
	if target.cluster == nil || target.cluster.Server != testServer {
		t.Errorf("cluster entry = %+v", target.cluster)
	}
}

func TestLoadSelectsTheRequestedContextAndDefaultsItsNamespace(t *testing.T) {
	kubeconfig := testutil.Kubeconfig{Server: testServer, Namespace: "team-a"}.Write(t)

	target, err := Load(kubeconfig, testutil.ARNContext, "")
	if err != nil {
		t.Fatal(err)
	}
	if target.Context != testutil.ARNContext || target.Namespace != "default" {
		t.Errorf("context/namespace = %s/%s, want the ARN context and the default namespace",
			target.Context, target.Namespace)
	}
}

func TestLoadMergesAKUBECONFIGList(t *testing.T) {
	first := testutil.Kubeconfig{Server: testServer, Namespace: "team-a"}.Write(t)
	second := testutil.Kubeconfig{
		Name: "other", Server: "https://other.example.invalid:6443", Namespace: "team-b", NoCurrentContext: true,
	}.Write(t)
	t.Setenv("KUBECONFIG", first+string(os.PathListSeparator)+second)

	target, err := Load("", "other", "")
	if err != nil {
		t.Fatal(err)
	}
	if target.Namespace != "team-b" || target.Config.Host != "https://other.example.invalid:6443" {
		t.Errorf("namespace/host = %s/%s, want the second file's context", target.Namespace, target.Config.Host)
	}
}

func TestLoadRejectsAnUnknownContext(t *testing.T) {
	kubeconfig := testutil.Kubeconfig{Server: testServer}.Write(t)
	_, err := Load(kubeconfig, "staging", "")
	if err == nil || !strings.Contains(err.Error(), `context "staging" not found`) {
		t.Errorf("error = %v", err)
	}
}

func TestLoadRequiresAContext(t *testing.T) {
	kubeconfig := testutil.Kubeconfig{Server: testServer, NoCurrentContext: true}.Write(t)
	_, err := Load(kubeconfig, "", "")
	if err == nil || !strings.Contains(err.Error(), "no kubeconfig context selected") {
		t.Errorf("error = %v", err)
	}
}

func TestLoadReportsAMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "missing"), "", "")
	if err == nil || !strings.Contains(err.Error(), "failed to read the kubeconfig") {
		t.Errorf("error = %v", err)
	}
}

func TestRawExtensionKeepsTheKubeconfigBytes(t *testing.T) {
	got, err := rawExtension(&runtime.Unknown{Raw: []byte(`{"region":"us-west-2"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Raw) != `{"region":"us-west-2"}` {
		t.Errorf("Raw = %s", got.Raw)
	}
	empty, err := rawExtension(nil)
	if err != nil || empty.Raw != nil {
		t.Errorf("rawExtension(nil) = %+v, %v", empty, err)
	}
}
