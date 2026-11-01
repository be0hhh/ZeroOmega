package main

import (
	"crypto/rand"
	"encoding/hex"
	"net"
	"strconv"
	"sync"
	"time"
)

type manager struct {
	mu      sync.Mutex
	storage store
	db      *database
	slots   map[string]*proxySlot
	factory func(*Config) (engine, error)
}

func newManager(s store) (*manager, error) {
	db, err := s.Load()
	if err != nil {
		return nil, err
	}
	m := &manager{storage: s, db: db, slots: map[string]*proxySlot{}, factory: startEngine}
	for id, p := range db.Profiles {
		slot, err := newProxySlot(p.Port)
		if err == nil {
			m.slots[id] = slot
		}
	}
	return m, nil
}
func validConfigID(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 16 && s == hex.EncodeToString(b)
}
func (m *manager) Handle(req Request) Response {
	m.mu.Lock()
	defer m.mu.Unlock()
	if req.ID == "" || len(req.ID) > 128 {
		return failure(req.ID, "invalid_request", "Request id is required (maximum 128 characters).")
	}
	if req.Command == "version" {
		return Response{ID: req.ID, OK: true, Result: map[string]any{"protocolVersion": protocolVersion, "version": helperVersion}}
	}
	if req.Command == "import" {
		return m.importConfig(req)
	}
	if !validConfigID(req.ConfigID) {
		return failure(req.ID, "invalid_config_id", "A valid configId is required.")
	}
	p, ok := m.db.Profiles[req.ConfigID]
	if !ok {
		return failure(req.ID, "config_not_found", "Configuration is missing on this computer. Import the .conf file again.")
	}
	switch req.Command {
	case "status":
		return Response{ID: req.ID, OK: true, Result: m.status(p)}
	case "connect":
		slot, err := m.ensureSlot(p)
		if err != nil {
			return failure(req.ID, "port_in_use", "Reserved local proxy port is unavailable. Close conflicting software and retry.")
		}
		if !slot.running() {
			cfg, err := parseConfig(p.Raw)
			if err != nil {
				return failure(req.ID, "invalid_config", "Stored configuration is unsupported. Import the .conf file again.")
			}
			e, err := m.factory(cfg)
			if err != nil {
				slot.setError(err.Error())
				return failure(req.ID, "connect_failed", err.Error())
			}
			slot.start(e)
		}
		return Response{ID: req.ID, OK: true, Result: m.status(p)}
	case "disconnect":
		if slot := m.slots[p.ID]; slot != nil {
			slot.stop()
		}
		return Response{ID: req.ID, OK: true, Result: m.status(p)}
	case "delete":
		delete(m.db.Profiles, p.ID)
		if err := m.storage.Save(m.db); err != nil {
			m.db.Profiles[p.ID] = p
			return failure(req.ID, "storage_failed", err.Error())
		}
		if slot := m.slots[p.ID]; slot != nil {
			slot.close()
			delete(m.slots, p.ID)
		}
		return Response{ID: req.ID, OK: true, Result: map[string]string{"configId": p.ID}}
	default:
		return failure(req.ID, "unknown_command", "Unsupported helper command.")
	}
}
func (m *manager) importConfig(req Request) Response {
	cfg, err := parseConfig(req.Config)
	if err != nil {
		return failure(req.ID, "invalid_config", err.Error())
	}
	if err := validateCoreConfig(cfg); err != nil {
		return failure(req.ID, "invalid_config", err.Error())
	}
	id := req.ConfigID
	var previous storedProfile
	var exists bool
	if id != "" {
		if !validConfigID(id) {
			return failure(req.ID, "invalid_config_id", "Invalid configId.")
		}
		previous, exists = m.db.Profiles[id]
		if !exists {
			return failure(req.ID, "config_not_found", "Configuration to update is missing. Create a new profile.")
		}
	}
	if !exists && len(m.db.Profiles) >= 128 {
		return failure(req.ID, "profile_limit", "Maximum of 128 tunnel profiles reached.")
	}
	port := previous.Port
	var newSlot *proxySlot
	if !exists {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return failure(req.ID, "random_failed", "Could not create profile identifier.")
		}
		id = hex.EncodeToString(b)
		for m.db.NextPort < 64000 {
			port = m.db.NextPort
			m.db.NextPort++
			newSlot, err = newProxySlot(port)
			if err == nil {
				break
			}
		}
		if newSlot == nil {
			return failure(req.ID, "ports_exhausted", "No unused local proxy port is available.")
		}
	}
	p := storedProfile{ID: id, Port: port, Raw: req.Config}
	m.db.Profiles[id] = p
	if err := m.storage.Save(m.db); err != nil {
		if exists {
			m.db.Profiles[id] = previous
		} else {
			delete(m.db.Profiles, id)
			newSlot.close()
		}
		return failure(req.ID, "storage_failed", err.Error())
	}
	if exists {
		if slot := m.slots[id]; slot != nil {
			slot.stop()
		}
	} else {
		m.slots[id] = newSlot
	}
	return Response{ID: req.ID, OK: true, Result: ProfileStatus{ConfigID: id, Protocol: cfg.Protocol, Port: port, State: "disconnected"}}
}
func (m *manager) ensureSlot(p storedProfile) (*proxySlot, error) {
	if slot := m.slots[p.ID]; slot != nil {
		return slot, nil
	}
	slot, err := newProxySlot(p.Port)
	if err != nil {
		return nil, err
	}
	m.slots[p.ID] = slot
	return slot, nil
}
func (m *manager) status(p storedProfile) ProfileStatus {
	cfg, _ := parseConfig(p.Raw)
	s := ProfileStatus{ConfigID: p.ID, Port: p.Port, State: "disconnected"}
	if cfg != nil {
		s.Protocol = cfg.Protocol
	}
	if slot := m.slots[p.ID]; slot != nil {
		s.State, s.Error = slot.status()
	} else {
		s.State = "error"
		s.Error = "Reserved local proxy port is unavailable."
	}
	return s
}
func (m *manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, slot := range m.slots {
		slot.close()
	}
}

type proxySlot struct {
	mu          sync.Mutex
	listener    net.Listener
	engine      engine
	connections map[net.Conn]bool
	lastError   string
	started     time.Time
	closed      bool
}

func newProxySlot(port int) (*proxySlot, error) {
	l, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	s := &proxySlot{listener: l, connections: map[net.Conn]bool{}}
	go s.serve()
	return s, nil
}
func (s *proxySlot) serve() {
	for {
		c, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			c.Close()
			return
		}
		s.connections[c] = true
		s.mu.Unlock()
		go func() {
			defer func() { c.Close(); s.mu.Lock(); delete(s.connections, c); s.mu.Unlock() }()
			serveSOCKS(c, s.dial)
		}()
	}
}
func (s *proxySlot) running() bool           { s.mu.Lock(); defer s.mu.Unlock(); return s.engine != nil }
func (s *proxySlot) setError(message string) { s.mu.Lock(); s.lastError = message; s.mu.Unlock() }
func (s *proxySlot) start(e engine) {
	s.mu.Lock()
	s.engine = e
	s.lastError = ""
	s.started = time.Now()
	s.mu.Unlock()
}
func (s *proxySlot) stop() {
	s.mu.Lock()
	e := s.engine
	s.engine = nil
	s.lastError = ""
	for c := range s.connections {
		c.Close()
	}
	s.mu.Unlock()
	if e != nil {
		e.Close()
	}
}
func (s *proxySlot) close() {
	s.mu.Lock()
	s.closed = true
	s.listener.Close()
	s.mu.Unlock()
	s.stop()
}
func (s *proxySlot) status() (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.engine == nil {
		if s.lastError != "" {
			return "error", s.lastError
		}
		return "disconnected", ""
	}
	handshake := s.engine.LastHandshake()
	if !handshake.IsZero() && time.Since(handshake) < 180*time.Second {
		return "connected", ""
	}
	if time.Since(s.started) > 30*time.Second {
		return "connecting", "Waiting for a successful server handshake. Open a routed site to initiate traffic."
	}
	return "connecting", ""
}
