//go:build e2e

/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package native

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"
)

// startSSHServer listens on loopback and serves SSH sessions whose exec requests run
// three commands: "echo <text>", "exit <n>" and "cat". Any user is accepted without
// authentication; the client's handling of the entry is under test, not the server.
func startSSHServer(t *testing.T) string {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go serveConn(conn, config)
		}
	}()
	return listener.Addr().String()
}

func serveConn(conn net.Conn, config *ssh.ServerConfig) {
	defer conn.Close()
	server, channels, requests, err := ssh.NewServerConn(conn, config)
	if err != nil {
		return
	}
	defer func() { _ = server.Close() }()
	go ssh.DiscardRequests(requests)
	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "only sessions are served")
			continue
		}
		channel, channelRequests, err := newChannel.Accept()
		if err != nil {
			return
		}
		go serveSession(channel, channelRequests)
	}
}

// serveSession answers one session's requests; the exec request runs the command and
// ends the session with its exit status.
func serveSession(channel ssh.Channel, requests <-chan *ssh.Request) {
	defer func() { _ = channel.Close() }()
	for req := range requests {
		switch req.Type {
		case "exec":
			var payload struct{ Command string }
			if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
				_ = req.Reply(false, nil)
				return
			}
			_ = req.Reply(true, nil)
			status := runCommand(channel, payload.Command)
			_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
			return
		case "pty-req", "env", "window-change":
			_ = req.Reply(true, nil)
		default:
			_ = req.Reply(false, nil)
		}
	}
}

func runCommand(channel ssh.Channel, command string) uint32 {
	switch {
	case strings.HasPrefix(command, "echo "):
		_, _ = fmt.Fprintln(channel, strings.TrimPrefix(command, "echo "))
		return 0
	case strings.HasPrefix(command, "exit "):
		status, err := strconv.ParseUint(strings.TrimPrefix(command, "exit "), 10, 8)
		if err != nil {
			return 2
		}
		return uint32(status)
	case command == "cat":
		_, _ = io.Copy(channel, channel)
		return 0
	}
	_, _ = fmt.Fprintf(channel.Stderr(), "unknown command %q\n", command)
	return 127
}

// bridge relays a WebSocket's binary frames to the SSH server and its bytes back, the
// way the sidecar does between the ingress and the SSH server in the pod.
func bridge(ws *websocket.Conn, sshAddr string) {
	tcp, err := net.Dial("tcp", sshAddr)
	if err != nil {
		return
	}
	defer tcp.Close()
	done := make(chan struct{}, 2)
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				return
			}
			if _, err := tcp.Write(data); err != nil {
				return
			}
		}
	}()
	go func() {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, 32*1024)
		for {
			n, err := tcp.Read(buf)
			if n > 0 {
				if werr := ws.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				msg := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")
				_ = ws.WriteControl(websocket.CloseMessage, msg, time.Now().Add(time.Second))
				return
			}
		}
	}()
	<-done
	_ = tcp.Close()
	<-done
}
