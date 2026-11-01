package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

type storedProfile struct {
	ID   string `json:"id"`
	Port int    `json:"port"`
	Raw  string `json:"config"`
}
type database struct {
	Version  int                      `json:"version"`
	NextPort int                      `json:"nextPort"`
	Profiles map[string]storedProfile `json:"profiles"`
}
type store interface {
	Load() (*database, error)
	Save(*database) error
}
type protectedStore struct{ path string }

func newDatabase() *database {
	return &database{Version: 1, NextPort: 24000, Profiles: map[string]storedProfile{}}
}
func (s protectedStore) Load() (*database, error) {
	encrypted, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return newDatabase(), nil
	}
	if err != nil {
		return nil, errors.New("could not read protected configuration store")
	}
	if len(encrypted) > 40<<20 {
		return nil, errors.New("configuration store is too large")
	}
	raw, err := unprotect(encrypted)
	if err != nil {
		return nil, errors.New("could not decrypt configurations for this Windows user")
	}
	defer clear(raw)
	var db database
	if json.Unmarshal(raw, &db) != nil {
		return nil, errors.New("invalid configuration store")
	}
	if db.Version != 1 || db.Profiles == nil || db.NextPort < 24000 || db.NextPort > 64000 || len(db.Profiles) > 128 {
		return nil, errors.New("unsupported configuration store")
	}
	ports := map[int]bool{}
	for key, p := range db.Profiles {
		if key != p.ID || !validConfigID(key) || p.Port < 24000 || p.Port >= db.NextPort || ports[p.Port] {
			return nil, errors.New("invalid stored profile metadata")
		}
		ports[p.Port] = true
		if _, err := parseConfig(p.Raw); err != nil {
			return nil, errors.New("stored configuration is not supported")
		}
	}
	return &db, nil
}
func (s protectedStore) Save(db *database) error {
	raw, err := json.Marshal(db)
	if err != nil {
		return errors.New("could not serialize configuration store")
	}
	defer clear(raw)
	encrypted, err := protect(raw)
	if err != nil {
		return errors.New("could not protect configuration store")
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return errors.New("could not create configuration directory")
	}
	f, err := os.CreateTemp(dir, ".store-*")
	if err != nil {
		return errors.New("could not create configuration store")
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(encrypted)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, s.path)
	}
	if err != nil {
		return errors.New("could not save protected configuration store")
	}
	return nil
}
