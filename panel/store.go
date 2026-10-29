package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

type Config struct {
	AllowChanges  bool     `json:"allow_changes"`
	ExpectedBoard string   `json:"expected_board"`
	Radio2G       string   `json:"radio_2g"`
	Protected5G   string   `json:"protected_5g"`
	Bridge        string   `json:"lan_bridge"`
	Existing      []string `json:"allowed_existing"`
	Hosts         []string `json:"allowed_hosts"`
}
type Project struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	SSID       string `json:"ssid"`
	Password   string `json:"password"`
	Interface  string `json:"interface"`
	NextNumber int    `json:"next_number,omitempty"`
}
type RadioSnapshot struct {
	Name     string `json:"name"`
	Settings Row    `json:"settings"`
	Disabled any    `json:"disabled"`
}
type Recovery struct {
	Radio       RadioSnapshot `json:"radio"`
	Keys        []string      `json:"temporary_keys"`
	Children    []Row         `json:"children"`
	Ports       []Row         `json:"ports"`
	Watchdog    string        `json:"watchdog,omitempty"`
	Fingerprint Row           `json:"watchdog_fingerprint,omitempty"`
}
type Data struct {
	Projects []Project `json:"projects"`
	History  []Row     `json:"history"`
	Recovery *Recovery `json:"recovery"`
}
type Store struct {
	mu   sync.Mutex
	path string
	data Data
}

func newStore(dir string) (*Store, error) {
	if e := os.MkdirAll(dir, 0700); e != nil {
		return nil, e
	}
	s := &Store{path: filepath.Join(dir, "state.json"), data: Data{Projects: []Project{}, History: []Row{}}}
	b, e := os.ReadFile(s.path)
	if e == nil {
		if e = json.Unmarshal(b, &s.data); e != nil {
			return nil, e
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	if s.data.Projects == nil {
		s.data.Projects = []Project{}
	}
	if s.data.History == nil {
		s.data.History = []Row{}
	}
	return s, nil
}
func (s *Store) read() Data { s.mu.Lock(); defer s.mu.Unlock(); return clone(s.data) }
func (s *Store) change(fn func(*Data)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old := clone(s.data)
	fn(&s.data)
	b, e := json.Marshal(s.data)
	if e != nil {
		s.data = old
		return e
	}
	if len(b) > 4*1024*1024 {
		s.data = old
		return errors.New("Данните надвишават лимита от 4 MiB за вътрешната памет")
	}
	// Private file plus fsync and rename: never overwrite a recovery journal in place.
	f, e := os.OpenFile(s.path+".tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if e != nil {
		s.data = old
		return e
	}
	_, e = f.Write(b)
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e == nil {
		e = os.Rename(s.path+".tmp", s.path)
	}
	if e != nil {
		s.data = old
	}
	return e
}
