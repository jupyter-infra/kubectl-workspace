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
	"net/http"
	"net/url"
	"strings"

	"k8s.io/client-go/rest"
)

// connectionRequest mirrors WorkspaceConnectionRequest in the operator's
// api/connection/v1alpha1 package. The operator module is not imported because
// it would pull the controller's dependency graph into this binary.
type connectionRequest struct {
	APIVersion string             `json:"apiVersion"`
	Kind       string             `json:"kind"`
	Metadata   connectionMetadata `json:"metadata"`
	Spec       connectionSpec     `json:"spec"`
}

type connectionMetadata struct {
	Namespace string `json:"namespace"`
}

type connectionSpec struct {
	WorkspaceName           string `json:"workspaceName"`
	WorkspaceConnectionType string `json:"workspaceConnectionType"`
}

type connectionResponse struct {
	Status connectionStatus `json:"status"`
}

type connectionStatus struct {
	WorkspaceConnectionURL string `json:"workspaceConnectionUrl"`
}

// apiStatus is the Kubernetes Status object an error response carries.
type apiStatus struct {
	Message string `json:"message"`
}

// Connection is the operator's answer: the WebSocket URL and the token that
// authorizes it.
type Connection struct {
	URL   *url.URL
	Token string
}

// CreateConnection asks the operator for an ssh-over-websocket connection to the
// workspace. The operator checks that the caller may create connections in the
// namespace and may reach the workspace, that the workspace is Available and that
// its access strategy has the WebSocket route, then mints a short-lived token.
func CreateConnection(ctx context.Context, config *rest.Config, namespace, workspace string) (*Connection, error) {
	client, err := rest.HTTPClientFor(config)
	if err != nil {
		return nil, fmt.Errorf("failed to build the API client: %w", err)
	}
	base, _, err := rest.DefaultServerUrlFor(config)
	if err != nil {
		return nil, fmt.Errorf("invalid API server address %q: %w", config.Host, err)
	}
	endpoint := base.JoinPath(connectionAPIPath, "namespaces", namespace, "workspaceconnections")

	body, err := json.Marshal(connectionRequest{
		APIVersion: connectionAPIVersion,
		Kind:       connectionKind,
		Metadata:   connectionMetadata{Namespace: namespace},
		Spec:       connectionSpec{WorkspaceName: workspace, WorkspaceConnectionType: connectionTypeSSH},
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		// The URL error repeats the host the prefix already names.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return nil, fmt.Errorf("failed to reach the API server at %s: %w", base.Host, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to read the API server's response: %w", err)
	}
	if resp.StatusCode != http.StatusCreated {
		return nil, responseError(resp.StatusCode, data, namespace, workspace)
	}

	var out connectionResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("invalid connection response: %w", err)
	}
	if out.Status.WorkspaceConnectionURL == "" {
		return nil, errors.New("the connection response carries no URL")
	}
	conn, err := ParseConnectionURL(out.Status.WorkspaceConnectionURL)
	if err != nil {
		return nil, err
	}
	if conn.Token == "" {
		return nil, errors.New("the connection URL carries no token")
	}
	return conn, nil
}

// responseError turns the operator's Status object into one line for the user.
func responseError(code int, data []byte, namespace, workspace string) error {
	var status apiStatus
	_ = json.Unmarshal(data, &status)
	message := strings.TrimSpace(status.Message)
	if message == "" {
		message = strings.Join(strings.Fields(string(data)), " ")
		if len(message) > maxErrorDetailBytes {
			message = message[:maxErrorDetailBytes] + "..."
		}
	}
	name := namespace + "/" + workspace
	switch code {
	case http.StatusUnauthorized:
		return fmt.Errorf("the API server rejected the credential (HTTP 401): %s", message)
	case http.StatusForbidden:
		return fmt.Errorf("not allowed to connect to workspace %s (HTTP 403): %s", name, message)
	case http.StatusNotFound:
		return fmt.Errorf("workspace %s or the connection API was not found (HTTP 404): %s", name, message)
	default:
		return fmt.Errorf("the connection request for workspace %s failed (HTTP %d): %s", name, code, message)
	}
}

// ParseConnectionURL splits a token out of the URL's query, when there is one.
// The auth middleware reads the token from the Authorization header as well, and
// a header stays out of access logs and error messages.
func ParseConnectionURL(raw string) (*Connection, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid connection URL: %w", err)
	}
	query := u.Query()
	token := query.Get("token")
	query.Del("token")
	u.RawQuery = query.Encode()
	return &Connection{URL: u, Token: token}, nil
}
