package main

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

func socksClient(t *testing.T, handler dialFunc) (net.Conn, <-chan struct{}) {
	t.Helper()
	server, client := net.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); defer server.Close(); serveSOCKS(server, handler) }()
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	t.Cleanup(func() {
		client.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("SOCKS handler did not close")
		}
	})
	return client, done
}
func socksHandshake(t *testing.T, c net.Conn) {
	t.Helper()
	_, _ = c.Write([]byte{5, 1, 0})
	var result [2]byte
	if _, err := io.ReadFull(c, result[:]); err != nil || result != [2]byte{5, 0} {
		t.Fatal("SOCKS negotiation failed")
	}
}
func TestSOCKSHostnameAndBidirectionalRelay(t *testing.T) {
	called := make(chan string, 1)
	c, _ := socksClient(t, func(ctx context.Context, network, address string) (net.Conn, error) {
		called <- address
		a, b := net.Pipe()
		go func() {
			defer b.Close()
			var payload [4]byte
			if _, err := io.ReadFull(b, payload[:]); err == nil {
				_, _ = b.Write(payload[:])
			}
		}()
		return a, nil
	})
	socksHandshake(t, c)
	_, _ = c.Write(append([]byte{5, 1, 0, 3, 12}, append([]byte("test.example"), 0, 80)...))
	var result [10]byte
	if _, err := io.ReadFull(c, result[:]); err != nil || result[1] != 0 {
		t.Fatalf("SOCKS connect failed %v", err)
	}
	if address := <-called; address != "test.example:80" {
		t.Fatalf("wrong target %s", address)
	}
	_, _ = c.Write([]byte("ping"))
	var response [4]byte
	if _, err := io.ReadFull(c, response[:]); err != nil || string(response[:]) != "ping" {
		t.Fatal("relay failed")
	}
}
func TestSOCKSDisconnectedAndUDPFailClosed(t *testing.T) {
	for _, command := range []byte{1, 3} {
		t.Run(itoa(int(command)), func(t *testing.T) {
			slot := &proxySlot{}
			c, _ := socksClient(t, slot.dial)
			socksHandshake(t, c)
			request := []byte{5, command, 0, 1, 8, 8, 8, 8, 0, 80}
			if command != 1 {
				request = request[:4]
			}
			_, _ = c.Write(request)
			var result [10]byte
			_, err := io.ReadFull(c, result[:])
			if err != nil || result[1] == 0 {
				t.Fatal("disconnected/unsupported request succeeded")
			}
		})
	}
}
