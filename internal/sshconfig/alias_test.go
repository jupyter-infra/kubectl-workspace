/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package sshconfig

import (
	"strings"
	"testing"
)

func TestParseAlias(t *testing.T) {
	tests := []struct {
		name    string
		host    string
		want    Alias
		wantErr string
	}{
		{name: "plain", host: "ws1.team-a.prod.jk8s", want: Alias{"ws1", "team-a", "prod"}},
		{name: "dotted workspace name", host: "my.ws.team-a.prod.jk8s", want: Alias{"my.ws", "team-a", "prod"}},
		{name: "mixed case and trailing dot", host: "WS1.Team-A.Prod.jk8s.", want: Alias{"ws1", "team-a", "prod"}},
		{name: "too few labels", host: "ws1.prod.jk8s", wantErr: "is not a workspace alias"},
		{name: "wrong suffix", host: "ws1.team-a.prod.example", wantErr: "is not a workspace alias"},
		{name: "underscore in namespace", host: "ws1.team_a.prod.jk8s", wantErr: "invalid namespace"},
		{name: "empty workspace", host: ".team-a.prod.jk8s", wantErr: "invalid workspace name"},
		{name: "empty", host: "", wantErr: "is not a workspace alias"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseAlias(tt.host)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ParseAlias(%q) = %v, %v; want an error mentioning %q", tt.host, got, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseAlias(%q): %v", tt.host, err)
			}
			if got != tt.want {
				t.Errorf("ParseAlias(%q) = %+v, want %+v", tt.host, got, tt.want)
			}
		})
	}
}

func TestAliasRoundTrip(t *testing.T) {
	alias, err := NewAlias("my.ws", "team-a", "prod")
	if err != nil {
		t.Fatal(err)
	}
	if alias.String() != "my.ws.team-a.prod.jk8s" {
		t.Fatalf("String() = %q", alias.String())
	}
	parsed, err := ParseAlias(alias.String())
	if err != nil {
		t.Fatal(err)
	}
	if parsed != alias {
		t.Errorf("round trip changed the alias: %+v -> %+v", alias, parsed)
	}
}

func TestNewAliasRejectsInvalidNames(t *testing.T) {
	tests := []struct {
		name                          string
		workspace, namespace, cluster string
		wantErr                       string
	}{
		{name: "uppercase workspace", workspace: "Ws", namespace: "ns", cluster: "c", wantErr: "workspace"},
		{name: "dotted namespace", workspace: "ws", namespace: "a.b", cluster: "c", wantErr: "namespace"},
		{name: "cluster too long", workspace: "ws", namespace: "ns", cluster: strings.Repeat("a", 64), wantErr: "cluster"},
		{name: "cluster with dot", workspace: "ws", namespace: "ns", cluster: "a.b", wantErr: "cluster"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewAlias(tt.workspace, tt.namespace, tt.cluster)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("NewAlias error = %v, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}
