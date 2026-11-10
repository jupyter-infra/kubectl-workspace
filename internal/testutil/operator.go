/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

// Package testutil provides the fakes the plugin's tests share: an operator that
// answers connection requests, a WebSocket echo server standing in for the
// sidecar, kubeconfig files pointing at them, an HTTP CONNECT proxy and a
// listener that never answers.
package testutil

import (
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"k8s.io/client-go/rest"
)

// ConnectionRequest is the body the fake operator decodes from a request.
type ConnectionRequest struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		WorkspaceName           string `json:"workspaceName"`
		WorkspaceConnectionType string `json:"workspaceConnectionType"`
	} `json:"spec"`
}

// RecordedRequest is one request the fake operator received.
type RecordedRequest struct {
	Method        string
	Path          string
	Authorization string
	ContentType   string
	Body          ConnectionRequest
}

// FakeOperator serves the connection API over TLS with a self-signed certificate,
// answers every request with one status and body, and records what it received.
type FakeOperator struct {
	server *httptest.Server

	mu       sync.Mutex
	status   int
	body     string
	requests []RecordedRequest
}

// NewFakeOperator starts an operator answering every request with status and body.
func NewFakeOperator(t *testing.T, status int, body string) *FakeOperator {
	t.Helper()
	op := &FakeOperator{status: status, body: body}
	op.server = httptest.NewTLSServer(op)
	t.Cleanup(op.server.Close)
	return op
}

// ServeHTTP records the request and writes the configured answer.
func (o *FakeOperator) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rec := RecordedRequest{
		Method:        r.Method,
		Path:          r.URL.Path,
		Authorization: r.Header.Get("Authorization"),
		ContentType:   r.Header.Get("Content-Type"),
	}
	_ = json.NewDecoder(r.Body).Decode(&rec.Body)

	o.mu.Lock()
	o.requests = append(o.requests, rec)
	status, body := o.status, o.body
	o.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// URL is the operator's https address.
func (o *FakeOperator) URL() string {
	return o.server.URL
}

// CAPEM is the certificate a client must trust to reach the operator.
func (o *FakeOperator) CAPEM() string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: o.server.Certificate().Raw}))
}

// RestConfig reaches the operator with a static bearer token and its CA.
func (o *FakeOperator) RestConfig(token string) *rest.Config {
	return &rest.Config{
		Host:            o.URL(),
		BearerToken:     token,
		TLSClientConfig: rest.TLSClientConfig{CAData: []byte(o.CAPEM())},
	}
}

// Requests returns a copy of the recorded requests.
func (o *FakeOperator) Requests() []RecordedRequest {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]RecordedRequest(nil), o.requests...)
}

// LastRequest returns the most recent request and fails the test when there is none.
func (o *FakeOperator) LastRequest(t *testing.T) RecordedRequest {
	t.Helper()
	requests := o.Requests()
	if len(requests) == 0 {
		t.Fatal("the operator received no request")
	}
	return requests[len(requests)-1]
}

// ConnectionBody renders the operator's answer to a granted connection.
func ConnectionBody(connectionURL string) string {
	body, _ := json.Marshal(map[string]any{
		"apiVersion": "connection.workspace.jupyter.org/v1alpha1",
		"kind":       "WorkspaceConnection",
		"status": map[string]string{
			"workspaceConnectionType": "ssh-over-websocket",
			"workspaceConnectionUrl":  connectionURL,
		},
	})
	return string(body)
}

// StatusBody renders the Kubernetes Status object the operator sends on errors.
func StatusBody(code int, message string) string {
	body, _ := json.Marshal(map[string]any{
		"kind":       "Status",
		"apiVersion": "v1",
		"status":     "Failure",
		"message":    message,
		"code":       code,
	})
	return string(body)
}
