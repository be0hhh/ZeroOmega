package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/net/dns/dnsmessage"
)

func keypair(t *testing.T) (string, string) {
	t.Helper()
	private := make([]byte, 32)
	if _, err := rand.Read(private); err != nil {
		t.Fatal(err)
	}
	public, err := curve25519.X25519(private, curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(private), base64.StdEncoding.EncodeToString(public)
}
func udpPort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := c.LocalAddr().(*net.UDPAddr).Port
	c.Close()
	return port
}
func TestEncryptedTunnelToLocalPeer(t *testing.T) {
	for _, protocol := range []string{"wireguard", "amneziawg"} {
		t.Run(protocol, func(t *testing.T) {
			serverPrivate, serverPublic := keypair(t)
			clientPrivate, clientPublic := keypair(t)
			port := udpPort(t)
			extra := ""
			if protocol == "amneziawg" {
				extra = "Jc = 1\nJmin = 40\nJmax = 70\nS1 = 21\nS2 = 35\nH1 = 111\nH2 = 222\nH3 = 333\nH4 = 444\n"
			}
			config := func(private, addr, peer, endpoint, allowed string, listen int) *Config {
				raw := "[Interface]\nPrivateKey = " + private + "\nAddress = " + addr + "/32\nDNS = 10.20.0.1\n" + extra
				if listen > 0 {
					raw += "ListenPort = " + itoa(listen) + "\n"
				}
				raw += "[Peer]\nPublicKey = " + peer + "\nEndpoint = " + endpoint + "\nAllowedIPs = " + allowed + "\n"
				c, err := parseConfig(raw)
				if err != nil {
					t.Fatal(err)
				}
				return c
			}
			// Server includes its own DNS address in AllowedIPs solely to satisfy the
			// common config validator. Client requests are routed to the client peer.
			serverCfg := config(serverPrivate, "10.20.0.1", clientPublic, "127.0.0.1:9", "10.20.0.0/24", port)
			serverEngine, err := startEngine(serverCfg)
			if err != nil {
				t.Fatal(err)
			}
			defer serverEngine.Close()
			server := serverEngine.(*tunnel)
			listener, err := server.stack.ListenTCPAddrPort(netip.MustParseAddrPort("10.20.0.1:8080"))
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			go func() {
				c, err := listener.Accept()
				if err != nil {
					return
				}
				defer c.Close()
				payload := make([]byte, 4)
				if _, err := io.ReadFull(c, payload); err == nil {
					_, _ = c.Write(payload)
				}
			}()
			dns, err := server.stack.ListenUDPAddrPort(netip.MustParseAddrPort("10.20.0.1:53"))
			if err != nil {
				t.Fatal(err)
			}
			defer dns.Close()
			dnsSeen := make(chan struct{}, 1)
			go func() {
				var packet [1500]byte
				for {
					n, from, err := dns.ReadFrom(packet[:])
					if err != nil {
						return
					}
					var message dnsmessage.Message
					if message.Unpack(packet[:n]) != nil || len(message.Questions) == 0 {
						continue
					}
					question := message.Questions[0]
					message.Header.Response = true
					message.Header.RecursionAvailable = true
					message.Answers = nil
					if question.Type == dnsmessage.TypeA {
						message.Answers = []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: question.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 30}, Body: &dnsmessage.AResource{A: [4]byte{10, 20, 0, 1}}}}
					}
					response, err := message.Pack()
					if err == nil {
						_, _ = dns.WriteTo(response, from)
						select {
						case dnsSeen <- struct{}{}:
						default:
						}
					}
				}
			}()
			clientEngine, err := startEngine(config(clientPrivate, "10.20.0.2", serverPublic, net.JoinHostPort("127.0.0.1", itoa(port)), "10.20.0.1/32", 0))
			if err != nil {
				t.Fatal(err)
			}
			defer clientEngine.Close()
			client := clientEngine.(*tunnel)
			if !client.LastHandshake().IsZero() {
				t.Fatal("reported handshake before exchanging packets")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			conn, err := client.DialContext(ctx, "tcp", "echo.example:8080")
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			_, _ = conn.Write([]byte("ping"))
			var echo [4]byte
			if _, err := io.ReadFull(conn, echo[:]); err != nil || string(echo[:]) != "ping" {
				t.Fatalf("encrypted echo failed: %v", err)
			}
			select {
			case <-dnsSeen:
			default:
				t.Fatal("hostname was not resolved through tunnel")
			}
			if client.LastHandshake().IsZero() {
				t.Fatal("successful authenticated handshake not detected")
			}
			if _, err := client.DialContext(ctx, "tcp", "192.0.2.1:80"); err == nil || !strings.Contains(err.Error(), "AllowedIPs") {
				t.Fatal("unrouted destination was not rejected")
			}
		})
	}
}

func TestAddressFallbackDoesNotWaitForBlackholedIPv6(t *testing.T) {
	addresses := []netip.Addr{netip.MustParseAddr("2001:db8::1"), netip.MustParseAddr("192.0.2.1")}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	peer, remote := net.Pipe()
	defer remote.Close()
	started := time.Now()
	conn, err := dialCandidates(ctx, addresses, "80", func(ctx context.Context, network, address string) (net.Conn, error) {
		if strings.HasPrefix(address, "[") {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return peer, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if time.Since(started) > time.Second {
		t.Fatal("IPv6 blackhole exhausted deadline before IPv4 fallback")
	}
}
func TestAWGCoreValidationRejectsIncompatibleHeaderProtection(t *testing.T) {
	raw := strings.Replace(sampleConfig(), "Address =", "HeaderProtectionKey = "+base64.StdEncoding.EncodeToString([]byte("12345678901234567890123456789012"))+"\nS1=1\nS2=1\nS3=1\nS4=1\nAddress =", 1)
	c, err := parseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCoreConfig(c); err == nil {
		t.Fatal("incompatible header protection padding accepted")
	}
}
