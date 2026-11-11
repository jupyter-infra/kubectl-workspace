/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package testutil

import (
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

// Handshake records the headers of one WebSocket handshake.
type Handshake struct {
	Authorization string
	RawQuery      string
	UserAgent     string
}

// EchoServer accepts WebSocket upgrades and echoes binary frames until the client
// closes, standing in for the sidecar in front of an SSH server that echoes.
type EchoServer struct {
	server *httptest.Server

	mu         sync.Mutex
	onConn     func(ws *websocket.Conn)
	handshakes []Handshake
}

// NewEchoServer starts an echo server.
func NewEchoServer(t *testing.T) *EchoServer {
	t.Helper()
	return newEchoServer(t, httptest.NewServer)
}

// NewTLSEchoServer starts an echo server behind TLS with a certificate of its own,
// standing in for an ingress whose certificate the system roots do not know.
func NewTLSEchoServer(t *testing.T) *EchoServer {
	t.Helper()
	return newEchoServer(t, httptest.NewTLSServer)
}

func newEchoServer(t *testing.T, start func(http.Handler) *httptest.Server) *EchoServer {
	t.Helper()
	e := &EchoServer{}
	e.server = start(e)
	t.Cleanup(e.server.Close)
	return e
}

// CertPool returns a pool that trusts a TLS echo server's certificate.
func (e *EchoServer) CertPool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(e.server.Certificate())
	return pool
}

// CAPEM returns a TLS echo server's certificate as a PEM bundle.
func (e *EchoServer) CAPEM() []byte {
	return []byte(certPEM(e.server))
}

// SetOnConn replaces the echo loop for connections accepted from now on.
func (e *EchoServer) SetOnConn(fn func(ws *websocket.Conn)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.onConn = fn
}

// ServeHTTP records the handshake, upgrades, and echoes or runs the hook.
func (e *EchoServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	e.handshakes = append(e.handshakes, Handshake{
		Authorization: r.Header.Get("Authorization"),
		RawQuery:      r.URL.RawQuery,
		UserAgent:     r.Header.Get("User-Agent"),
	})
	onConn := e.onConn
	e.mu.Unlock()

	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close()
	if onConn != nil {
		onConn(ws)
		return
	}
	for {
		messageType, data, err := ws.ReadMessage()
		if err != nil {
			return
		}
		if err := ws.WriteMessage(messageType, data); err != nil {
			return
		}
	}
}

// URL returns a ws:// URL on the server with the given path.
func (e *EchoServer) URL(path string) *url.URL {
	u, err := url.Parse("ws" + strings.TrimPrefix(e.server.URL, "http") + path)
	if err != nil {
		panic(err)
	}
	return u
}

// LastHandshake returns the most recent handshake and fails the test when there is none.
func (e *EchoServer) LastHandshake(t *testing.T) Handshake {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.handshakes) == 0 {
		t.Fatal("the echo server received no WebSocket handshake")
	}
	return e.handshakes[len(e.handshakes)-1]
}

// CloseWithReason is an OnConn hook that closes the session the way the sidecar
// does at its session duration limit, then waits for the client's reply.
func CloseWithReason(reason string) func(ws *websocket.Conn) {
	return func(ws *websocket.Conn) {
		msg := websocket.FormatCloseMessage(websocket.CloseNormalClosure, reason)
		_ = ws.WriteControl(websocket.CloseMessage, msg, time.Now().Add(time.Second))
		_, _, _ = ws.ReadMessage()
	}
}
