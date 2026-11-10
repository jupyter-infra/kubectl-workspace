/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package kube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/term"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientauthv1 "k8s.io/client-go/pkg/apis/clientauthentication/v1"
	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// execInfoEnv is the variable through which client-go describes the request to
// an exec credential plugin.
const execInfoEnv = "KUBERNETES_EXEC_INFO"

// killDelay is how long a plugin gets to exit after SIGTERM before SIGKILL.
const killDelay = 2 * time.Second

// SignInRequiredError reports that the exec credential plugin produced nothing
// before the deadline, which is how a plugin waiting for a browser sign-in looks
// from the outside.
type SignInRequiredError struct {
	Context string
	Command string
	Timeout time.Duration
}

func (e *SignInRequiredError) Error() string {
	return fmt.Sprintf("the credential plugin for context %q (%s) returned nothing within %s, "+
		"which is how a plugin waiting for a browser sign-in looks", e.Context, e.Command, e.Timeout)
}

// PluginExitError reports a credential plugin that ran and exited non-zero.
type PluginExitError struct {
	Command string
	Status  int
}

func (e *PluginExitError) Error() string {
	return fmt.Sprintf("credential plugin %s exited with status %d", e.Command, e.Status)
}

// CredentialOptions controls how the exec credential plugin runs.
type CredentialOptions struct {
	// Interactive passes Stdin to the plugin and lets it wait for a sign-in.
	Interactive bool
	// Stdin is handed to the plugin only when Interactive is set.
	Stdin io.Reader
	// Stderr receives the plugin's diagnostics unchanged.
	Stderr io.Writer
	// Timeout kills a non-interactive plugin that has not answered.
	Timeout time.Duration
}

// ResolveCredential runs the exec credential plugin the kubeconfig names, if any,
// and replaces it in the target's configuration with the token or client
// certificate it returned. client-go would run the plugin on the first request
// with no deadline and with the process's stdin, which for a ProxyCommand is the
// SSH byte stream. Here a non-interactive plugin gets no stdin, sees
// interactive=false in KUBERNETES_EXEC_INFO, and is killed at the deadline.
func ResolveCredential(ctx context.Context, target *Target, opts CredentialOptions) error {
	provider := target.Config.ExecProvider
	if provider == nil {
		return nil
	}
	interactive, err := interactiveFor(provider, opts)
	if err != nil {
		return err
	}
	info, err := execInfo(target, provider, interactive)
	if err != nil {
		return err
	}
	if opts.Timeout > 0 && !interactive {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, provider.Command, provider.Args...)
	cmd.Env = os.Environ()
	for _, v := range provider.Env {
		cmd.Env = append(cmd.Env, v.Name+"="+v.Value)
	}
	cmd.Env = append(cmd.Env, execInfoEnv+"="+info)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = opts.Stderr
	if interactive {
		cmd.Stdin = opts.Stdin
	} else {
		// A wrapper that does not exec the real plugin leaves it running past the
		// deadline unless the whole process group is signalled.
		setProcessGroup(cmd)
	}
	cmd.Cancel = func() error { return terminate(cmd.Process, !interactive) }
	cmd.WaitDelay = killDelay

	// The deadline is read only for a failed run: a plugin that exited successfully
	// produced its credential, even when the deadline passed between its exit and this check.
	if runErr := cmd.Run(); runErr != nil {
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			return &SignInRequiredError{Context: target.Context, Command: provider.Command, Timeout: opts.Timeout}
		case ctx.Err() != nil:
			return ctx.Err()
		}
		return runError(provider, runErr)
	}

	status, err := parseCredential(stdout.Bytes(), provider.APIVersion)
	if err != nil {
		return fmt.Errorf("credential plugin %s: %w", provider.Command, err)
	}
	apply(target.Config, status)
	return nil
}

// interactiveFor applies the kubeconfig's interactiveMode as client-go does; a
// non-interactive caller never passes stdin.
func interactiveFor(provider *clientcmdapi.ExecConfig, opts CredentialOptions) (bool, error) {
	if !opts.Interactive {
		return false, nil
	}
	terminal := isTerminal(opts.Stdin)
	switch provider.InteractiveMode {
	case clientcmdapi.NeverExecInteractiveMode:
		return false, nil
	case clientcmdapi.AlwaysExecInteractiveMode:
		if !terminal {
			return false, fmt.Errorf("credential plugin %s requires a terminal (interactiveMode: Always) "+
				"and stdin is not one", provider.Command)
		}
		return true, nil
	default:
		return terminal, nil
	}
}

// isTerminal reports whether the reader is a terminal; a variable so tests can
// exercise the interactive path without one.
var isTerminal = func(r io.Reader) bool {
	f, ok := r.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// runError explains a plugin that could not run or exited non-zero. The install
// hint is the deployer's text in the kubeconfig on where to get the plugin.
func runError(provider *clientcmdapi.ExecConfig, err error) error {
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		return &PluginExitError{Command: provider.Command, Status: exitErr.ExitCode()}
	case errors.Is(err, exec.ErrNotFound) && provider.InstallHint != "":
		return fmt.Errorf("credential plugin %s is not installed: %w\n%s",
			provider.Command, err, strings.TrimSpace(provider.InstallHint))
	default:
		return fmt.Errorf("failed to run credential plugin %s: %w", provider.Command, err)
	}
}

// execInfo builds the ExecCredential a plugin reads from KUBERNETES_EXEC_INFO.
func execInfo(target *Target, provider *clientcmdapi.ExecConfig, interactive bool) (string, error) {
	cred := clientauthv1.ExecCredential{
		TypeMeta: metav1.TypeMeta{APIVersion: provider.APIVersion, Kind: "ExecCredential"},
		Spec:     clientauthv1.ExecCredentialSpec{Interactive: interactive},
	}
	if provider.ProvideClusterInfo && target.cluster != nil {
		cluster, err := clusterInfo(target.cluster, provider)
		if err != nil {
			return "", err
		}
		cred.Spec.Cluster = cluster
	}
	data, err := json.Marshal(cred)
	if err != nil {
		return "", fmt.Errorf("failed to encode %s: %w", execInfoEnv, err)
	}
	return string(data), nil
}

func clusterInfo(c *clientcmdapi.Cluster, provider *clientcmdapi.ExecConfig) (*clientauthv1.Cluster, error) {
	caData := c.CertificateAuthorityData
	if len(caData) == 0 && c.CertificateAuthority != "" {
		data, err := os.ReadFile(c.CertificateAuthority)
		if err != nil {
			return nil, fmt.Errorf("failed to read the certificate authority file: %w", err)
		}
		caData = data
	}
	extension, err := rawExtension(provider.Config)
	if err != nil {
		return nil, fmt.Errorf("failed to encode the exec plugin's config: %w", err)
	}
	return &clientauthv1.Cluster{
		Server:                   c.Server,
		TLSServerName:            c.TLSServerName,
		InsecureSkipTLSVerify:    c.InsecureSkipTLSVerify,
		CertificateAuthorityData: caData,
		ProxyURL:                 c.ProxyURL,
		DisableCompression:       c.DisableCompression,
		Config:                   extension,
	}, nil
}

// rawExtension serializes the exec plugin's free-form config the way client-go's
// codec does when it converts the kubeconfig to the plugin's API version.
func rawExtension(obj runtime.Object) (runtime.RawExtension, error) {
	if obj == nil {
		return runtime.RawExtension{}, nil
	}
	if unknown, ok := obj.(*runtime.Unknown); ok {
		return runtime.RawExtension{Raw: unknown.Raw}, nil
	}
	data, err := json.Marshal(obj)
	if err != nil {
		return runtime.RawExtension{}, err
	}
	return runtime.RawExtension{Raw: data}, nil
}

// parseCredential decodes the plugin's output with the checks client-go applies.
func parseCredential(out []byte, apiVersion string) (*clientauthv1.ExecCredentialStatus, error) {
	var cred clientauthv1.ExecCredential
	if err := json.Unmarshal(out, &cred); err != nil {
		return nil, fmt.Errorf("invalid output: %w", err)
	}
	if cred.APIVersion != apiVersion {
		return nil, fmt.Errorf("configured for API version %s, but the plugin returned %q", apiVersion, cred.APIVersion)
	}
	status := cred.Status
	if status == nil {
		return nil, errors.New("output has no status")
	}
	if status.Token == "" && status.ClientCertificateData == "" && status.ClientKeyData == "" {
		return nil, errors.New("output has neither a token nor a certificate and key")
	}
	if (status.ClientCertificateData == "") != (status.ClientKeyData == "") {
		return nil, errors.New("output has only one of certificate and key")
	}
	return status, nil
}

// apply replaces the exec plugin in the configuration with its result.
func apply(config *rest.Config, status *clientauthv1.ExecCredentialStatus) {
	config.ExecProvider = nil
	config.BearerToken = status.Token
	config.BearerTokenFile = ""
	if status.ClientCertificateData != "" {
		config.CertData = []byte(status.ClientCertificateData)
		config.KeyData = []byte(status.ClientKeyData)
		config.CertFile = ""
		config.KeyFile = ""
	}
}
