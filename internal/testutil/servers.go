/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package testutil

import (
	"net"
	"testing"
)

// StartSilentListener accepts TCP connections and never answers on them, standing
// in for a server whose handshake hangs. It returns the listener's address.
func StartSilentListener(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	t.Cleanup(func() {
		close(done)
		_ = listener.Close()
	})
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				<-done
				_ = conn.Close()
			}()
		}
	}()
	return listener.Addr().String()
}
