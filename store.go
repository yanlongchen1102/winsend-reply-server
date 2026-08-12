package main

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DeviceInfo is a paired device inside a group.
type DeviceInfo struct {
	Name     string    `json:"name"`
	JoinedAt time.Time `json:"joinedAt"`
}

// Group is a pairing group (one user's devices).
type Group struct {
	// JoinKeySHA256 is the hex SHA-256 of the client-derived join key.
	// The relay never sees the raw sync code.
	JoinKeySHA256 string                `json:"joinKeySha256"`
	Devices       map[string]DeviceInfo `json:"devices"`
	CreatedAt     time.Time             `json:"createdAt"`
}

// Store persists group membership to a JSON file.
type Store struct {
	path   string
	mu     sync.Mutex
	Groups map[string]*Group `json:"groups"`
}

func NewStore(path string) (*Store, error) {
	s := &Store{path: path, Groups: map[string]*Group{}}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, err
	}
	if s.Groups == nil {
		s.Groups = map[string]*Group{}
	}
	log.Printf("[store] loaded %d group(s) from %s", len(s.Groups), path)
	return s, nil
}

// GetOrCreateGroup returns the group for groupID, creating it with the given
// join key hash if it does not exist yet.
func (s *Store) GetOrCreateGroup(groupID, joinKeySHA256 string) *Group {
	s.mu.Lock()
	defer s.mu.Unlock()

	if g, ok := s.Groups[groupID]; ok {
		return g
	}
	g := &Group{
		JoinKeySHA256: joinKeySHA256,
		Devices:       map[string]DeviceInfo{},
		CreatedAt:     time.Now(),
	}
	s.Groups[groupID] = g
	s.saveLocked()
	log.Printf("[store] created group %s", groupID)
	return g
}

// AddDevice registers a device in the group. Returns false if the group
// already has maxDevicesPerGroup devices and this is a new device.
func (s *Store) AddDevice(groupID, deviceID, name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	g, ok := s.Groups[groupID]
	if !ok {
		return false
	}
	if _, exists := g.Devices[deviceID]; exists {
		if name != "" {
			g.Devices[deviceID] = DeviceInfo{Name: name, JoinedAt: g.Devices[deviceID].JoinedAt}
			s.saveLocked()
		}
		return true
	}
	if len(g.Devices) >= maxDevicesPerGroup {
		return false
	}
	g.Devices[deviceID] = DeviceInfo{Name: name, JoinedAt: time.Now()}
	s.saveLocked()
	log.Printf("[store] group %s: device %s (%s) joined, total=%d", groupID, deviceID, name, len(g.Devices))
	return true
}

func (s *Store) saveLocked() {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		log.Printf("[store] mkdir failed: %v", err)
		return
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		log.Printf("[store] marshal failed: %v", err)
		return
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		log.Printf("[store] write failed: %v", err)
		return
	}
	if err := os.Rename(tmp, s.path); err != nil {
		log.Printf("[store] rename failed: %v", err)
	}
}
