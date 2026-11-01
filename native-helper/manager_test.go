package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

type memoryStore struct {
	db  *database
	err error
}

func (s *memoryStore) Load() (*database, error) {
	if s.db == nil {
		s.db = newDatabase()
	}
	return s.db, nil
}
func (s *memoryStore) Save(db *database) error {
	if s.err != nil {
		return s.err
	}
	s.db = db
	return nil
}

type fakeEngine struct {
	closed    bool
	handshake time.Time
}

func (e *fakeEngine) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("blocked")
}
func (e *fakeEngine) LastHandshake() time.Time { return e.handshake }
func (e *fakeEngine) Close()                   { e.closed = true }
func newTestManager(t *testing.T) (*manager, *memoryStore) {
	t.Helper()
	s := &memoryStore{}
	m, err := newManager(s)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m, s
}
func imported(t *testing.T, m *manager) ProfileStatus {
	t.Helper()
	r := m.Handle(Request{ID: "import", Command: "import", Config: sampleConfig()})
	if !r.OK {
		t.Fatalf("import: %v", r.Error)
	}
	return r.Result.(ProfileStatus)
}
func TestLifecyclePreservesPortAndDoesNotLeakSecrets(t *testing.T) {
	m, s := newTestManager(t)
	p := imported(t, m)
	e := &fakeEngine{}
	m.factory = func(*Config) (engine, error) { return e, nil }
	r := m.Handle(Request{ID: "connect", Command: "connect", ConfigID: p.ConfigID})
	if !r.OK || r.Result.(ProfileStatus).State != "connecting" {
		t.Fatalf("connect state: %#v", r)
	}
	e.handshake = time.Now()
	r = m.Handle(Request{ID: "status", Command: "status", ConfigID: p.ConfigID})
	if r.Result.(ProfileStatus).State != "connected" {
		t.Fatal("handshake did not determine state")
	}
	raw, _ := json.Marshal(r)
	if strings.Contains(string(raw), "123456789") || strings.Contains(string(raw), "private_key") {
		t.Fatal("response leaked a key")
	}
	m.Handle(Request{ID: "disconnect", Command: "disconnect", ConfigID: p.ConfigID})
	if !e.closed {
		t.Fatal("disconnect did not close engine")
	}
	r = m.Handle(Request{ID: "replace", Command: "import", ConfigID: p.ConfigID, Config: sampleConfig()})
	if !r.OK || r.Result.(ProfileStatus).Port != p.Port {
		t.Fatal("update changed stable port")
	}
	m.Close()
	m2, err := newManager(s)
	if err != nil {
		t.Fatal(err)
	}
	defer m2.Close()
	r = m2.Handle(Request{ID: "status", Command: "status", ConfigID: p.ConfigID})
	if r.Result.(ProfileStatus).Port != p.Port {
		t.Fatal("restart changed stable port")
	}
	m2.Handle(Request{ID: "delete", Command: "delete", ConfigID: p.ConfigID})
	next := imported(t, m2)
	if next.Port <= p.Port {
		t.Fatal("deleted profile port was reused")
	}
}
func TestImportStorageFailureDoesNotDestroyExisting(t *testing.T) {
	m, s := newTestManager(t)
	p := imported(t, m)
	s.err = errors.New("storage unavailable")
	r := m.Handle(Request{ID: "replace", Command: "import", ConfigID: p.ConfigID, Config: sampleConfig()})
	if r.OK {
		t.Fatal("update succeeded despite failed persistence")
	}
	if _, ok := m.db.Profiles[p.ConfigID]; !ok {
		t.Fatal("previous profile lost")
	}
}
func TestMissingConfigAndPortConflictFailClosed(t *testing.T) {
	m, _ := newTestManager(t)
	r := m.Handle(Request{ID: "missing", Command: "connect", ConfigID: strings.Repeat("a", 32)})
	if r.OK || r.Error.Code != "config_not_found" {
		t.Fatal("missing configuration did not fail")
	}
	p := imported(t, m)
	slot := m.slots[p.ConfigID]
	slot.close()
	delete(m.slots, p.ConfigID)
	conflict, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", itoa(p.Port)))
	if err != nil {
		t.Fatal(err)
	}
	defer conflict.Close()
	r = m.Handle(Request{ID: "conflict", Command: "connect", ConfigID: p.ConfigID})
	if r.OK || r.Error.Code != "port_in_use" {
		t.Fatal("port collision did not fail closed")
	}
}
func itoa(n int) string {
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = digits[n%10]
		n /= 10
	}
	return string(b[i:])
}
