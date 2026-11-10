/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package testutil

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
)

// ConnectProxy is an HTTP proxy that answers CONNECT by tunnelling to one fixed
// target, whatever host the client asked for, and records what it asked for.
type ConnectProxy struct {
	server *httptest.Server
	target string

	mu      sync.Mutex
	targets []string
}

// NewConnectProxy starts a proxy that tunnels every CONNECT to target (host:port).
func NewConnectProxy(t *testing.T, target string) *ConnectProxy {
	t.Helper()
	p := &ConnectProxy{target: target}
	p.server = httptest.NewServer(p)
	t.Cleanup(p.server.Close)
	return p
}

// ServeHTTP tunnels a CONNECT request to the fixed target.
func (p *ConnectProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		http.Error(w, "CONNECT only", http.StatusMethodNotAllowed)
		return
	}
	p.mu.Lock()
	p.targets = append(p.targets, r.Host)
	p.mu.Unlock()

	upstream, err := net.Dial("tcp", p.target)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer upstream.Close()
	client, _, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return
	}
	defer client.Close()
	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, client); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, upstream); done <- struct{}{} }()
	<-done
}

// URL is the proxy's address for HTTPS_PROXY-style configuration.
func (p *ConnectProxy) URL() *url.URL {
	u, err := url.Parse(p.server.URL)
	if err != nil {
		panic(err)
	}
	return u
}

// Targets returns the host:port of every CONNECT received.
func (p *ConnectProxy) Targets() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.targets...)
}
