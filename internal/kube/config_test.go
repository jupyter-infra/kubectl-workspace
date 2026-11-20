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

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

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
	if target.Kubeconfig != second {
		t.Errorf("kubeconfig = %s, want the file that defines the context, %s", target.Kubeconfig, second)
	}
}

// The entry ssh-config writes carries the file, and OpenSSH runs the ProxyCommand from
// its own working directory, so the file is absolute whichever way it was named.
func TestLoadNamesTheKubeconfigFileAbsolutely(t *testing.T) {
	kubeconfig := testutil.Kubeconfig{Server: testServer}.Write(t)
	t.Chdir(filepath.Dir(kubeconfig))

	target, err := Load(filepath.Base(kubeconfig), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if target.Kubeconfig != kubeconfig {
		t.Errorf("kubeconfig = %s, want %s", target.Kubeconfig, kubeconfig)
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

// A context resolves from its own file alone only when the cluster and the user it names
// come from that file too; under a KUBECONFIG list either can come from another file,
// also when the context's file defines them as well, since the first definition wins.
func TestStandsAlone(t *testing.T) {
	write := func(t *testing.T, name string, fill func(*clientcmdapi.Config)) string {
		t.Helper()
		config := clientcmdapi.NewConfig()
		fill(config)
		path := filepath.Join(t.TempDir(), name)
		if err := clientcmd.WriteToFile(*config, path); err != nil {
			t.Fatal(err)
		}
		return path
	}
	context := func(c *clientcmdapi.Config) {
		c.Contexts["dev"] = &clientcmdapi.Context{Cluster: "dev", AuthInfo: "dev"}
		c.CurrentContext = "dev"
	}
	cluster := func(c *clientcmdapi.Config) { c.Clusters["dev"] = &clientcmdapi.Cluster{Server: testServer} }
	user := func(c *clientcmdapi.Config) { c.AuthInfos["dev"] = &clientcmdapi.AuthInfo{Token: "t"} }
	all := func(fills ...func(*clientcmdapi.Config)) func(*clientcmdapi.Config) {
		return func(c *clientcmdapi.Config) {
			for _, fill := range fills {
				fill(c)
			}
		}
	}
	tests := []struct {
		name  string
		own   func(*clientcmdapi.Config)
		other func(*clientcmdapi.Config)
		// otherFirst puts the other file first in KUBECONFIG, where its definitions win.
		otherFirst bool
		wantErr    string
	}{
		{name: "the cluster and the user in the file", own: all(context, cluster, user), other: func(*clientcmdapi.Config) {}},
		{
			name: "the user in another file", own: all(context, cluster), other: user,
			wantErr: "the user it names is taken from",
		},
		{
			name: "the cluster in another file", own: all(context, user), other: cluster,
			wantErr: "the cluster it names is taken from",
		},
		{
			name: "the cluster shadowed by an earlier file", own: all(context, cluster, user), other: cluster, otherFirst: true,
			wantErr: "the cluster it names is taken from",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			own, other := write(t, "own", tt.own), write(t, "other", tt.other)
			files := []string{own, other}
			if tt.otherFirst {
				files = []string{other, own}
			}
			t.Setenv("KUBECONFIG", strings.Join(files, string(os.PathListSeparator)))
			target, err := Load("", "dev", "")
			if err != nil {
				t.Fatal(err)
			}
			err = target.StandsAlone()
			if tt.wantErr == "" && err != nil {
				t.Errorf("error = %v, want nil", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr+" "+other)) {
				t.Errorf("error = %v, want %q naming %s", err, tt.wantErr, other)
			}
		})
	}
}
