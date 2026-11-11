/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package tunnel

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/jupyter-infra/kubectl-workspace/internal/testutil"
)

const testPath = "/workspaces/ns/ws/ssh-ws"

func TestDialSendsTheTokenInAHeaderOnly(t *testing.T) {
	echo := testutil.NewEchoServer(t)

	ws, err := Dial(context.Background(), echo.URL(testPath), DialOptions{Token: "secret-token", UserAgent: "kubectl-workspace/test"})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	handshake := echo.LastHandshake(t)
	if handshake.Authorization != "Bearer secret-token" {
		t.Errorf("Authorization = %q", handshake.Authorization)
	}
	if handshake.RawQuery != "" {
		t.Errorf("query = %q, want the token kept out of the URL", handshake.RawQuery)
	}
	if handshake.UserAgent != "kubectl-workspace/test" {
		t.Errorf("User-Agent = %q", handshake.UserAgent)
	}
}

func TestDialTrustsSystemRootsOnly(t *testing.T) {
	server := httptest.NewTLSServer(http.NotFoundHandler())
	defer server.Close()
	u, _ := url.Parse("wss" + strings.TrimPrefix(server.URL, "https") + testPath)

	_, err := Dial(context.Background(), u, DialOptions{Token: "t"})
	var verification *tls.CertificateVerificationError
	if !errors.As(err, &verification) {
		t.Errorf("error = %v, want the self-signed certificate to fail verification", err)
	}
}

func TestDialHonorsTheProxyEnvironment(t *testing.T) {
	echo := testutil.NewEchoServer(t)
	proxy := testutil.NewConnectProxy(t, echo.URL("/").Host)
	previous := proxyFromEnvironment
	proxyFromEnvironment = func(*http.Request) (*url.URL, error) { return proxy.URL(), nil }
	t.Cleanup(func() { proxyFromEnvironment = previous })

	u, _ := url.Parse("ws://workspace.example.test:8443" + testPath)
	ws, err := Dial(context.Background(), u, DialOptions{Token: "secret-token"})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	if targets := proxy.Targets(); len(targets) != 1 || targets[0] != "workspace.example.test:8443" {
		t.Errorf("CONNECT targets = %q, want the workspace host once", targets)
	}
	if handshake := echo.LastHandshake(t); handshake.Authorization != "Bearer secret-token" {
		t.Errorf("Authorization through the proxy = %q", handshake.Authorization)
	}
}

func TestDialReportsARefusedHandshake(t *testing.T) {
	tests := []struct {
		status  int
		body    string
		wantErr string
	}{
		{status: http.StatusUnauthorized, body: "Invalid token", wantErr: "rejected the connection token (HTTP 401): Invalid token"},
		{status: http.StatusTooManyRequests, body: "Too many concurrent connections", wantErr: "too many open connections"},
		{status: http.StatusBadGateway, body: "", wantErr: "refused the WebSocket (HTTP 502)"},
	}
	for _, tt := range tests {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, tt.body, tt.status)
			}))
			defer server.Close()
			u, _ := url.Parse("ws" + strings.TrimPrefix(server.URL, "http") + "/")

			_, err := Dial(context.Background(), u, DialOptions{Token: "t"})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

func TestDialReturnsWhenCancelledDuringTheHandshake(t *testing.T) {
	u, _ := url.Parse("ws://" + testutil.StartSilentListener(t) + testPath)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)

	start := time.Now()
	_, err := Dial(ctx, u, DialOptions{Token: "t"})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Dial returned %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Dial returned after %s, want it to end with the cancel and not the handshake timeout", elapsed)
	}
}

func TestDialReportsAnUnreachableServer(t *testing.T) {
	u, _ := url.Parse("ws://127.0.0.1:1/")
	_, err := Dial(context.Background(), u, DialOptions{})
	if err == nil || !strings.Contains(err.Error(), "failed to connect to ws://127.0.0.1:1/") {
		t.Errorf("error = %v", err)
	}
}

// relayHarness runs Relay against a server with pipes standing in for ssh's
// stdin and stdout.
type relayHarness struct {
	stdin  *io.PipeWriter
	stdout *io.PipeReader
	done   chan error
	cancel context.CancelFunc
}

func startRelay(t *testing.T, echo *testutil.EchoServer) *relayHarness {
	t.Helper()
	ws, err := Dial(context.Background(), echo.URL(testPath), DialOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close() })

	inReader, inWriter := io.Pipe()
	outReader, outWriter := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h := &relayHarness{stdin: inWriter, stdout: outReader, done: make(chan error, 1), cancel: cancel}
	go func() {
		err := Relay(ctx, ws, inReader, outWriter)
		_ = outWriter.Close()
		h.done <- err
	}()
	return h
}

func (h *relayHarness) wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-h.done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("Relay did not return")
		return nil
	}
}

func TestRelayCarriesBytesBothWaysAndEndsOnStdinEOF(t *testing.T) {
	h := startRelay(t, testutil.NewEchoServer(t))

	payload := make([]byte, 3*chunkSize+17)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = h.stdin.Write(payload) }()
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(h.stdout, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Error("echoed bytes differ from what was sent")
	}

	// EOF on stdin is ssh exiting.
	_ = h.stdin.Close()
	if err := h.wait(t); err != nil {
		t.Errorf("Relay returned %v after stdin closed, want nil", err)
	}
	if rest, _ := io.ReadAll(h.stdout); len(rest) != 0 {
		t.Errorf("%d unexpected bytes after EOF", len(rest))
	}
}

func TestRelayReportsAStdinFailureAtOnce(t *testing.T) {
	h := startRelay(t, testutil.NewEchoServer(t))

	start := time.Now()
	_ = h.stdin.CloseWithError(errors.New("boom"))
	err := h.wait(t)
	if err == nil || !strings.Contains(err.Error(), "failed to read from stdin: boom") {
		t.Errorf("Relay returned %v, want the stdin failure", err)
	}
	if elapsed := time.Since(start); elapsed >= closeWait {
		t.Errorf("Relay took %s to report the failure, want it before the %s close wait", elapsed, closeWait)
	}
}

func TestRelayEndsWhenStdoutIsGone(t *testing.T) {
	h := startRelay(t, testutil.NewEchoServer(t))
	_ = h.stdout.Close()

	go func() { _, _ = io.WriteString(h.stdin, "echo me") }()
	err := h.wait(t)
	if err == nil || !strings.Contains(err.Error(), "failed to write to stdout") {
		t.Errorf("Relay returned %v, want the stdout write failure", err)
	}
}

func TestRelayReturnsTheServersCloseReason(t *testing.T) {
	echo := testutil.NewEchoServer(t)
	echo.SetOnConn(testutil.CloseWithReason("session duration limit reached"))
	h := startRelay(t, echo)

	err := h.wait(t)
	reason, ok := CloseReason(err)
	if !ok || reason != "session duration limit reached" {
		t.Errorf("CloseReason(%v) = %q, %v", err, reason, ok)
	}
}

func TestRelayEndsQuietlyOnANormalCloseWithoutReason(t *testing.T) {
	echo := testutil.NewEchoServer(t)
	echo.SetOnConn(testutil.CloseWithReason(""))
	h := startRelay(t, echo)

	if err := h.wait(t); err != nil {
		t.Errorf("Relay returned %v, want nil", err)
	}
}

func TestRelayReportsACutConnection(t *testing.T) {
	tests := []struct {
		name string
		cut  func(t *testing.T, ws *websocket.Conn)
	}{
		{name: "closed without a close frame", cut: func(_ *testing.T, ws *websocket.Conn) {
			_ = ws.UnderlyingConn().Close()
		}},
		{name: "reset", cut: func(t *testing.T, ws *websocket.Conn) {
			// A zero linger turns the close into a TCP reset instead of a FIN.
			tcp, ok := ws.UnderlyingConn().(*net.TCPConn)
			if !ok {
				t.Errorf("the echo server's connection is a %T, want a TCP connection", ws.UnderlyingConn())
				return
			}
			_ = tcp.SetLinger(0)
			_ = tcp.Close()
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			echo := testutil.NewEchoServer(t)
			echo.SetOnConn(func(ws *websocket.Conn) { tt.cut(t, ws) })
			h := startRelay(t, echo)

			err := h.wait(t)
			if err == nil {
				t.Fatal("Relay returned nil for a connection cut without a close frame")
			}
			if _, ok := CloseReason(err); ok {
				t.Errorf("a cut connection must not read as a normal close: %v", err)
			}
			if !strings.Contains(err.Error(), "the connection to the workspace was cut") || strings.Contains(err.Error(), "1006") {
				t.Errorf("Relay returned %q, want the plain message without the close code", err)
			}
		})
	}
}

func TestRelayAnswersPings(t *testing.T) {
	echo := testutil.NewEchoServer(t)
	pong := make(chan struct{}, 1)
	echo.SetOnConn(func(ws *websocket.Conn) {
		ws.SetPongHandler(func(string) error {
			pong <- struct{}{}
			return nil
		})
		_ = ws.WriteControl(websocket.PingMessage, []byte("keepalive"), time.Now().Add(time.Second))
		for {
			if _, _, err := ws.ReadMessage(); err != nil {
				return
			}
		}
	})
	h := startRelay(t, echo)

	select {
	case <-pong:
	case <-time.After(5 * time.Second):
		t.Fatal("the server received no pong")
	}
	_ = h.stdin.Close()
	_ = h.wait(t)
}

func TestRelayClosesATunnelThatWentSilent(t *testing.T) {
	previous := readTimeout
	readTimeout = 300 * time.Millisecond
	t.Cleanup(func() { readTimeout = previous })

	echo := testutil.NewEchoServer(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	// The server neither pings nor sends, which is what a vanished far end looks like.
	echo.SetOnConn(func(*websocket.Conn) { <-release })
	h := startRelay(t, echo)

	err := h.wait(t)
	if err == nil || !strings.Contains(err.Error(), "closing the dead tunnel") {
		t.Errorf("Relay returned %v, want the dead-tunnel error", err)
	}
}

func TestRelayRejectsAFrameLargerThanTheReadLimit(t *testing.T) {
	echo := testutil.NewEchoServer(t)
	echo.SetOnConn(func(ws *websocket.Conn) {
		_ = ws.WriteMessage(websocket.BinaryMessage, make([]byte, readLimit+1))
		_, _, _ = ws.ReadMessage()
	})
	h := startRelay(t, echo)

	if err := h.wait(t); !errors.Is(err, websocket.ErrReadLimit) {
		t.Errorf("Relay returned %v, want the read limit error", err)
	}
}

func TestRelayStopsWhenTheContextIsCancelled(t *testing.T) {
	h := startRelay(t, testutil.NewEchoServer(t))

	h.cancel()
	if err := h.wait(t); !errors.Is(err, context.Canceled) {
		t.Errorf("Relay returned %v, want context.Canceled", err)
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "write timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func TestCloseResultKeepsAFailedPongApart(t *testing.T) {
	err := closeResult(fmt.Errorf("%w: %w", errPongFailed, timeoutError{}))
	if !errors.Is(err, errPongFailed) || strings.Contains(err.Error(), "dead tunnel") {
		t.Errorf("closeResult = %v, want the pong failure, not the silence message", err)
	}
	if err := closeResult(timeoutError{}); err == nil || !strings.Contains(err.Error(), "closing the dead tunnel") {
		t.Errorf("closeResult(timeout) = %v, want the silence message", err)
	}
}

func TestCloseReasonIgnoresOtherErrors(t *testing.T) {
	if _, ok := CloseReason(errors.New("boom")); ok {
		t.Error("a plain error is not a close reason")
	}
	abnormal := &websocket.CloseError{Code: websocket.CloseAbnormalClosure, Text: "unexpected EOF"}
	if _, ok := CloseReason(abnormal); ok {
		t.Error("an abnormal close is not a normal close reason")
	}
}

func TestDialTrustsTheGivenRootsOnly(t *testing.T) {
	echo := testutil.NewTLSEchoServer(t)
	ws, err := Dial(context.Background(), echo.URL(testPath), DialOptions{RootCAs: echo.CertPool()})
	if err != nil {
		t.Fatalf("Dial with the server's own root: %v", err)
	}
	_ = ws.Close()

	_, err = Dial(context.Background(), echo.URL(testPath), DialOptions{RootCAs: x509.NewCertPool()})
	var verification *tls.CertificateVerificationError
	if !errors.As(err, &verification) {
		t.Errorf("Dial with a pool lacking the server's root = %v, want a verification failure", err)
	}
}

func TestLoadRoots(t *testing.T) {
	echo := testutil.NewTLSEchoServer(t)
	dir := t.TempDir()
	bundle := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(bundle, echo.CAPEM(), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRoots(bundle); err != nil {
		t.Errorf("LoadRoots(bundle) = %v", err)
	}
	junk := filepath.Join(dir, "junk.pem")
	if err := os.WriteFile(junk, []byte("not a certificate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRoots(junk); err == nil || !strings.Contains(err.Error(), "no certificates") {
		t.Errorf("LoadRoots(junk) = %v, want the empty bundle named", err)
	}
	_, err := LoadRoots(filepath.Join(dir, "missing.pem"))
	if err == nil || !strings.Contains(err.Error(), "failed to read the CA file") {
		t.Errorf("LoadRoots(missing) = %v, want the unreadable file reported", err)
	}
}
