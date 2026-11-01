package main

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

type dialFunc func(context.Context, string, string) (net.Conn, error)

func (s *proxySlot) dial(ctx context.Context, network, address string) (net.Conn, error) {
	s.mu.Lock()
	e := s.engine
	s.mu.Unlock()
	if e == nil {
		return nil, errors.New("tunnel is disconnected")
	}
	return e.DialContext(ctx, network, address)
}
func socksReply(c net.Conn, code byte) error {
	_, err := c.Write([]byte{5, code, 0, 1, 0, 0, 0, 0, 0, 0})
	return err
}
func serveSOCKS(c net.Conn, dial dialFunc) {
	_ = c.SetDeadline(time.Now().Add(15 * time.Second))
	var h [2]byte
	if _, err := io.ReadFull(c, h[:]); err != nil || h[0] != 5 || h[1] == 0 {
		return
	}
	methods := make([]byte, int(h[1]))
	if _, err := io.ReadFull(c, methods); err != nil {
		return
	}
	noAuth := false
	for _, m := range methods {
		if m == 0 {
			noAuth = true
		}
	}
	if !noAuth {
		_, _ = c.Write([]byte{5, 255})
		return
	}
	if _, err := c.Write([]byte{5, 0}); err != nil {
		return
	}
	var head [4]byte
	if _, err := io.ReadFull(c, head[:]); err != nil {
		return
	}
	if head[0] != 5 || head[2] != 0 {
		_ = socksReply(c, 1)
		return
	}
	if head[1] != 1 {
		_ = socksReply(c, 7)
		return
	}
	host := ""
	switch head[3] {
	case 1:
		var ip [4]byte
		if _, err := io.ReadFull(c, ip[:]); err != nil {
			return
		}
		host = net.IP(ip[:]).String()
	case 4:
		var ip [16]byte
		if _, err := io.ReadFull(c, ip[:]); err != nil {
			return
		}
		host = net.IP(ip[:]).String()
	case 3:
		var n [1]byte
		if _, err := io.ReadFull(c, n[:]); err != nil || n[0] == 0 {
			return
		}
		name := make([]byte, n[0])
		if _, err := io.ReadFull(c, name); err != nil {
			return
		}
		host = string(name)
		if strings.ContainsAny(host, "\x00\r\n\t /\\:") {
			_ = socksReply(c, 8)
			return
		}
	default:
		_ = socksReply(c, 8)
		return
	}
	var p [2]byte
	if _, err := io.ReadFull(c, p[:]); err != nil {
		return
	}
	port := binary.BigEndian.Uint16(p[:])
	if port == 0 {
		_ = socksReply(c, 1)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	remote, err := dial(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(int(port))))
	if err != nil {
		_ = socksReply(c, 4)
		return
	}
	defer remote.Close()
	if err := socksReply(c, 0); err != nil {
		return
	}
	_ = c.SetDeadline(time.Time{})
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(remote, c)
		if tcp, ok := remote.(interface{ CloseWrite() error }); ok {
			_ = tcp.CloseWrite()
		}
		close(done)
	}()
	_, _ = io.Copy(c, remote)
	_ = c.Close()
	_ = remote.Close()
	<-done
}
