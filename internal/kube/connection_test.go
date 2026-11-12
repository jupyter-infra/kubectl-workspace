/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package kube

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"strings"
	"testing"

	"k8s.io/client-go/rest"

	"github.com/jupyter-infra/kubectl-workspace/internal/testutil"
)

const wantPath = "/apis/connection.workspace.jupyter.org/v1alpha1/namespaces/team-a/workspaceconnections"

func TestCreateConnectionPostsTheRequestAndSplitsTheToken(t *testing.T) {
	op := testutil.NewFakeOperator(t, http.StatusCreated,
		testutil.ConnectionBody("wss://jk8s.example.com/workspaces/team-a/ws1/ssh-ws?token=abc.def.ghi"))

	conn, err := CreateConnection(context.Background(), op.RestConfig("kube-token"), "team-a", "ws1")
	if err != nil {
		t.Fatal(err)
	}

	req := op.LastRequest(t)
	if req.Method != http.MethodPost || req.Path != wantPath {
		t.Errorf("request = %s %s, want POST %s", req.Method, req.Path, wantPath)
	}
	if req.Authorization != "Bearer kube-token" {
		t.Errorf("Authorization = %q, want the kubeconfig token", req.Authorization)
	}
	if req.ContentType != "application/json" {
		t.Errorf("Content-Type = %q", req.ContentType)
	}
	if req.Body.Kind != "WorkspaceConnection" || req.Body.APIVersion != connectionAPIVersion {
		t.Errorf("request type = %s %s", req.Body.APIVersion, req.Body.Kind)
	}
	if req.Body.Spec.WorkspaceName != "ws1" || req.Body.Spec.WorkspaceConnectionType != connectionTypeSSH {
		t.Errorf("request spec = %+v", req.Body.Spec)
	}
	if req.Body.Metadata.Namespace != "team-a" {
		t.Errorf("request namespace = %q", req.Body.Metadata.Namespace)
	}
	if conn.Token != "abc.def.ghi" {
		t.Errorf("Token = %q", conn.Token)
	}
	if got := conn.URL.String(); got != "wss://jk8s.example.com/workspaces/team-a/ws1/ssh-ws" {
		t.Errorf("URL = %q, want the token removed", got)
	}
}

func TestCreateConnectionTrustsOnlyTheClusterCA(t *testing.T) {
	op := testutil.NewFakeOperator(t, http.StatusCreated, "{}")
	config := op.RestConfig("kube-token")
	config.TLSClientConfig = rest.TLSClientConfig{}

	_, err := CreateConnection(context.Background(), config, "team-a", "ws1")
	var verification *tls.CertificateVerificationError
	if !errors.As(err, &verification) {
		t.Fatalf("error = %v, want the self-signed certificate to fail verification", err)
	}
}

func TestCreateConnectionReportsTheOperatorsMessage(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr []string
	}{
		{
			name:    "not available",
			status:  http.StatusBadRequest,
			body:    testutil.StatusBody(400, "workspace is not available. Check workspace status for details"),
			wantErr: []string{"HTTP 400", "workspace is not available"},
		},
		{
			name:    "forbidden",
			status:  http.StatusForbidden,
			body:    testutil.StatusBody(403, "user is not the owner"),
			wantErr: []string{"not allowed to connect to workspace team-a/ws1", "user is not the owner"},
		},
		{
			name:    "not found",
			status:  http.StatusNotFound,
			body:    testutil.StatusBody(404, "workspace not found"),
			wantErr: []string{"HTTP 404", "workspace not found"},
		},
		{
			name:    "unauthorized without a Status body",
			status:  http.StatusUnauthorized,
			body:    `Unauthorized`,
			wantErr: []string{"rejected the credential", "Unauthorized"},
		},
		{
			name:    "no websocket route",
			status:  http.StatusBadRequest,
			body:    testutil.StatusBody(400, "WebSocket access is not enabled for this workspace"),
			wantErr: []string{"WebSocket access is not enabled"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			op := testutil.NewFakeOperator(t, tt.status, tt.body)
			_, err := CreateConnection(context.Background(), op.RestConfig("kube-token"), "team-a", "ws1")
			if err == nil {
				t.Fatal("expected an error")
			}
			for _, want := range tt.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
		})
	}
}

func TestCreateConnectionReportsAnUnreachableAPIServer(t *testing.T) {
	config := &rest.Config{Host: "https://127.0.0.1:1", BearerToken: "kube-token"}
	_, err := CreateConnection(context.Background(), config, "team-a", "ws1")
	if err == nil || !strings.Contains(err.Error(), "failed to reach the API server at 127.0.0.1:1") {
		t.Errorf("error = %v", err)
	}
}

func TestCreateConnectionCutsANonStatusErrorBodyShort(t *testing.T) {
	page := "<html>\n<body>\n" + strings.Repeat("<p>Bad gateway</p>\n", 400) + "</body>\n</html>"
	op := testutil.NewFakeOperator(t, http.StatusBadGateway, page)
	_, err := CreateConnection(context.Background(), op.RestConfig("kube-token"), "team-a", "ws1")
	if err == nil {
		t.Fatal("expected an error")
	}
	if len(err.Error()) > 700 || strings.Contains(err.Error(), "\n") || !strings.HasSuffix(err.Error(), "...") {
		t.Errorf("error = %d bytes, want one short line ending in ...: %.120s", len(err.Error()), err.Error())
	}
}

func TestCreateConnectionRejectsAnUnreadableAnswer(t *testing.T) {
	op := testutil.NewFakeOperator(t, http.StatusCreated, "not json")
	_, err := CreateConnection(context.Background(), op.RestConfig("kube-token"), "team-a", "ws1")
	if err == nil || !strings.Contains(err.Error(), "invalid connection response") {
		t.Errorf("error = %v", err)
	}
}

func TestParseConnectionURLToleratesAMissingToken(t *testing.T) {
	conn, err := ParseConnectionURL("ws://127.0.0.1:18080/")
	if err != nil {
		t.Fatal(err)
	}
	if conn.Token != "" || conn.URL.String() != "ws://127.0.0.1:18080/" {
		t.Errorf("ParseConnectionURL = %+v", conn)
	}
}

func TestCreateConnectionRejectsAnAnswerWithoutAURL(t *testing.T) {
	op := testutil.NewFakeOperator(t, http.StatusCreated, testutil.ConnectionBody(""))

	_, err := CreateConnection(context.Background(), op.RestConfig("kube-token"), "team-a", "ws1")
	if err == nil || !strings.Contains(err.Error(), "no URL") {
		t.Errorf("error = %v, want a complaint about the missing URL", err)
	}
}

func TestCreateConnectionRejectsAURLWithoutAToken(t *testing.T) {
	op := testutil.NewFakeOperator(t, http.StatusCreated,
		testutil.ConnectionBody("wss://jk8s.example.com/workspaces/team-a/ws1/ssh-ws"))

	_, err := CreateConnection(context.Background(), op.RestConfig("kube-token"), "team-a", "ws1")
	if err == nil || !strings.Contains(err.Error(), "no token") {
		t.Errorf("error = %v, want a complaint about the missing token", err)
	}
}
