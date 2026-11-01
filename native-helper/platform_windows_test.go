//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
)

func TestDPAPIRoundTripAndProtectedStore(t *testing.T) {
	secret := []byte("test-only private configuration material")
	encrypted, err := protect(secret)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted, secret) {
		t.Fatal("DPAPI output contains plaintext")
	}
	decrypted, err := unprotect(encrypted)
	if err != nil || !bytes.Equal(decrypted, secret) {
		t.Fatalf("DPAPI roundtrip failed: %v", err)
	}
	db := newDatabase()
	db.NextPort = 24001
	db.Profiles["00000000000000000000000000000001"] = storedProfile{ID: "00000000000000000000000000000001", Port: 24000, Raw: sampleConfig()}
	s := protectedStore{path: filepath.Join(t.TempDir(), "configs", "store.dpapi")}
	if err := s.Save(db); err != nil {
		t.Fatal(err)
	}
	disk, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(disk, []byte("PrivateKey")) || bytes.Contains(disk, []byte(sampleConfig())) {
		t.Fatal("store contains plaintext secrets")
	}
	loaded, err := s.Load()
	if err != nil || loaded.Profiles["00000000000000000000000000000001"].Raw != sampleConfig() {
		t.Fatalf("store did not recover: %v", err)
	}
}
func TestWindowsBrokerSharedHostsAndRestart(t *testing.T) {
	sid, err := currentSID()
	if err != nil {
		t.Fatal(err)
	}
	name := `\\.\pipe\zeroomega-wg-test-` + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	path := filepath.Join(t.TempDir(), "store.dpapi")
	launch := func() <-chan error {
		done := make(chan error, 1)
		go func() { done <- runBrokerAt(name, sid, path) }()
		return done
	}
	dial := func() net.Conn {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			c, err := winio.DialPipeContext(ctx, name)
			cancel()
			if err == nil {
				if _, err := verifyServer(c, sid); err != nil {
					c.Close()
					t.Fatal(err)
				}
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				return c
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("broker pipe unavailable")
		return nil
	}
	request := func(c net.Conn, req Request) Response {
		t.Helper()
		if err := writeFrame(c, req); err != nil {
			t.Fatal(err)
		}
		var response Response
		if err := readFrame(c, &response); err != nil {
			t.Fatal(err)
		}
		if !response.OK {
			t.Fatalf("broker failure: %#v", response.Error)
		}
		return response
	}
	stop := func(c net.Conn, done <-chan error) {
		t.Helper()
		request(c, Request{ID: "stop", Command: "__shutdown"})
		c.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("broker shutdown hung")
		}
	}
	done := launch()
	first := dial()
	defer first.Close()
	if err := checkBrokerConnection(first); err != nil {
		t.Fatalf("running broker failed startup health check: %v", err)
	}
	second := dial()
	defer second.Close()
	if _, err := verifyServer(first, "S-1-5-18"); err == nil {
		t.Fatal("pipe authenticated to a different expected user")
	}
	response := request(first, Request{ID: "import", Command: "import", Config: sampleConfig()})
	raw, _ := json.Marshal(response.Result)
	var p ProfileStatus
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	response = request(second, Request{ID: "status", Command: "status", ConfigID: p.ConfigID})
	raw, _ = json.Marshal(response.Result)
	var status ProfileStatus
	_ = json.Unmarshal(raw, &status)
	if status.Port != p.Port || status.State != "disconnected" {
		t.Fatal("second native host did not share broker profile")
	}
	stop(first, done)
	second.Close()
	done = launch()
	restarted := dial()
	defer restarted.Close()
	response = request(restarted, Request{ID: "status", Command: "status", ConfigID: p.ConfigID})
	raw, _ = json.Marshal(response.Result)
	_ = json.Unmarshal(raw, &status)
	if status.Port != p.Port {
		t.Fatal("broker restart changed reserved port")
	}
	stop(restarted, done)
}

func TestBrokerHealthRejectsInvalidReplies(t *testing.T) {
	for _, response := range []Response{
		{ID: "health", OK: false},
		{ID: "wrong-request", OK: true, Result: map[string]int{"protocolVersion": protocolVersion}},
		{ID: "health", OK: true, Result: map[string]int{"protocolVersion": protocolVersion + 1}},
	} {
		client, server := net.Pipe()
		done := make(chan error, 1)
		go func(response Response) {
			defer server.Close()
			var request Request
			if err := readFrame(server, &request); err != nil {
				done <- err
				return
			}
			done <- writeFrame(server, response)
		}(response)
		err := checkBrokerConnection(client)
		client.Close()
		if err == nil {
			t.Fatal("invalid broker response reported healthy")
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}
