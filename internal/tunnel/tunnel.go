/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

// Package tunnel opens the WebSocket to the ingress and relays the SSH byte
// stream between it and the streams OpenSSH gives a ProxyCommand.
package tunnel

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const (
	handshakeTimeout = 15 * time.Second

	// writeWait bounds one WebSocket write.
	writeWait = 10 * time.Second
	// maxErrorDetail bounds how much of a refusing server's body reaches the user.
	maxErrorDetail = 512

	// closeWait is how long to wait for the server's close frame after sending ours.
	closeWait = 2 * time.Second

	// chunkSize matches the sidecar's TCP read buffer.
	chunkSize = 32 * 1024

	// readLimit is the largest frame accepted from the sidecar, which sends at most
	// chunkSize per frame; the margin tolerates a sidecar built with a larger buffer.
	readLimit = 1024 * 1024
)

// errWriteFailed marks a frame the WebSocket refused; when the server closed first,
// its close frame says why.
var errWriteFailed = errors.New("failed to write to the workspace")

// errPongFailed marks a ping that could not be answered, which would otherwise
// surface as a read deadline and read as silence from the server.
var errPongFailed = errors.New("failed to answer the sidecar's ping")

// readTimeout is three of the sidecar's 30-second ping intervals. Every frame
// and ping extends it, so a tunnel whose far end vanished closes after three
// missed pings instead of waiting for the kernel to give up the TCP connection.
var readTimeout = 90 * time.Second

// proxyFromEnvironment honors HTTPS_PROXY and NO_PROXY for the dial; a variable so
// tests can route through a proxy of their own.
var proxyFromEnvironment = http.ProxyFromEnvironment

// DialOptions is what the dial sends besides the URL, and what it verifies the
// server against.
type DialOptions struct {
	// Token travels in the Authorization header.
	Token     string
	UserAgent string
	// RootCAs, when set, is trusted for the TLS handshake instead of the system
	// roots, for an ingress behind a private certificate authority.
	RootCAs *x509.CertPool
}

// LoadRoots reads a PEM bundle into a certificate pool; an empty path returns nil,
// which keeps the system roots.
func LoadRoots(path string) (*x509.CertPool, error) {
	if path == "" {
		return nil, nil
	}
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read the CA file: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("no certificates in the CA file %s", path)
	}
	return pool, nil
}

// Dial opens the WebSocket with the token in the Authorization header.
func Dial(ctx context.Context, u *url.URL, opts DialOptions) (*websocket.Conn, error) {
	var stopWatch func() bool
	dialer := websocket.Dialer{
		Proxy:            proxyFromEnvironment,
		HandshakeTimeout: handshakeTimeout,
		// A client writes a message larger than its buffer as several frames, so the
		// buffer matches the chunk and one stdin read is one frame.
		ReadBufferSize:  chunkSize,
		WriteBufferSize: chunkSize,
		NetDialContext: func(dialCtx context.Context, network, addr string) (net.Conn, error) {
			conn, err := (&net.Dialer{}).DialContext(dialCtx, network, addr)
			if err != nil {
				return nil, err
			}
			// The library stops watching ctx once the TCP connection is up, so a
			// cancelled handshake would otherwise wait out HandshakeTimeout.
			stopWatch = context.AfterFunc(ctx, func() { _ = conn.Close() })
			return conn, nil
		},
	}
	if opts.RootCAs != nil {
		dialer.TLSClientConfig = &tls.Config{RootCAs: opts.RootCAs, MinVersion: tls.VersionTLS12}
	}
	header := http.Header{}
	if opts.Token != "" {
		header.Set("Authorization", "Bearer "+opts.Token)
	}
	if opts.UserAgent != "" {
		header.Set("User-Agent", opts.UserAgent)
	}
	ws, resp, err := dialer.DialContext(ctx, u.String(), header)
	if stopWatch != nil {
		stopWatch()
	}
	if ctx.Err() != nil {
		if ws != nil {
			_ = ws.Close()
		}
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, dialError(u, resp, err)
	}
	return ws, nil
}

// dialError reports a refused handshake with the server's status and body, which
// is where the ingress and the sidecar say why.
func dialError(u *url.URL, resp *http.Response, err error) error {
	if resp == nil {
		return fmt.Errorf("failed to connect to %s: %w", u.Redacted(), err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorDetail))
	detail := strings.Join(strings.Fields(string(body)), " ")
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%s rejected the connection token (HTTP %d): %s", u.Host, resp.StatusCode, detail)
	case http.StatusTooManyRequests:
		return fmt.Errorf("the workspace has too many open connections (HTTP 429): %s", detail)
	default:
		return fmt.Errorf("%s refused the WebSocket (HTTP %d): %s", u.Host, resp.StatusCode, detail)
	}
}

// Relay copies in to the WebSocket as binary frames and the WebSocket's frames to
// out until the SSH client closes in or the server closes the WebSocket. A normal
// close by the server returns nil, or a *websocket.CloseError when the server gave
// a reason; see CloseReason.
func Relay(ctx context.Context, ws *websocket.Conn, in io.Reader, out io.Writer) error {
	ws.SetReadLimit(readLimit)
	extendReadDeadline(ws)
	ws.SetPingHandler(func(data string) error {
		extendReadDeadline(ws)
		err := writeControl(ws, websocket.PongMessage, []byte(data))
		if err == nil || errors.Is(err, websocket.ErrCloseSent) {
			return nil
		}
		return fmt.Errorf("%w: %w", errPongFailed, err)
	})

	downErr := make(chan error, 1)
	go func() { downErr <- downstream(ws, out) }()
	upErr := make(chan error, 1)
	go func() { upErr <- upstream(ws, in) }()

	select {
	case err := <-downErr:
		return closeResult(err)
	case err := <-upErr:
		if err != nil && !errors.Is(err, errWriteFailed) {
			return err
		}
		// After the close frame the EOF path sent, or a write the server refused
		// because it closed first, the server's own close frame is the outcome.
		select {
		case derr := <-downErr:
			return closeResult(derr)
		case <-time.After(closeWait):
			return err
		}
	case <-ctx.Done():
		msg := websocket.FormatCloseMessage(websocket.CloseGoingAway, "")
		_ = writeControl(ws, websocket.CloseMessage, msg)
		return ctx.Err()
	}
}

// CloseReason returns the reason the server gave when it closed the session
// normally, so the caller can show it without depending on the WebSocket library.
func CloseReason(err error) (string, bool) {
	var closeErr *websocket.CloseError
	if errors.As(err, &closeErr) && isNormalClose(closeErr.Code) {
		return closeErr.Text, true
	}
	return "", false
}

// upstream copies in to the WebSocket. EOF on in is the SSH client exiting, which
// is reported to the server with a close frame.
func upstream(ws *websocket.Conn, in io.Reader) error {
	buf := make([]byte, chunkSize)
	for {
		n, err := in.Read(buf)
		if n > 0 {
			_ = ws.SetWriteDeadline(time.Now().Add(writeWait))
			if werr := ws.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
				return fmt.Errorf("%w: %w", errWriteFailed, werr)
			}
		}
		if err == nil {
			continue
		}
		if errors.Is(err, io.EOF) {
			msg := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")
			_ = writeControl(ws, websocket.CloseMessage, msg)
			return nil
		}
		return fmt.Errorf("failed to read from stdin: %w", err)
	}
}

// downstream copies binary frames to out until the WebSocket ends, through one
// buffer: ReadMessage would allocate and regrow a slice for every frame.
func downstream(ws *websocket.Conn, out io.Writer) error {
	buf := make([]byte, chunkSize)
	for {
		messageType, frame, err := ws.NextReader()
		if err != nil {
			return err
		}
		extendReadDeadline(ws)
		if messageType != websocket.BinaryMessage {
			continue
		}
		if err := copyFrame(out, frame, buf); err != nil {
			return err
		}
	}
}

// copyFrame writes one frame to out as it arrives.
func copyFrame(out io.Writer, frame io.Reader, buf []byte) error {
	for {
		n, err := frame.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				return fmt.Errorf("failed to write to stdout: %w", werr)
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// extendReadDeadline gives the server another readTimeout to show life.
func extendReadDeadline(ws *websocket.Conn) {
	_ = ws.SetReadDeadline(time.Now().Add(readTimeout))
}

// writeControl sends a control frame; the library allows it alongside the data
// writer, so no lock of our own is needed.
func writeControl(ws *websocket.Conn, messageType int, data []byte) error {
	return ws.WriteControl(messageType, data, time.Now().Add(writeWait))
}

// closeResult maps the error that ended the read loop. A normal close frame means
// the session ended as intended; a read deadline means readTimeout of silence.
func closeResult(err error) error {
	if errors.Is(err, errPongFailed) {
		return err
	}
	var closeErr *websocket.CloseError
	if errors.As(err, &closeErr) && isNormalClose(closeErr.Code) {
		if closeErr.Text == "" {
			return nil
		}
		return closeErr
	}
	// The sidecar closes the socket without a close frame when the SSH server
	// hangs up first, which the library reports as close code 1006.
	if (closeErr != nil && closeErr.Code == websocket.CloseAbnormalClosure) ||
		errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, errConnReset) {
		return errors.New("the connection to the workspace was cut")
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return fmt.Errorf("nothing received from the workspace for %s; closing the dead tunnel", readTimeout)
	}
	return err
}

func isNormalClose(code int) bool {
	return code == websocket.CloseNormalClosure || code == websocket.CloseGoingAway
}
